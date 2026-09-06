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

// Precedence: the remembered answer outranks an ambient .env, and .env outranks
// the plan default. The order used to be the reverse — MergeRoles let a shell
// rc override the answer the operator had just given this agent in the prompt,
// which made the remembered choice a suggestion.
func TestRememberedChoiceOutranksTheEnv(t *testing.T) {
	t.Parallel()
	remembered := Roles{"ARCHITECT_MODEL": "opencode-go/glm-5.3"}
	env := Roles{"ARCHITECT_MODEL": "opencode-go/kimi-k3"}
	got, notes := ResolveRoles(remembered, env, "opencode", BillPlan, nil, func(string) bool { return true })
	if got["ARCHITECT_MODEL"] != "opencode-go/glm-5.3" {
		t.Errorf("ARCHITECT_MODEL = %q, want the remembered choice", got["ARCHITECT_MODEL"])
	}
	if len(notes) != 0 {
		t.Errorf("reported a skip when the first tier was usable: %v", notes)
	}
}

// Each tier is gated on its own. An unusable remembered choice falls to .env
// rather than straight past it to the default — .env is tier 2, not a tiebreak.
func TestAnUnusableRememberedChoiceFallsToTheEnv(t *testing.T) {
	t.Parallel()
	remembered := Roles{"ARCHITECT_MODEL": "anthropic/claude-opus-5"}
	env := Roles{"ARCHITECT_MODEL": "opencode-go/glm-5.3"}
	got, notes := ResolveRoles(remembered, env, "opencode", BillPlan,
		[]string{"anthropic"}, func(string) bool { return true })
	if got["ARCHITECT_MODEL"] != "opencode-go/glm-5.3" {
		t.Errorf("ARCHITECT_MODEL = %q, want the .env value", got["ARCHITECT_MODEL"])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "withheld") {
		t.Fatalf("notes = %v, want one naming the skipped tier and why", notes)
	}
	if !strings.Contains(notes[0], "remembered choice") {
		t.Errorf("the note does not say WHICH tier was skipped: %q", notes[0])
	}
}

// Only when every tier above it is unusable does the default apply — and it
// applies to every role, because each maps to an agent.
func TestBothTiersUnusableFallsToThePlanDefault(t *testing.T) {
	t.Parallel()
	env := Roles{
		"ARCHITECT_MODEL": "anthropic/claude-opus-5",
		"EDITOR_MODEL":    "anthropic/claude-sonnet-4-6",
		"SMALL_MODEL":     "anthropic/claude-haiku-4-5",
	}
	got, notes := ResolveRoles(nil, env, "opencode", BillPlan, nil,
		func(n string) bool { return n != "anthropic" })
	want := PlanFallback("opencode", BillPlan)
	if want == "" {
		t.Fatal("no plan fallback resolves; the whole list has gone stale")
	}
	for _, role := range RoleVars {
		if got[role] != want {
			t.Errorf("%s = %q, want %q", role, got[role], want)
		}
	}
	if len(notes) != 3 {
		t.Errorf("substituted 3 roles but reported %d — a silent swap is worse than the warning it replaces", len(notes))
	}
	for _, n := range notes {
		if !strings.Contains(n, "no credential") {
			t.Errorf("note does not say why: %q", n)
		}
	}
}

// A usable choice is left exactly as written. This is a preference honoured,
// not a policy imposed.
func TestAUsableChoiceIsLeftAlone(t *testing.T) {
	t.Parallel()
	env := Roles{"ARCHITECT_MODEL": "opencode-go/kimi-k3", "EDITOR_MODEL": "anthropic/claude-opus-5"}
	got, notes := ResolveRoles(nil, env, "opencode", BillPlan, nil, func(string) bool { return true })
	if got["ARCHITECT_MODEL"] != "opencode-go/kimi-k3" || got["EDITOR_MODEL"] != "anthropic/claude-opus-5" {
		t.Errorf("rewrote a runnable choice: %v", got)
	}
	if len(notes) != 0 {
		t.Errorf("reported skips with nothing withheld: %v", notes)
	}
}

// Harnesses with no plan default are untouched, so this cannot leak into
// cursor or claudecode by accident.
func TestNoFallbackMeansNoSubstitution(t *testing.T) {
	t.Parallel()
	env := Roles{"ARCHITECT_MODEL": "anthropic/claude-opus-5"}
	for _, h := range []string{"cursor", "claudecode", "cecli"} {
		got, _ := ResolveRoles(nil, env, h, BillPlan, []string{"anthropic"}, func(string) bool { return false })
		if _, set := got["ARCHITECT_MODEL"]; set {
			t.Errorf("%s: substituted with no fallback defined: %v", h, got)
		}
	}
}

// The default list is judgement written down against a lineup that rotates, so
// every id in it has to still resolve through the registry AND still land on
// the side it claims. A stale entry is a run that dies on an unknown model;
// this makes it a build failure instead. models.dev carries release_date and
// cost but no "recommended" field — deriving "newest" picks omen-alpha — so the
// list is ordered by hand and degrades to its next entry.
func TestPlanFallbacksAreRealModels(t *testing.T) {
	t.Parallel()
	for harness, sides := range planFallback {
		for side, models := range sides {
			for _, model := range models {
				p := ModelProvider(model)
				if p == "" {
					t.Errorf("%s/%v fallback %q resolves to no provider", harness, side, model)
					continue
				}
				if _, ok := Lookup(p); !ok {
					t.Errorf("%s/%v fallback %q names %q, not in the registry", harness, side, model, p)
				}
				// A fallback must not spend a side the operator did not choose.
				// Matching the side satisfies that; so does costing nothing,
				// which is the entitlement-safe escape: proveo cannot see which
				// plan a key entitles, so a free id is the only thing it can
				// pick without assuming one.
				if got := ModelBilling(model); got != side && !IsFreeTier(model) {
					t.Errorf("%s fallback %q is billed %v, is listed as the %v choice, "+
						"and is not free — it would spend a side nobody chose",
						harness, model, got, side)
				}
			}
			if len(models) > 0 && PlanFallback(harness, side) == "" {
				t.Errorf("%s/%v has entries but none resolves — the whole list is stale", harness, side)
			}
		}
	}
}

// Headless is the case this split exists for. Nobody was asked a billing
// question, so no side is claimed — but a model with no credential behind it is
// unrunnable whoever is watching, and launching on one only to warn to a log
// nobody reads until the job fails is the worst of both.
func TestFeasibilityAppliesWithNoAnswerGiven(t *testing.T) {
	t.Parallel()
	env := Roles{"ARCHITECT_MODEL": "anthropic/claude-opus-5"}
	got, notes := ResolveRoles(nil, env, "opencode", BillUnknown, nil,
		func(n string) bool { return n != "anthropic" })

	want := PlanFallback("opencode", BillUnknown)
	if want == "" {
		t.Fatal("BillUnknown resolves no fallback, so a headless run has nowhere to land")
	}
	if got["ARCHITECT_MODEL"] != want {
		t.Errorf("ARCHITECT_MODEL = %q, want %q — feasibility does not need an answer",
			got["ARCHITECT_MODEL"], want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "no credential") {
		t.Errorf("notes = %v, want the skip reported", notes)
	}
	// It must not silently pick a side: unanswered resolves through the plan
	// list because that is the only one populated, not because anyone chose it.
	if BillUnknown == BillPlan {
		t.Fatal("BillUnknown and BillPlan collapsed; an unanswered run would claim a side")
	}
}

// And with an answer absent, a model on the "wrong" side is left alone — there
// is no wrong side when nobody named one.
func TestNoAnswerJudgesNoBillingSide(t *testing.T) {
	t.Parallel()
	r := Roles{"ARCHITECT_MODEL": "opencode/gpt-5.6-luna"} // Zen: metered
	if got := r.BillingClashes(""); len(got) != 0 {
		t.Errorf("BillingClashes with no answer = %v, want silence", got)
	}
	// Feasible on every count, so resolution leaves it exactly as written.
	got, notes := ResolveRoles(nil, r, "opencode", BillUnknown, nil, func(string) bool { return true })
	if got["ARCHITECT_MODEL"] != "opencode/gpt-5.6-luna" || len(notes) != 0 {
		t.Errorf("rewrote a runnable model with no answer given: %v %v", got, notes)
	}
}

// Holding OPENCODE_API_KEY does not tell you which PLAN it entitles — Zen and
// Go share the variable, which is the central finding this package encodes. A
// fallback of `opencode-go/muse-spark-1.3-contributor` therefore assumed Go,
// and on a Zen key opencode answered "configured model is not valid" and
// silently fell through to Whisper Large V3 Turbo on Groq: a 2024
// speech-to-text model driving a coding agent, with no error the operator
// could act on.
//
// So a fallback may only name something the gateway serves to ANY key. A
// plan-gated prefix is exactly the assumption proveo cannot make.
func TestFallbacksNeverAssumeAnEntitlement(t *testing.T) {
	t.Parallel()
	for harness, sides := range planFallback {
		for side, models := range sides {
			for _, model := range models {
				if strings.HasPrefix(strings.ToLower(model), "opencode-go/") {
					t.Errorf("%s/%v fallback %q is gated on the Go subscription — proveo "+
						"cannot see whether this key has it, and a wrong guess degrades the "+
						"run to whatever opencode picks instead", harness, side, model)
				}
				if !IsFreeTier(model) {
					t.Errorf("%s/%v fallback %q is not free-tier; a fallback is the model "+
						"that RUNS, not the best one", harness, side, model)
				}
			}
		}
	}
}
