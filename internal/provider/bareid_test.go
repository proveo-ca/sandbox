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
