// SPEC: _spec/internal/provider/model-resolution.puml, _spec/internal/provider/model-catalog.puml
package provider

import (
	"fmt"
	"sort"
	"strings"
)

// RoleVars are the proveo-level model role variables, in the order they are
// reported. Each harness entrypoint translates these into its own names —
// CECLI_MODEL / AIDER_MODEL, OPENCODE_MODEL, ANTHROPIC_MODEL — so an operator
// sets the role once and it lands wherever that harness expects it.
var RoleVars = []string{"ARCHITECT_MODEL", "EDITOR_MODEL", "SMALL_MODEL"}

// Roles is a session's model assignment, keyed by RoleVars name. Empty entries
// mean the role is unset, which is normal: most sessions set one.
type Roles map[string]string

// RolesFrom reads the role variables through lookup, normalizing each value so a
// loose spelling resolves the same as an exact one.
func RolesFrom(lookup func(string) string) Roles {
	r := Roles{}
	for _, v := range RoleVars {
		if got := strings.TrimSpace(lookup(v)); got != "" {
			r[v] = got
		}
	}
	return r
}

// Providers is the set of providers this assignment implies, in registry order.
// This is phase 1 of resolution: it runs on the host, before the container
// exists, because the result decides the broker's route table and the Squid
// allowlist — both of which are argv on docker run.
//
// A model whose provider cannot be attributed contributes nothing rather than a
// guess. That is safe because the broker routes every detected provider anyway:
// attribution here informs warnings and optional narrowing, never whether a key
// gets injected.
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

// MissingKeys reports the roles whose provider has no credential among detected.
// This is the diagnosis an operator could not previously get: a role pointed at
// a provider with no key produced a generic "invalid API key" from the harness,
// with nothing naming which role or which variable was at fault.
//
// Roles whose provider cannot be attributed are omitted — an unknown id is not
// evidence of a missing key, and a false warning about a model that works is
// worse than silence.
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

// Canonical returns the assignment with each value normalized, for storing in
// agent-settings.yml. Canonical rather than harness-specific: a translated
// spelling would pin the entry to whatever the catalog said the day it was
// written, so a later correction could never reach it.
func (r Roles) Canonical() map[string]string {
	out := make(map[string]string, len(r))
	for role, model := range r {
		out[roleKey(role)] = normalizeIntent(model)
	}
	return out
}

// RolesFromCanonical rebuilds an assignment from a stored map, ignoring keys it
// does not recognize so a hand-edited or future settings file cannot break a run.
func RolesFromCanonical(m map[string]string) Roles {
	r := Roles{}
	for _, role := range RoleVars {
		if v := strings.TrimSpace(m[roleKey(role)]); v != "" {
			r[role] = v
		}
	}
	return r
}

// roleKey is the short yaml key for a role var: ARCHITECT_MODEL -> main.
// "main" rather than "architect" because it is the name three of the four
// harnesses use for the primary model; architect is aider's term.
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

// normalizeIntent turns a loose operator spelling into the id the registry
// matches against: "Kimi K3", "kimi_k3" and "kimi.k3" are one intent.
//
// It deliberately preserves a provider prefix ("moonshot/kimi-k3"), because that
// prefix is how a harness disambiguates a model several providers serve, and
// ModelProvider reads it. Only the separators and case are normalized.
func normalizeIntent(model string) string {
	s := strings.TrimSpace(strings.ToLower(model))
	for _, sep := range []string{" ", "_", "."} {
		s = strings.ReplaceAll(s, sep, "-")
	}
	// Collapse runs introduced by the replacements above.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
