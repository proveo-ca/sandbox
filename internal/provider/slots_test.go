// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml
package provider

import (
	"strings"
	"testing"
)

// A vendor-locked slot refuses a model from another provider.
func TestVendorLockedSlotRefusesForeignModels(t *testing.T) {
	t.Parallel()
	tbl := BridgeTable{"claudecode": []Bridge{
		{Slot: "main", Targets: []string{"ANTHROPIC_MODEL"},
			Roles: []string{"ARCHITECT_MODEL", "EDITOR_MODEL"}, Transform: "bare", Provider: "anthropic"},
	}}
	tests := []struct {
		name, model string
		want        bool
	}{
		{"prefixed anthropic", "anthropic/claude-opus-5", true},
		{"bare anthropic", "claude-sonnet-5", true},
		{"haiku", "claude-haiku-4-5", true},
		// Anthropic, and outside opus/sonnet/haiku: the rule is the PROVIDER, so a
		// family the operator has not heard of is still accepted.
		{"fable", "claude-fable-5", true},
		{"prefixed openai", "openai/gpt-5", false},
		{"bare openai", "gpt-5", false},
		{"bare xai", "grok-4", false},
		{"prefixed google", "google/gemini-2.5-pro", false},
		// Unresolvable is ACCEPTED: local and shim endpoints serve arbitrary ids,
		// and refusing what cannot be classified breaks --local-model.
		{"ollama", "ollama/llama3", true},
		{"openai-compatible", "openai-compatible/whatever", true},
		{"unknown id", "some-model-nobody-lists", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := Roles{"ARCHITECT_MODEL": tc.model}
			slots := tbl.EffectiveSlots("claudecode", r)
			env := tbl.ResolvedEnv("claudecode", r)
			refused := tbl.RefusedSlots("claudecode", r)

			if got := len(slots) == 1; got != tc.want {
				t.Errorf("EffectiveSlots showed %d slots, want accepted=%v", len(slots), tc.want)
			}
			if got := env["ANTHROPIC_MODEL"] != ""; got != tc.want {
				t.Errorf("ResolvedEnv ANTHROPIC_MODEL=%q, want accepted=%v", env["ANTHROPIC_MODEL"], tc.want)
			}
			if got := len(refused) == 0; got != tc.want {
				t.Errorf("RefusedSlots = %d, want accepted=%v", len(refused), tc.want)
			}
			// A refusal names the variable, both vendors, and what it costs.
			if !tc.want {
				why := refused[0].Reason()
				for _, want := range []string{"ARCHITECT_MODEL", "ANTHROPIC_MODEL", "anthropic", "left unset"} {
					if !strings.Contains(why, want) {
						t.Errorf("reason %q lacks %q", why, want)
					}
				}
			}
		})
	}
}

// An unconstrained slot takes anything — every harness-named target, including
// OPENCODE_MODEL, whose own default is an anthropic model.
func TestUnconstrainedSlotsTakeAnyProvider(t *testing.T) {
	t.Parallel()
	tbl := BridgeTable{"opencode": []Bridge{
		{Slot: "main", Targets: []string{"OPENCODE_MODEL"}, Roles: []string{"ARCHITECT_MODEL"}, Transform: "normalize"},
	}}
	for _, m := range []string{"openai/gpt-5", "anthropic/claude-opus-5", "grok-4", "ollama/llama3"} {
		env := tbl.ResolvedEnv("opencode", Roles{"ARCHITECT_MODEL": m})
		if env["OPENCODE_MODEL"] == "" {
			t.Errorf("%s: an unconstrained slot must accept it", m)
		}
	}
}
