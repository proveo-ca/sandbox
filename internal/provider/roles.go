// SPEC: _spec/internal/provider/model-resolution.puml,
// _spec/internal/provider/model-catalog.puml
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
// keeps off the wire.
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

// AnsweredBilling maps the auth row's answer onto a billing side.
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
// SPEC: _spec/internal/agentsettings/choice-cache.puml
func (r Roles) Canonical() map[string]string {
	out := make(map[string]string, len(r))
	for role, model := range r {
		out[roleKey(role)] = canonicalModel(model)
	}
	return out
}

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

// SPEC: _spec/internal/provider/model-catalog.puml
var planFallback = map[string]map[Billing][]string{
	"opencode": {
		BillPlan: {
			"opencode/muse-spark-1.3-contributor-free", // $0, Zen free tier
			"opencode/glm-5-free",                      // $0, fallback of the fallback
		},
		BillMetered: nil, // Zen is metered like any provider key; nothing to prefer
	},
}

// PlanFallback returns the model to use for a harness on a billing side, or
// "" when there is nothing better to offer than what the operator already
// has.
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
				note += " (free tier — proveo cannot verify which plan this key" +
					" entitles; name an opencode-go/ or opencode/ model to choose)"
			}
			notes = append(notes, note+" — skipped "+strings.Join(skipped, ", "))
		}
	}
	return out, notes
}
