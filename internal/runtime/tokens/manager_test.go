package tokens

import "testing"

func TestResolveModelProfileKnownModelsIgnorePrefix(t *testing.T) {
	tests := []struct {
		model      string
		wantName   string
		wantHFRepo string
		wantWindow int
	}{
		{
			model:      "cloudru/gpt-oss-120b",
			wantName:   "gpt-oss-120b",
			wantHFRepo: "openai/gpt-oss-120b",
			wantWindow: 131072,
		},
		{
			model:      "vendor/Qwen3-Next-80B-A3B-Instruct",
			wantName:   "qwen3-next-80b-a3b-instruct",
			wantHFRepo: "Qwen/Qwen3-Next-80B-A3B-Instruct",
			wantWindow: 262144,
		},
		{
			model:      "acme/glm-4.6",
			wantName:   "glm-4.6",
			wantHFRepo: "zai-org/GLM-4.6",
			wantWindow: 200000,
		},
	}

	for _, tt := range tests {
		profile := ResolveModelProfile(tt.model, nil)
		if profile.DisplayName != tt.wantName {
			t.Fatalf("model %q: expected display name %q, got %q", tt.model, tt.wantName, profile.DisplayName)
		}
		if profile.HFTokenizerModelID != tt.wantHFRepo {
			t.Fatalf("model %q: expected HF repo %q, got %q", tt.model, tt.wantHFRepo, profile.HFTokenizerModelID)
		}
		if profile.ContextWindow != tt.wantWindow {
			t.Fatalf("model %q: expected context window %d, got %d", tt.model, tt.wantWindow, profile.ContextWindow)
		}
	}
}

func TestResolveModelProfileFallback(t *testing.T) {
	profile := ResolveModelProfile("tenant/unknown-model", nil)
	if profile.HFTokenizerModelID != "" {
		t.Fatalf("expected empty tokenizer repo for unknown model, got %q", profile.HFTokenizerModelID)
	}
	if profile.ContextWindow != DefaultFallbackContextWindow {
		t.Fatalf("expected fallback context window %d, got %d", DefaultFallbackContextWindow, profile.ContextWindow)
	}
}
