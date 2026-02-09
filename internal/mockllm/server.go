package mockllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type Server struct {
	log     *slog.Logger
	ln      net.Listener
	srv     *http.Server
	baseURL string

	reqSeq atomic.Int64
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type requestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []requestMessage `json:"messages"`
	Tools    []toolSpec       `json:"tools,omitempty"`
}

type execResult struct {
	Tool   string `json:"tool"`
	Ok     bool   `json:"ok"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Message      any    `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Start поднимает локальный мок OpenAI-совместимого Chat Completions API.
// Он предназначен только для smoke/CI и не пытается быть полноценной LLM.
func Start(addr string, log *slog.Logger) (*Server, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("mockllm: addr is required")
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With(slog.String("component", "mockllm"))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		log:     log,
		ln:      ln,
		baseURL: "http://" + ln.Addr().String(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		err := s.srv.Serve(s.ln)
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		s.log.Error("mock llm server stopped", slog.Any("error", err))
	}()

	return s, nil
}

func (s *Server) BaseURL() string {
	return s.baseURL
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"invalid json"}}`, http.StatusBadRequest)
		return
	}

	toolNames := make(map[string]struct{}, len(req.Tools))
	for _, t := range req.Tools {
		if t.Type != "function" {
			continue
		}
		if name := strings.TrimSpace(t.Function.Name); name != "" {
			toolNames[name] = struct{}{}
		}
	}

	// Достаём system/user подсказки (если есть) для простых эвристик.
	var systemText, userText string
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			systemText = m.Content
		case "user":
			userText = m.Content
		}
	}

	// Финальная реакция по умолчанию.
	assistantContent := "OK"
	var toolCall any // если не nil -> tool call

	// Если предыдущий шаг был tool-result, то обычно "подхватываем" stdout.
	if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
		var out execResult
		if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &out); err == nil {
			switch strings.TrimSpace(out.Tool) {
			case "http_request":
				assistantContent = strings.TrimSpace(out.Stdout)
			default:
				assistantContent = "OK"
			}
		}
	} else {
		// Специальные ответы под smoke-конфиги.
		switch {
		case strings.Contains(systemText, "Верни ровно слово: TEXT"):
			assistantContent = "TEXT"
		case strings.Contains(systemText, "Ответь коротко: JSON"):
			assistantContent = "JSON"
		case strings.Contains(systemText, "DAG PASSED"):
			assistantContent = "DAG PASSED."
		case strings.Contains(strings.ToLower(userText), "ping"):
			assistantContent = "pong"
		case strings.Contains(userText, "items:"):
			assistantContent = "items: []"
		}

		// Инструментальные шаги smoke: отдаём один tool_call, затем на следующей итерации вернём финальный текст.
		if _, ok := toolNames["apply_patch"]; ok && strings.Contains(userText, ".agent/artifacts/tools-smoke") {
			toolCall = map[string]any{
				"id":   "tool_mock_apply_patch",
				"type": "function",
				"function": map[string]any{
					"name":      "apply_patch",
					"arguments": `{"input":"*** Begin Patch\n*** Update File: .agent/artifacts/tools-smoke/b.txt\n@@\n-two\n+TWO\n*** Add File: .agent/artifacts/tools-smoke/new.txt\n+created\n*** End Patch\n"}`,
				},
			}
		} else if _, ok := toolNames["http_request"]; ok && strings.Contains(userText, ".agent/artifacts/functions-smoke/payload.txt") {
			toolCall = map[string]any{
				"id":   "tool_mock_http_request",
				"type": "function",
				"function": map[string]any{
					"name":      "http_request",
					"arguments": `{"method":"GET","url":".agent/artifacts/functions-smoke/payload.txt"}`,
				},
			}
		} else if _, ok := toolNames["shell"]; ok && strings.Contains(userText, ".agent/artifacts/tools-smoke") && strings.Contains(userText, "list.txt") {
			toolCall = map[string]any{
				"id":   "tool_mock_shell",
				"type": "function",
				"function": map[string]any{
					"name":      "shell",
					"arguments": `{"command":["bash","-lc","set -euo pipefail\nls -la .agent/artifacts/tools-smoke > .agent/artifacts/tools-smoke/list.txt\ncat .agent/artifacts/tools-smoke/b.txt > .agent/artifacts/tools-smoke/b.txt.bak\n"],"timeout_ms":30000}`,
				},
			}
		}
	}

	id := s.reqSeq.Add(1)
	resp := chatResponse{
		ID:      fmt.Sprintf("chatcmpl-mock-%d", id),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   strings.TrimSpace(req.Model),
	}
	if strings.TrimSpace(resp.Model) == "" {
		resp.Model = "mock"
	}

	var msg map[string]any
	finishReason := "stop"
	if toolCall != nil {
		msg = map[string]any{
			"role":       "assistant",
			"content":    "",
			"tool_calls": []any{toolCall},
		}
		finishReason = "tool_calls"
	} else {
		msg = map[string]any{
			"role":    "assistant",
			"content": assistantContent,
		}
	}

	resp.Choices = append(resp.Choices, struct {
		Index        int    `json:"index"`
		Message      any    `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	})

	// Токены — заглушка, но полезна для логов.
	resp.Usage.PromptTokens = 1
	resp.Usage.CompletionTokens = 1
	resp.Usage.TotalTokens = 2

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
