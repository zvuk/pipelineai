package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openai/openai-go/v2"
)

func TestModelConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ModelConfig
		wantErr bool
	}{
		{
			name: "valid",
			cfg: ModelConfig{
				BaseURL:        "http://localhost:1234/v1",
				Model:          "gpt-test",
				RequestTimeout: time.Second,
			},
		},
		{
			name:    "empty url",
			cfg:     ModelConfig{Model: "gpt-test", RequestTimeout: time.Second},
			wantErr: true,
		},
		{
			name:    "empty model",
			cfg:     ModelConfig{BaseURL: "http://localhost:1234/v1", RequestTimeout: time.Second},
			wantErr: true,
		},
		{
			name:    "invalid timeout",
			cfg:     ModelConfig{BaseURL: "http://localhost:1234/v1", Model: "gpt-test", RequestTimeout: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("did not expect an error: %v", err)
			}
		})
	}
}

func TestClientCreateChatCompletionSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("expected Authorization header, got %q", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payloadModel, ok := payload["model"].(string); !ok || payloadModel != "gpt-smoke" {
			t.Fatalf("expected model gpt-smoke, got %#v", payload["model"])
		}

		resp := map[string]any{
			"id":                 "chatcmpl-1",
			"object":             "chat.completion",
			"created":            time.Unix(0, 0).Unix(),
			"model":              "gpt-smoke",
			"service_tier":       nil,
			"system_fingerprint": "",
			"choices": []any{
				map[string]any{
					"index":         0,
					"finish_reason": "stop",
					"logprobs": map[string]any{
						"content": []any{},
						"refusal": []any{},
					},
					"message": map[string]any{
						"role":          "assistant",
						"content":       "pong",
						"refusal":       "",
						"annotations":   []any{},
						"audio":         nil,
						"function_call": map[string]any{"name": "", "arguments": ""},
						"tool_calls": []any{
							map[string]any{
								"id":   "tool_1",
								"type": "function",
								"function": map[string]any{
									"name":      "demo",
									"arguments": "{\"echo\":\"pong\"}",
								},
							},
						},
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     5,
				"completion_tokens": 7,
				"total_tokens":      12,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := NewClient(ModelConfig{
		BaseURL:        ts.URL,
		APIKey:         "secret",
		Model:          "gpt-smoke",
		RequestTimeout: time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	resp, err := client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("expected successful response, got error: %v", err)
	}

	if resp.Model != "gpt-smoke" {
		t.Fatalf("expected response model gpt-smoke, got %s", resp.Model)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Content != "pong" {
		t.Fatalf("unexpected response content: %s", choice.Message.Content)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool_call, got %d", len(choice.Message.ToolCalls))
	}
	if choice.Message.ToolCalls[0].Function.Name != "demo" {
		t.Fatalf("expected function name demo, got %s", choice.Message.ToolCalls[0].Function.Name)
	}
}

func TestClientCreateChatCompletionAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "bad request",
				"type":    "invalid_request_error",
				"code":    "invalid",
				"param":   "messages",
			},
		})
	}))
	defer ts.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := NewClient(ModelConfig{BaseURL: ts.URL, Model: "gpt-smoke", RequestTimeout: time.Second}, logger)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{Messages: []Message{{Role: RoleUser, Content: "ping"}}})
	if err == nil {
		t.Fatal("expected API error but got nil")
	}

	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected error of type openai.Error, got %T", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected error status: %d", apiErr.StatusCode)
	}
	if apiErr.Message != "bad request" {
		t.Fatalf("unexpected error message: %s", apiErr.Message)
	}
}
