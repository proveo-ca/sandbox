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

// An operator holding both an API key and a subscription token must not have the
// choice made by array order.
func TestResolveWithHonoursAnExplicitCredential(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-api", "CLAUDE_CODE_OAUTH_TOKEN": "oauth-tok"}
	get := func(k string) string { return env[k] }

	def, _ := Resolve("anthropic", get)
	if def.EnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("declared order should pick the API key, got %q", def.EnvVar)
	}
	sub, _ := ResolveWith("anthropic", "CLAUDE_CODE_OAUTH_TOKEN", get)
	if sub.EnvVar != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("explicit choice ignored, got %q", sub.EnvVar)
	}
	if sub.Value != "Bearer oauth-tok" || sub.Header != "authorization" {
		t.Errorf("subscription token must use its own header/shape: %+v", sub)
	}
	// An unavailable preference falls back rather than failing closed.
	delete(env, "CLAUDE_CODE_OAUTH_TOKEN")
	fb, _ := ResolveWith("anthropic", "CLAUDE_CODE_OAUTH_TOKEN", get)
	if fb.EnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("missing preference should fall back to the declared order, got %q", fb.EnvVar)
	}
	if got := AuthVars("anthropic"); len(got) != 2 {
		t.Errorf("AuthVars(anthropic) = %v, want two options", got)
	}
}
