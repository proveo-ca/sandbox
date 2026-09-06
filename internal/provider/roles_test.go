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

// An INTENT is normalized — "Kimi K3" is something a human typed and wants
// matched. A provider-qualified id is not an intent, it is an address, and it
// survives verbatim; see TestCanonicalKeepsQualifiedIdsExact.
func TestCanonicalRoundTrip(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "Kimi K3", "SMALL_MODEL": "grok-4.5-fast"}
	stored := r.Canonical()
	if stored["main"] != "kimi-k3" || stored["small"] != "grok-4-5-fast" {
		t.Fatalf("Canonical() = %v", stored)
	}
	back := RolesFromCanonical(stored)
	if back["ARCHITECT_MODEL"] != "kimi-k3" || back["SMALL_MODEL"] != "grok-4-5-fast" {
		t.Errorf("round trip lost data: %v", back)
	}
	if _, ok := back["EDITOR_MODEL"]; ok {
		t.Error("an unset role must stay unset")
	}
}

// One credential cannot express OpenCode's two plans. Zen (pay-as-you-go) and
// Go (the $10/mo subscription) are one gateway on one OPENCODE_API_KEY, split
// only by the model prefix — so an operator can answer "subscription", hold
// exactly the right key, and still be metered because the role names a Zen
// model. Only the id catches that.
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

// A Go subscriber holding ONLY OPENCODE_API_KEY answers `subscription`, gets
// both plans registered from that one key — and then lands on the bridge
// default, anthropic/claude-sonnet-4-5, for a provider they have no key for.
// They skipped /connect and hit /models instead; the in-session step moved
// rather than went away. The role var is a preference, so it yields.
func TestUnfeasibleRoleModelsYieldToTheChosenPlan(t *testing.T) {
	t.Parallel()
	r := Roles{
		"ARCHITECT_MODEL": "anthropic/claude-opus-5",
		"EDITOR_MODEL":    "anthropic/claude-sonnet-4-6",
		"SMALL_MODEL":     "anthropic/claude-haiku-4-5",
	}
	got, swapped := r.Feasible("opencode", BillPlan, []string{"anthropic"}, func(string) bool { return true })

	want := PlanFallback("opencode", BillPlan)
	if want == "" {
		t.Fatal("no plan fallback for opencode; the substitution can never happen")
	}
	// Every role maps to an agent, so every role has to land somewhere runnable.
	for _, role := range RoleVars {
		if got[role] != want {
			t.Errorf("%s = %q, want %q", role, got[role], want)
		}
	}
	if len(swapped) != 3 {
		t.Errorf("substituted %d roles but reported %d — a silent swap is worse than the warning it replaced",
			3, len(swapped))
	}
	for _, m := range swapped {
		if !strings.Contains(m, "withheld") {
			t.Errorf("substitution message does not say why: %q", m)
		}
	}
	// The operator's own map is not mutated under them.
	if r["ARCHITECT_MODEL"] != "anthropic/claude-opus-5" {
		t.Error("Feasible mutated the caller's Roles instead of returning a new one")
	}
}

// A model that CAN authenticate is left exactly as written. This is a
// preference being honoured, not a policy being applied.
func TestFeasibleRoleModelsAreLeftAlone(t *testing.T) {
	t.Parallel()
	r := Roles{"ARCHITECT_MODEL": "opencode-go/kimi-k3", "EDITOR_MODEL": "anthropic/claude-opus-5"}
	got, swapped := r.Feasible("opencode", BillPlan, nil, func(string) bool { return true })
	if got["ARCHITECT_MODEL"] != "opencode-go/kimi-k3" || got["EDITOR_MODEL"] != "anthropic/claude-opus-5" {
		t.Errorf("rewrote a runnable choice: %v", got)
	}
	if len(swapped) != 0 {
		t.Errorf("reported substitutions with nothing withheld: %v", swapped)
	}
}

// No key for the provider is the same problem by a different route, and the
// message has to say which — "withheld" and "no credential" send the operator
// to different places.
func TestARoleWithNoCredentialAlsoYields(t *testing.T) {
	t.Parallel()
	r := Roles{"ARCHITECT_MODEL": "anthropic/claude-opus-5"}
	got, swapped := r.Feasible("opencode", BillPlan, nil, func(n string) bool { return n != "anthropic" })
	if got["ARCHITECT_MODEL"] != PlanFallback("opencode", BillPlan) {
		t.Errorf("ARCHITECT_MODEL = %q, want the plan fallback", got["ARCHITECT_MODEL"])
	}
	if len(swapped) != 1 || !strings.Contains(swapped[0], "no credential") {
		t.Errorf("swapped = %v, want one message naming the missing credential", swapped)
	}
}

// Harnesses with no plan fallback are untouched, so this cannot leak into
// cursor or claudecode by accident.
func TestNoFallbackMeansNoSubstitution(t *testing.T) {
	t.Parallel()
	r := Roles{"ARCHITECT_MODEL": "anthropic/claude-opus-5"}
	for _, h := range []string{"cursor", "claudecode", "cecli"} {
		got, swapped := r.Feasible(h, BillPlan, []string{"anthropic"}, func(string) bool { return false })
		if got["ARCHITECT_MODEL"] != "anthropic/claude-opus-5" || len(swapped) != 0 {
			t.Errorf("%s: substituted with no fallback defined: %v %v", h, got, swapped)
		}
	}
}

// The fallback is a provisional pick against a lineup that rotates, so it has
// to be a model the registry actually knows. A stale id here is a run that dies
// on an unknown model; this turns it into a test failure instead.
func TestPlanFallbacksAreRealModels(t *testing.T) {
	t.Parallel()
	for harness, sides := range planFallback {
		for side, model := range sides {
			if model == "" {
				continue
			}
			p := ModelProvider(model)
			if p == "" {
				t.Errorf("%s/%v fallback %q resolves to no provider", harness, side, model)
				continue
			}
			if _, ok := Lookup(p); !ok {
				t.Errorf("%s/%v fallback %q names %q, which is not in the registry", harness, side, model, p)
			}
			// It must also land on the side it claims to serve.
			if got := ModelBilling(model); got != side {
				t.Errorf("%s fallback %q is billed %v, but is registered as the %v choice",
					harness, model, got, side)
			}
		}
	}
}

// normalizeIntent turns "." into "-", which is harmless for claude-opus-5 and
// fatal for every dotted id opencode serves. Stored through the choice cache,
// `opencode-go/glm-5.3` came back as `opencode-go/glm-5-3` — a model that does
// not exist — on the SECOND run of any operator who let the prompt remember
// their answer. Anthropic ids carry no dots, so the defaults could not trip it.
// SPEC: _spec/internal/agentsettings/choice-cache.puml
func TestCanonicalKeepsQualifiedIdsExact(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"opencode-go/muse-spark-1.3-contributor",
		"opencode-go/glm-5.3",
		"opencode-go/qwen3.8-max",
		"opencode/gpt-5.6-luna",
		"anthropic/claude-opus-5",
	} {
		back := RolesFromCanonical(Roles{"ARCHITECT_MODEL": id}.Canonical())["ARCHITECT_MODEL"]
		if back != id {
			t.Errorf("round trip corrupted %q into %q — the agent is handed a model that does not exist", id, back)
		}
	}
}
