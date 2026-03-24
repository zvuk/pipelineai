package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
)

// buildLLMArtifactRecord формирует полезную нагрузку артефакта и вычисляет хэш для дедупликации.
func buildLLMArtifactRecord(system, prompt string, resp llm.ChatCompletionResponse, messages []llm.Message, metrics *stepTokenMetrics) (map[string]any, string) {
	// Хэш считаем от структурированного набора полей
	hw := struct {
		System   string                     `json:"system"`
		Prompt   string                     `json:"prompt"`
		Response llm.ChatCompletionResponse `json:"response"`
	}{System: system, Prompt: prompt, Response: resp}

	data, _ := json.Marshal(hw)
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])

	rec := map[string]any{
		"system":   system,
		"prompt":   prompt,
		"response": resp,
		"messages": messages,
		"model":    resp.Model,
		"tokens":   resp.Usage,
		"meta": map[string]any{
			"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
			"hash":         hash,
		},
	}
	if metrics != nil {
		rec["token_metrics"] = metrics
	}
	return rec, hash
}

// buildLLMErrorArtifactRecord формирует артефакт неуспешного LLM-шага с диагностикой.
func buildLLMErrorArtifactRecord(system, prompt string, resp llm.ChatCompletionResponse, messages []llm.Message, metrics *stepTokenMetrics, err error) (map[string]any, string) {
	rec, hash := buildLLMArtifactRecord(system, prompt, resp, messages, metrics)
	if err != nil {
		rec["error"] = err.Error()
	}
	if metrics != nil && metrics.BudgetExceededReason == "" && err != nil {
		metrics.BudgetExceededReason = err.Error()
	}
	return rec, hash
}
