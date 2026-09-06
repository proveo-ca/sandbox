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

// planFallback is the model a run falls back to when the operator's own choice
// cannot authenticate on the side they picked, keyed by harness family and
// billing side.
//
// opencode is the case that needs it. Its bridge default is
// anthropic/claude-sonnet-4-5, so a Go subscriber holding ONLY OPENCODE_API_KEY
// answers `subscription`, gets both plans registered from that one key — and
// then lands on an anthropic model they have no key for. They skipped /connect
// and hit /models instead; the in-session step only moved.
//
// muse-spark-1.3-contributor is a provisional pick, not a considered one: it is
// a model the Go plan serves today. Go's lineup rotates (35 models at the time
// of writing), so this WILL go stale, and TestPlanFallbacksAreRealModels is
// what turns that into a test failure rather than a run that dies on an
// unknown id. SPEC: _spec/internal/credentials/credential-decisions.puml
var planFallback = map[string]map[Billing]string{
	"opencode": {
		BillPlan:    "opencode-go/muse-spark-1.3-contributor",
		BillMetered: "", // Zen is metered like any provider key; nothing to prefer
	},
}

// PlanFallback returns the model to use for a harness on a billing side, or ""
// when there is nothing better to offer than what the operator already has.
func PlanFallback(harness string, want Billing) string {
	return planFallback[strings.ToLower(strings.TrimSpace(harness))][want]
}

// Feasible drops the role models this run cannot actually authenticate and
// returns what it substituted, so the caller can say so.
//
// The operator's ARCHITECT_MODEL and friends are a PREFERENCE, not a mandate:
// a value carried in a shell rc or a project .env was written for some other
// run, and honouring it when the credential behind it is absent or withheld
// produces a session that cannot make a single model call. Warning about that
// and launching anyway asks the operator to fix it from inside a broken run.
//
// A model is unfeasible when its provider is withheld by the auth answer, or
// when nothing on the host can authenticate it. Anything else is left exactly
// as the operator wrote it. SPEC: _spec/internal/credentials/credential-decisions.puml
func (r Roles) Feasible(harness string, want Billing, withheld []string, usable func(string) bool) (Roles, []string) {
	fallback := PlanFallback(harness, want)
	if fallback == "" || len(r) == 0 {
		return r, nil
	}
	off := map[string]bool{}
	for _, w := range withheld {
		off[w] = true
	}
	out := make(Roles, len(r))
	for k, v := range r {
		out[k] = v
	}
	var swapped []string
	for _, role := range RoleVars { // deterministic order
		model, ok := r[role]
		if !ok {
			continue
		}
		p := ModelProvider(normalizeIntent(model))
		switch {
		case p == "":
			continue // a bare or local id we do not judge
		case off[p]:
			swapped = append(swapped, fmt.Sprintf(
				"%s=%s cannot run here — %s is withheld by this run's auth answer; using %s",
				role, model, p, fallback))
		case usable != nil && !usable(p):
			swapped = append(swapped, fmt.Sprintf(
				"%s=%s cannot run here — no credential for %s; using %s",
				role, model, p, fallback))
		default:
			continue
		}
		out[role] = fallback
	}
	return out, swapped
}
