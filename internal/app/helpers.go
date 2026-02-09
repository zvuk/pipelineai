package app

import (
	"fmt"
	"time"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

// scenarioTimeout возвращает таймаут всего запуска сценария.
func scenarioTimeout(cfg *dsl.Config, _ string) (time.Duration, error) {
	const minTO = 30 * time.Second
	defaultScenario := 20 * time.Minute
	scenario := defaultScenario
	if cfg.Defaults != nil && cfg.Defaults.ScenarioTimeout != nil {
		scenario = cfg.Defaults.ScenarioTimeout.Duration
	}
	if scenario < minTO {
		scenario = minTO
	}
	if scenario <= 0 {
		return 0, fmt.Errorf("invalid scenario timeout")
	}
	return scenario, nil
}
