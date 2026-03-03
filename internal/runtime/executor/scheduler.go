package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

// RunWithNeeds выполняет зависимости шага по DAG (needs) и затем сам шаг.
// Параллелит независимые шаги; логирует завершение каждого шага.
func (e *Executor) RunWithNeeds(ctx context.Context, target string, parallel int) error {
	if _, ok := e.getStep(target); !ok {
		return fmt.Errorf("executor: target step %s not found", target)
	}
	// Построим подграф: множество шагов, включая зависимости
	nodes, edges, indegree, err := e.subgraph(target)
	if err != nil {
		return err
	}

	// Очередь готовых к запуску шагов (indegree==0)
	ready := make([]string, 0)
	for id := range nodes {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}

	var mu sync.Mutex
	done := map[string]bool{}
	for len(ready) > 0 {
		var wg sync.WaitGroup
		errs := make(chan error, len(ready))
		for _, id := range ready {
			sid := id
			step, ok := e.getStep(sid)
			if !ok {
				return fmt.Errorf("executor: step %s not found", sid)
			}
			if step.Template {
				mu.Lock()
				done[sid] = true
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				name, desc := e.renderStepMeta(step)
				e.log.Info("step start", slog.String("step", sid), slog.String("type", step.Type), slog.String("name", name), slog.String("description", crop(desc, 600)))
				if err := e.runStepWithPolicy(ctx, step, sid, parallel, name); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				done[sid] = true
				mu.Unlock()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		// Рассчитать следующую волну.
		ready = nextReadyWave(done, edges, indegree)
	}
	if !done[target] {
		return fmt.Errorf("executor: target step %s is marked as template and cannot be executed", target)
	}
	return nil
}

// RunAll выполняет все нетемплейтные шаги сценария с учётом зависимостей (needs).
// Независимые шаги исполняются параллельно волнами, размер волны ограничен параметром parallel.
func (e *Executor) RunAll(ctx context.Context, parallel int) error {
	// Построим полный граф по всем шагам
	nodes := map[string]bool{}
	edges := map[string][]string{}
	indegree := map[string]int{}

	// Инициализация узлов
	e.muSteps.RLock()
	for id, s := range e.stepByID {
		_ = s
		nodes[id] = true
		edges[id] = []string{}
		indegree[id] = 0
	}

	// Рёбра: dep -> id
	for id, s := range e.stepByID {
		for _, dep := range s.Needs {
			if _, ok := e.stepByID[dep]; !ok {
				return fmt.Errorf("executor: unknown dependency %s for step %s", dep, id)
			}
			edges[dep] = append(edges[dep], id)
			indegree[id]++
		}
	}
	e.muSteps.RUnlock()

	// Очередь готовых шагов
	ready := make([]string, 0)
	for id := range nodes {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}

	var mu sync.Mutex
	done := map[string]bool{}
	for len(ready) > 0 {
		var wg sync.WaitGroup
		errs := make(chan error, len(ready))
		// Ограничиваем параллелизм волны
		sem := make(chan struct{}, max(1, parallel))
		for _, id := range ready {
			sid := id
			step, ok := e.getStep(sid)
			if !ok {
				return fmt.Errorf("executor: step %s not found", sid)
			}
			if step.Template {
				mu.Lock()
				done[sid] = true
				mu.Unlock()
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				name, _ := e.renderStepMeta(step)
				if err := e.runStepWithPolicy(ctx, step, sid, parallel, name); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				done[sid] = true
				mu.Unlock()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		// Следующая волна.
		ready = nextReadyWave(done, edges, indegree)
	}

	e.muSteps.RLock()
	defer e.muSteps.RUnlock()
	// Проверим, что все нетемплейтные шаги завершены
	for id, s := range e.stepByID {
		if s.Template {
			continue
		}
		if !done[id] {
			return fmt.Errorf("executor: not all steps finished, pending: %s", id)
		}
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// nextReadyWave вычисляет следующий слой DAG на основе выполненных узлов.
func nextReadyWave(done map[string]bool, edges map[string][]string, indegree map[string]int) []string {
	next := make([]string, 0)
	for u, outs := range edges {
		if !done[u] {
			continue
		}
		for _, v := range outs {
			if done[v] {
				continue
			}
			indegree[v]--
			if indegree[v] == 0 {
				next = append(next, v)
			}
		}
	}
	return next
}

// subgraph строит множество узлов подграфа (target + все его зависимости), рёбра и входные степени
func (e *Executor) subgraph(target string) (map[string]bool, map[string][]string, map[string]int, error) {
	nodes := map[string]bool{}
	edges := map[string][]string{}
	indegree := map[string]int{}

	var collect func(string) error
	collect = func(id string) error {
		s, ok := e.getStep(id)
		if !ok {
			return fmt.Errorf("executor: step %s not found (referenced in needs)", id)
		}
		if nodes[id] {
			return nil
		}
		nodes[id] = true
		if _, ok := edges[id]; !ok {
			edges[id] = []string{}
		}
		if _, ok := indegree[id]; !ok {
			indegree[id] = 0
		}
		for _, dep := range s.Needs {
			if _, ok := e.getStep(dep); !ok {
				return fmt.Errorf("executor: unknown dependency %s for step %s", dep, id)
			}
			if err := collect(dep); err != nil {
				return err
			}
			// dep -> id
			edges[dep] = append(edges[dep], id)
			indegree[id]++
		}
		return nil
	}
	if err := collect(target); err != nil {
		return nil, nil, nil, err
	}
	return nodes, edges, indegree, nil
}

// stepTimeoutFor возвращает таймаут для конкретного шага с учётом defaults.step_timeout и минимального порога.
func (e *Executor) stepTimeoutFor(step dsl.Step) time.Duration {
	const minTO = 30 * time.Second
	// дефолт 5 минут
	to := 5 * time.Minute
	if e.cfg.Defaults != nil && e.cfg.Defaults.StepTimeout != nil {
		to = e.cfg.Defaults.StepTimeout.Duration
	}
	if step.Timeout != nil {
		to = step.Timeout.Duration
	}
	if to < minTO {
		to = minTO
	}
	return to
}
