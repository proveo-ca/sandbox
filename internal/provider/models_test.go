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
