package provider

import "testing"

func TestBareModelIDsResolveTheirProvider(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5": "anthropic", "gpt-5": "openai", "o3-mini": "openai",
		"grok-4": "xai", "gemini-2.5-pro": "google", "kimi-k2": "moonshot",
		"glm-4.6": "zai", "deepseek-v3": "deepseek", "mistral-large": "mistral",
		"openai/gpt-5": "openai", "ollama/llama3": "", "totally-unknown": "",
	} {
		if got := ModelProvider(model); got != want {
			t.Errorf("ModelProvider(%q) = %q, want %q", model, got, want)
		}
	}
}

// Open-weights families are served by many providers, so the id names the model
// but not the host. Pinning one to its originator routes the credential wrong.
func TestOpenWeightsIDsDoNotPinAProvider(t *testing.T) {
	for _, m := range []string{"gpt-oss", "gpt-oss-120b", "llama-3.3-70b", "qwen2.5-coder", "mixtral-8x7b"} {
		if got := ModelProvider(m); got != "" {
			t.Errorf("ModelProvider(%q) = %q, want \"\" — that model is multi-hosted", m, got)
		}
	}
	// The vendor's own ids must still resolve.
	if got := ModelProvider("gpt-5"); got != "openai" {
		t.Errorf("ModelProvider(gpt-5) = %q, want openai", got)
	}
}
