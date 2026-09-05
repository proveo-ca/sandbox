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

func (r Roles) Canonical() map[string]string {
	out := make(map[string]string, len(r))
	for role, model := range r {
		out[roleKey(role)] = normalizeIntent(model)
	}
	return out
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
