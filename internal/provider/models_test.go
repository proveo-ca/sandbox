package provider

import "testing"

func TestCheckModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model     string
		wantKnown bool
		wantOK    bool
	}{
		{"claude-opus-5", true, true},
		{"anthropic/claude-sonnet-4-6", true, true},
		{"claude-haiku-4-5", true, true},
		// the real typo this guard exists for: the Haiku id is 4-5, not 4-6
		{"claude-haiku-4-6", false, true},
		{"claude-sonnet-9", false, true},
		// no list for these providers → callers must stay silent
		{"gpt-5.5", false, false},
		{"ollama/gemma4", false, false},
		{"ollama_chat/gemma4", false, false},
		{"some-self-hosted-thing", false, false},
		{"", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			known, ok := CheckModel(tc.model)
			if known != tc.wantKnown || ok != tc.wantOK {
				t.Errorf("CheckModel(%q) = (known=%v, ok=%v), want (known=%v, ok=%v)",
					tc.model, known, ok, tc.wantKnown, tc.wantOK)
			}
		})
	}
}

// models.dev spells OpenCode's two plans as two provider ids; the registry holds
// ONE entry because key and host are shared. Both prefixes must land on it, or a
// Go model id pins a provider the registry cannot look up and MissingKeys names
// no credential for it.
func TestModelProviderFoldsOpenCodeGoOntoOpenCode(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"opencode/claude-sonnet-5", "opencode-go/glm-5", "OpenCode-Go/kimi-k2.5"} {
		if got := ModelProvider(m); got != "opencode" {
			t.Errorf("ModelProvider(%q) = %q, want opencode", m, got)
		}
	}
	if _, ok := Lookup(ModelProvider("opencode-go/glm-5")); !ok {
		t.Error("the provider an opencode-go/ id resolves to is not in the registry")
	}
	want := "ARCHITECT_MODEL=opencode-go/glm-5 needs OPENCODE_API_KEY (opencode), which is not set"
	got := Roles{"ARCHITECT_MODEL": "opencode-go/glm-5"}.MissingKeys(nil)
	if len(got) != 1 || got[0] != want {
		t.Errorf("MissingKeys = %q, want [%q]", got, want)
	}
}

func TestModelProviderIgnoresLocalEndpoints(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"ollama/gemma4", "ollama_chat/gemma4", "openai-compatible/whatever"} {
		if got := ModelProvider(m); got != "" {
			t.Errorf("ModelProvider(%q) = %q, want \"\" — local/shim endpoints serve arbitrary ids", m, got)
		}
	}
	if got := ModelProvider("claude-opus-5"); got != "anthropic" {
		t.Errorf("ModelProvider(bare claude id) = %q, want anthropic", got)
	}
}

func TestCheckModelToleratesNomenclature(t *testing.T) {
	t.Parallel()
	valid := []string{
		"claude-sonnet-4-5-20250929",         // Anthropic/litellm dated snapshot
		"claude-opus-4-5@20251101",           // Vertex dated form
		"anthropic/claude-sonnet-4.5",        // OpenRouter dots its versions
		"openrouter/anthropic/claude-opus-5", // litellm two-level id
	}
	for _, m := range valid {
		if known, ok := CheckModel(m); ok && !known {
			t.Errorf("CheckModel(%q) reported unknown — a valid id spelled another way must not warn", m)
		}
	}
	// A real typo must still be caught through the same normalization.
	if known, ok := CheckModel("anthropic/claude-haiku-4.6"); !ok || known {
		t.Errorf("CheckModel(dotted typo) = (known=%v, ok=%v), want (false, true)", known, ok)
	}
}
