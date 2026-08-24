// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml, _spec/internal/choiceui/wireframe.puml
package provider

import (
	"bufio"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/entrypoint"
)

// Bridge is one row of defs/bridges/<harness>.tsv — how the shared role vars become
// the env vars one harness actually reads. The table is the single declaration: the
// shell applies it at container start, and this package reads the same rows to show
// resolved slots in the prompt header before a container exists.
type Bridge struct {
	Slot      string   // header-facing name; "-" means internal, never displayed
	Targets   []string // env vars to set; an explicit value already set always wins
	Roles     []string // fallback chain, first non-empty wins
	Default   string   // literal, "$OTHER" to copy a target, or "" for none
	Transform string   // "normalize", "bare", or ""
}

// Slot is one model assignment a harness actually reads. Harnesses do not agree on
// how many they expose: Claude Code routes internally and offers two, cursor one,
// while cecli and opencode take three. Printing all three role vars on a harness
// that reads two advertises an assignment it cannot honour, and the operator only
// finds out after a run behaves differently than the header implied.
type Slot struct {
	Name  string // header-facing slot: main, small, build, editor, weak
	Role  string // the RoleVar that filled it
	Model string
}

// BridgeTable is every harness's rows, keyed by harness name. A nil table is a
// valid zero value: EffectiveSlots then shows every role rather than hiding one.
type BridgeTable map[string][]Bridge

// LoadBridges parses defs/bridges/*.tsv out of fsys. It takes an fs.FS rather than
// importing the root embed package, because packages under internal/ must stay
// buildable from a context holding only cmd/ and internal/ — the egress-proxy image
// builds exactly that, and an import of the root package breaks it.
func LoadBridges(fsys fs.FS) (BridgeTable, error) {
	matches, err := fs.Glob(fsys, "defs/bridges/*.tsv")
	if err != nil {
		return nil, err
	}
	tab := BridgeTable{}
	for _, path := range matches {
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		rows, err := parseBridges(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		name := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".tsv")
		tab[name] = rows
	}
	return tab, nil
}

// parseBridges is deliberately strict: a malformed row would otherwise mean a slot
// silently vanishes from the header while the shell still sets it.
func parseBridges(src string) ([]Bridge, error) {
	var out []Bridge
	sc := bufio.NewScanner(strings.NewReader(src))
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		f := strings.Split(text, "\t")
		if len(f) != 5 {
			return nil, fmt.Errorf("line %d: want 5 tab-separated columns, got %d", line, len(f))
		}
		b := Bridge{Slot: f[0], Targets: splitList(f[1]), Roles: splitList(f[2])}
		if f[3] != "-" {
			b.Default = f[3]
		}
		if f[4] != "-" {
			b.Transform = f[4]
		}
		if len(b.Targets) == 0 || len(b.Roles) == 0 {
			return nil, fmt.Errorf("line %d: targets and roles are both required", line)
		}
		out = append(out, b)
	}
	return out, sc.Err()
}

func splitList(s string) []string {
	if s == "" || s == "-" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Harnesses lists the harnesses that declare a bridge table.
func (t BridgeTable) Harnesses() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EffectiveSlots resolves r through the named harness's table and returns only the
// slots that harness will actually fill. Defaults are not applied: the header states
// what the operator chose, and a default that fires inside the container is not a
// choice. An unknown harness falls back to every role, because showing one model too
// many is a smaller error than hiding one that is in use.
func (t BridgeTable) EffectiveSlots(harness string, r Roles) []Slot {
	rows, ok := t[harness]
	if !ok {
		out := make([]Slot, 0, len(r))
		for _, kv := range r.Sorted() {
			out = append(out, Slot{Name: kv[0], Role: kv[0], Model: kv[1]})
		}
		return out
	}
	var out []Slot
	for _, row := range rows {
		if row.Slot == "-" {
			continue // internal back-fill, never shown
		}
		for _, role := range row.Roles {
			if v := r[role]; v != "" {
				out = append(out, Slot{Name: row.Slot, Role: role, Model: applyTransform(row.Transform, v)})
				break
			}
		}
	}
	return out
}

// applyTransform mirrors the shell so the header shows the value the harness will
// actually see, not the one the operator typed.
func applyTransform(kind, v string) string {
	switch kind {
	case "bare":
		if i := strings.LastIndex(v, "/"); i >= 0 {
			return v[i+1:]
		}
	case "normalize":
		return entrypoint.NormalizeModel(v)
	}
	return v
}
