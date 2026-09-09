package provider

import (
	"strings"
	"testing"
)

// The case from the field: roles spanning two vendors must attribute to both,
// which is what tells the operator whether a key is missing.
func TestRolesSpanningVendors(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "kimi-3", "EDITOR_MODEL": "grok-4.5", "SMALL_MODEL": "grok-4.5-fast"}
	got := r.Providers()
	if len(got) != 2 {
		t.Fatalf("Providers() = %v, want moonshot and xai", got)
	}
	want := map[string]bool{"moonshot": true, "xai": true}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected provider %q", p)
		}
	}
}

func TestNormalizeIntent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Kimi K3", "kimi-k3"},
		{"kimi_k3", "kimi-k3"},
		{"grok-4.5", "grok-4-5"},
		{"  GROK-4.5-Fast ", "grok-4-5-fast"},
		{"moonshot/kimi-k3", "moonshot/kimi-k3"}, // prefix preserved: it disambiguates
	} {
		if got := normalizeIntent(tc.in); got != tc.want {
			t.Errorf("normalizeIntent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMissingKeysNamesRoleAndVar(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "kimi-k3", "EDITOR_MODEL": "grok-4.5"}
	msgs := r.MissingKeys([]string{"moonshot"}) // xai key absent
	if len(msgs) != 1 {
		t.Fatalf("want exactly one missing-key message, got %v", msgs)
	}
	for _, want := range []string{"EDITOR_MODEL", "grok-4.5", "XAI_API_KEY"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("message %q should mention %q", msgs[0], want)
		}
	}
}

// An id no table attributes must not produce a warning: an unknown model is not
// evidence of a missing key, and a false alarm about a working model is worse.
func TestUnattributableModelIsSilent(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "qwen-3-max"} // ambiguous open-weights family
	if got := r.Providers(); len(got) != 0 {
		t.Errorf("Providers() = %v, want none for an ambiguous id", got)
	}
	if got := r.MissingKeys(nil); len(got) != 0 {
		t.Errorf("MissingKeys() = %v, want silence", got)
	}
}

func TestBillingClashesReadThePrefixNotTheKey(t *testing.T) {
	t.Parallel()
	if got := ModelBilling("opencode-go/kimi-k3"); got != BillPlan {
		t.Errorf("opencode-go/ = %v, want BillPlan", got)
	}
	if got := ModelBilling("opencode/gpt-5-nano"); got != BillMetered {
		t.Errorf("opencode/ = %v, want BillMetered — that is the Zen balance", got)
	}
	// Both fold to the same broker route, which is why the raw prefix is read.
	if ModelProvider("opencode-go/kimi-k3") != ModelProvider("opencode/gpt-5-nano") {
		t.Error("the alias fold changed; ModelBilling depends on it staying one route")
	}

	r := Roles{"ARCHITECT_MODEL": "opencode/gpt-5-nano"}
	msgs := r.BillingClashes("subscription")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "per token") {
		t.Fatalf("BillingClashes = %v, want the Zen model flagged against a plan answer", msgs)
	}
	// The right prefix for that answer is silent.
	goRole := Roles{"ARCHITECT_MODEL": "opencode-go/kimi-k3"}
	if got := goRole.BillingClashes("subscription"); len(got) != 0 {
		t.Errorf("BillingClashes = %v, want silence", got)
	}
	// And the mirror.
	if got := r.BillingClashes("usage credits"); len(got) != 0 {
		t.Errorf("a Zen model under a metered answer is no clash, got %v", got)
	}
	// An unanswered row judges nothing.
	if got := r.BillingClashes(""); len(got) != 0 {
		t.Errorf("BillingClashes with no answer = %v, want silence", got)
	}
}

// Cursor's split is on the vendor's side, so no id can be judged against it.
func TestCursorModelsCarryNoBillingVerdict(t *testing.T) {
	t.Parallel()
	if got := ModelBilling("cursor/some-model"); got != BillUnknown {
		t.Errorf("cursor/ = %v, want BillUnknown — Cursor decides plan vs overage", got)
	}
	if !SplitsBilling("cursor") || !SplitsBilling("opencode") {
		t.Error("both gateways sell a plan AND metered usage on one credential")
	}
	if SplitsBilling("anthropic") {
		t.Error("anthropic's key is metered and its token is the plan — the credential decides")
	}
}
