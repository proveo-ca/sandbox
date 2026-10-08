// SPEC: _spec/internal/sbx/clone-workspace.puml
package credentials

import (
	"slices"
	"strings"
)

// EnvMerge is the host .env after MergeProjectEnv, and the names it sorted.
type EnvMerge struct {
	Body      []byte
	Applied   []string // set or removed in the sandbox, now in Body
	Conflicts []string // changed on the host during the run too; the host value stays
	Refused   []string // in strip; the sandbox never writes a brokered key
}

type envLine struct{ value, line string }

// MergeProjectEnv applies the sandbox's edits (base → edited) onto host, name by name.
func MergeProjectEnv(host, base, edited []byte, strip []string) EnvMerge {
	hostVals, _ := envAssignments(host)
	baseVals, baseOrder := envAssignments(base)
	editVals, editOrder := envAssignments(edited)
	var m EnvMerge
	set, del := map[string]string{}, map[string]bool{}
	var added []string
	for _, name := range unionInOrder(editOrder, baseOrder) {
		ev, inEdit := editVals[name]
		bv, inBase := baseVals[name]
		if inEdit == inBase && ev.value == bv.value {
			continue
		}
		if slices.Contains(strip, name) {
			m.Refused = append(m.Refused, name)
			continue
		}
		hv, inHost := hostVals[name]
		if inHost != inBase || hv.value != bv.value {
			m.Conflicts = append(m.Conflicts, name)
			continue
		}
		m.Applied = append(m.Applied, name)
		if !inEdit {
			del[name] = true
			continue
		}
		set[name] = ensureNewline(ev.line)
		if !inHost {
			added = append(added, name)
		}
	}
	var out strings.Builder
	written := map[string]bool{}
	for _, line := range strings.SplitAfter(string(host), "\n") {
		name, _, ok := envAssignment(line)
		switch {
		case ok && del[name]:
		case ok && set[name] != "":
			if !written[name] {
				out.WriteString(set[name])
				written[name] = true
			}
		default:
			out.WriteString(line)
		}
	}
	for _, name := range added {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
		out.WriteString(set[name])
	}
	m.Body = []byte(out.String())
	return m
}

func envAssignments(b []byte) (map[string]envLine, []string) {
	vals := map[string]envLine{}
	var order []string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		name, value, ok := envAssignment(line)
		if !ok {
			continue
		}
		if _, seen := vals[name]; !seen {
			order = append(order, name)
		}
		vals[name] = envLine{value: value, line: line}
	}
	return vals, order
}

func envAssignment(line string) (name, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	name, value, ok = strings.Cut(strings.TrimPrefix(t, "export "), "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" || strings.ContainsAny(name, " \t") {
		return "", "", false
	}
	return name, strings.TrimSpace(value), true
}

func unionInOrder(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
