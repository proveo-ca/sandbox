// SPEC: _spec/internal/provider/model-resolution.puml,
// _spec/internal/provider/model-catalog.puml
//
// SPEC: _spec/internal/provider/model-resolution.puml, _spec/internal/provider/model-catalog.puml
package provider

import (
	"fmt"
	"sort"
	"strings"
)

var RoleVars = []string{"ARCHITECT_MODEL", "EDITOR_MODEL", "SMALL_MODEL"}

// Roles is a session's model assignment, keyed by RoleVars name.
type Roles map[string]string

func RolesFrom(lookup func(string) string) Roles {
	r := Roles{}
	for _, v := range RoleVars {
		if got := strings.TrimSpace(lookup(v)); got != "" {
			r[v] = got
		}
	}
	return r
}

func (r Roles) Providers() []string {
	seen := map[string]bool{}
	for _, model := range r {
		if p := ModelProvider(normalizeIntent(model)); p != "" {
			seen[p] = true
		}
	}
	var out []string
	for _, name := range Names() { // registry order, so output is stable
		if seen[name] {
			out = append(out, name)
		}
	}
	return out
}

func (r Roles) MissingKeys(detected []string) []string {
	have := map[string]bool{}
	for _, d := range detected {
		have[d] = true
	}
	var out []string
	for _, role := range RoleVars { // deterministic order
		model, ok := r[role]
		if !ok {
			continue
		}
		p := ModelProvider(normalizeIntent(model))
		if p == "" || have[p] {
			continue
		}
		vars := AuthVars(p)
		want := p + " key"
		if len(vars) > 0 {
			want = strings.Join(vars, " or ")
		}
		out = append(out, fmt.Sprintf("%s=%s needs %s (%s), which is not set", role, model, want, p))
	}
	return out
}

// WithheldKeys names each role pointing at a provider this run's auth answer
// keeps off the wire. It is deliberately NOT MissingKeys: that one says "which
// is not set", and here the key is set, present and deliberately withheld —
// sending the operator to export a key they already have is the wrong errand.
//
// The clash is real and has to be said out loud. The auth row answers how the
// run is BILLED and the role vars answer which MODELS; when they contradict,
// proveo cannot pick a winner without overriding something the operator typed.
// SPEC: _spec/internal/credentials/credential-decisions.puml
func (r Roles) WithheldKeys(withheld []string, answer string) []string {
	off := map[string]bool{}
	for _, w := range withheld {
		off[w] = true
	}
	var out []string
	for _, role := range RoleVars { // deterministic order
		model, ok := r[role]
		if !ok {
			continue
		}
		p := ModelProvider(normalizeIntent(model))
		if p == "" || !off[p] {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s=%s routes to %s, which the %q auth answer withholds — point it at the "+
				"chosen provider, or answer the auth row differently", role, model, p, answer))
	}
	return out
}

// BillingClashes names each role whose MODEL contradicts the billing side the
// operator answered — the case one credential cannot express.
//
// OpenCode is the whole reason this exists. Zen (pay-as-you-go) and Go (the
// $10/mo subscription) are one gateway on one OPENCODE_API_KEY, distinguished
// only by the model prefix: opencode-go/<m> spends the plan, opencode/<m>
// spends the Zen balance. So an operator can answer "subscription", hold
// exactly the right key, and still be billed per token because the role names
// a Zen model. No credential check can catch that; only the id can.
//
// It warns and does not block. Go falls back to the Zen balance once its limits
// are spent when 'Use balance' is enabled, so even a correct opencode-go/ id
// can become metered mid-run — proveo cannot promise a billing side, only point
// at the one place the operator's own two answers disagree.
// SPEC: _spec/internal/credentials/credential-decisions.puml
func (r Roles) BillingClashes(answer string) []string {
	want, ok := AnsweredBilling(answer)
	if !ok {
		return nil
	}
	var out []string
	for _, role := range RoleVars { // deterministic order
		model, present := r[role]
		if !present {
			continue
		}
		got := ModelBilling(normalizeIntent(model))
		if got == BillUnknown || got == want {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s=%s is billed %s, but the auth row was answered %q — one key serves both, "+
				"so the model id is what decides", role, model, billingWord(got), answer))
	}
	return out
}

// AnsweredBilling maps the auth row's answer onto a billing side. The strings
// are the row's, and live in internal/credentials; matching on them here rather
// than importing keeps provider free of that dependency, and the contract test
// pins the two spellings together.
func AnsweredBilling(answer string) (Billing, bool) {
	switch strings.TrimSpace(strings.ToLower(answer)) {
	case "subscription":
		return BillPlan, true
	case "usage credits":
		return BillMetered, true
	}
	return BillUnknown, false
}

func billingWord(b Billing) string {
	if b == BillPlan {
		return "against a plan"
	}
	return "per token"
}

// Canonical is the form the choice cache stores.
//
// It normalizes an INTENT — "Kimi K3" is something a human typed and wants
// matched — but leaves a provider-qualified id exactly as written, because
// that is not an intent, it is an address. normalizeIntent turns "." into "-",
// which is harmless for `claude-opus-5` and destroys every dotted id opencode
// serves: `opencode-go/glm-5.3` came back as `opencode-go/glm-5-3`, a model
// that does not exist, on the second run of any operator who let the prompt
// remember their answer.
//
// Anthropic ids carry no dots, which is why this survived: the defaults could
// not trip it and only a Zen or Go model would.
// SPEC: _spec/internal/agentsettings/choice-cache.puml
func (r Roles) Canonical() map[string]string {
	out := make(map[string]string, len(r))
	for role, model := range r {
		out[roleKey(role)] = canonicalModel(model)
	}
	return out
}

// canonicalModel keeps a provider-qualified id verbatim and normalizes anything
// else. The slash is the same line the registry already draws between an
// address it can route and a bare string it has to guess at.
func canonicalModel(model string) string {
	if trimmed := strings.TrimSpace(model); strings.Contains(trimmed, "/") {
		return trimmed
	}
	return normalizeIntent(model)
}

func RolesFromCanonical(m map[string]string) Roles {
	r := Roles{}
	for _, role := range RoleVars {
		if v := strings.TrimSpace(m[roleKey(role)]); v != "" {
			r[role] = v
		}
	}
	return r
}

func roleKey(v string) string {
	switch v {
	case "ARCHITECT_MODEL":
		return "main"
	case "EDITOR_MODEL":
		return "editor"
	case "SMALL_MODEL":
		return "small"
	}
	return strings.ToLower(strings.TrimSuffix(v, "_MODEL"))
}

// Sorted returns role/model pairs in RoleVars order, for display.
func (r Roles) Sorted() [][2]string {
	var out [][2]string
	for _, role := range RoleVars {
		if v, ok := r[role]; ok {
			out = append(out, [2]string{role, v})
		}
	}
	if len(out) != len(r) { // any unrecognized keys, appended deterministically
		var extra []string
		for k := range r {
			if roleKey(k) == strings.ToLower(strings.TrimSuffix(k, "_MODEL")) && !knownRole(k) {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		for _, k := range extra {
			out = append(out, [2]string{k, r[k]})
		}
	}
	return out
}

func knownRole(v string) bool {
	for _, r := range RoleVars {
		if r == v {
			return true
		}
	}
	return false
}

func normalizeIntent(model string) string {
	s := strings.TrimSpace(strings.ToLower(model))
	for _, sep := range []string{" ", "_", "."} {
		s = strings.ReplaceAll(s, sep, "-")
	}
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// planFallback is tier 3 of model resolution: what a run uses when neither the
// operator's remembered answer nor their .env can authenticate on the side they
// picked. Ordered — the first id the registry still resolves wins.
//
// It is a LIST because the alternative is a pin that rots silently. Go rotated
// 35 models at the time of writing, and models.dev — opencode's own registry,
// and the source proveo already syncs — carries release_date, cost, context and
// capabilities for every one of them. What it does not carry is an opinion:
// there is no "recommended" field, and deriving "newest" picks `omen-alpha`,
// with `hy4-preview` and `ox-alpha-free` close behind. A registry can say what
// exists; it cannot say what is safe to point an agent at.
//
// So the judgement stays here, written down and reviewable, and the list
// degrades instead of breaking: drop muse-spark and glm-5.3 answers.
// TestPlanFallbacksAreRealModels fails the build when the whole list goes
// stale, which is the moment a human should look again.
// Refresh with scripts/rank-plan-fallbacks.py, which ranks models.dev by
// release_date after dropping ids that name themselves provisional — and
// prints what it dropped, because that exclusion is a guess about naming and
// not a contract. Read the excluded list before trusting the ranked one.
// SPEC: _spec/internal/provider/model-catalog.puml
var planFallback = map[string]map[Billing][]string{
	"opencode": {
		// ENTITLEMENT-SAFE ONLY. Not the best model — the one that runs.
		//
		// Holding OPENCODE_API_KEY does not tell you which plan it entitles.
		// That is the whole finding of this file: Zen and Go share the variable.
		// A fallback of opencode-go/muse-spark-1.3-contributor therefore ASSUMED
		// Go, and on a Zen key opencode answered "configured model is not valid"
		// and silently fell through to Whisper Large V3 Turbo on Groq — a 2024
		// speech-to-text model, driving a coding agent, with no error the
		// operator could act on.
		//
		// So the list holds only ids the gateway serves to any key: the `-free`
		// tier, which opencode.ai serves even unauthenticated. A Go subscriber
		// who wants their plan names an opencode-go/ model themselves and tier 1
		// or 2 honours it — proveo never has to guess an entitlement it cannot
		// see.
		BillPlan: {
			"opencode/muse-spark-1.3-contributor-free", // $0, Zen free tier
			"opencode/glm-5-free",                      // $0, fallback of the fallback
		},
		BillMetered: nil, // Zen is metered like any provider key; nothing to prefer
	},
}

// PlanFallback returns the model to use for a harness on a billing side, or ""
// when there is nothing better to offer than what the operator already has.
// PlanFallback returns the first entry the registry still resolves, or "" when
// the whole list has gone stale.
//
// BillUnknown is the unanswered case — a headless run, where nobody was asked a
// billing question. Feasibility still applies there (a model with no credential
// behind it is unrunnable whoever is watching), so tier 3 has to produce
// something; it takes the plan list, which is the only side a gateway harness
// populates. What it must NOT do is claim the operator chose a side.
func PlanFallback(harness string, want Billing) string {
	sides := planFallback[strings.ToLower(strings.TrimSpace(harness))]
	if want == BillUnknown {
		for _, s := range []Billing{BillPlan, BillMetered} {
			if len(sides[s]) > 0 {
				want = s
				break
			}
		}
	}
	for _, model := range sides[want] {
		if p := ModelProvider(model); p != "" {
			if _, ok := Lookup(p); ok {
				return model
			}
		}
	}
	return ""
}

// ResolveRoles picks each role's model by precedence, skipping any tier whose
// model cannot authenticate for this run:
//
//  1. the remembered answer  — this agent's own saved preference, the most
//     specific and most recent thing the operator said
//  2. the .env / shell value — ambient, written for some other run
//  3. the plan default       — planFallback, ordered
//
// The order used to be the reverse of that: MergeRoles let an ambient
// ARCHITECT_MODEL override the answer the operator had just given this agent
// in the prompt, which made the remembered choice a suggestion.
//
// Feasibility is a gate on EVERY tier, not just one. A tier that names a
// provider this run has no credential for, or one its auth answer withholds,
// is skipped and the next is tried — because honouring it produces a session
// that cannot make a single model call and then asks the operator to fix it
// from inside the broken run.
//
// Every skip is reported. A silent substitution is worse than the warning it
// replaces: the operator is owed the reason their typed value was not used.
// SPEC: _spec/internal/credentials/credential-decisions.puml
func ResolveRoles(remembered, env Roles, harness string, want Billing,
	withheld []string, usable func(string) bool) (Roles, []string) {
	off := map[string]bool{}
	for _, w := range withheld {
		off[w] = true
	}
	// why reports the reason a tier cannot be used, or "" when it can.
	why := func(model string) string {
		p := ModelProvider(normalizeIntent(model))
		switch {
		case p == "":
			return "" // a bare or local id we do not judge
		case off[p]:
			return p + " is withheld by this run's auth answer"
		case usable != nil && !usable(p):
			return "no credential for " + p
		}
		return ""
	}

	fallback := PlanFallback(harness, want)
	out, notes := Roles{}, []string(nil)
	for _, role := range RoleVars { // deterministic order
		var skipped []string
		picked := ""
		for _, tier := range []struct{ src, model string }{
			{"remembered choice", remembered[role]},
			{".env", env[role]},
		} {
			if tier.model == "" {
				continue
			}
			if reason := why(tier.model); reason != "" {
				skipped = append(skipped, fmt.Sprintf("%s (%s: %s)", tier.model, tier.src, reason))
				continue
			}
			picked = tier.model
			break
		}
		usedFallback := false
		if picked == "" && len(skipped) > 0 && fallback != "" {
			picked, usedFallback = fallback, true
		}
		if picked == "" {
			continue // nothing to say; the bridge default applies as before
		}
		out[role] = picked
		if len(skipped) > 0 {
			note := fmt.Sprintf("%s: using %s", role, picked)
			if usedFallback {
				// Say what this is. It is not a recommendation and it is not the
				// operator's plan: proveo cannot see which plan their key
				// entitles, so it picks the free tier the gateway serves to any
				// key and leaves the better choice to them.
				note += " (free tier — proveo cannot verify which plan this key" +
					" entitles; name an opencode-go/ or opencode/ model to choose)"
			}
			notes = append(notes, note+" — skipped "+strings.Join(skipped, ", "))
		}
	}
	return out, notes
}
