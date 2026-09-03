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
	// Provider vendor-locks the slot: only a model resolving to it may be
	// assigned. Empty takes any provider. Declared, never derived.
	Provider string
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
		if len(f) != 6 {
			return nil, fmt.Errorf("line %d: want 6 tab-separated columns, got %d", line, len(f))
		}
		b := Bridge{Slot: f[0], Targets: splitList(f[1]), Roles: splitList(f[2])}
		if f[3] != "-" {
			b.Default = f[3]
		}
		if f[4] != "-" {
			b.Transform = f[4]
		}
		if f[5] != "-" {
			b.Provider = f[5]
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
			v := r[role]
			if v == "" {
				continue
			}
			// Refused, not shown: the header states what the run will use, and a
			// slot the bridge will not fill is not an assignment.
			if row.Accepts(v) {
				out = append(out, Slot{Name: row.Slot, Role: role, Model: applyTransform(row.Transform, v)})
			}
			break
		}
	}
	return out
}

// Accepts reports whether model may fill this slot. An unresolvable provider is
// accepted — local and shim endpoints serve arbitrary ids.
func (b Bridge) Accepts(model string) bool {
	if b.Provider == "" || strings.TrimSpace(model) == "" {
		return true
	}
	got := ModelProvider(model)
	return got == "" || got == b.Provider
}

// RefusedSlots names the assignments a harness's table will NOT make, and why —
// reported, never merely applied.
func (t BridgeTable) RefusedSlots(harness string, r Roles) []Refusal {
	var out []Refusal
	for _, row := range t[harness] {
		for _, role := range row.Roles {
			v := r[role]
			if v == "" {
				continue
			}
			if !row.Accepts(v) {
				out = append(out, Refusal{
					Slot: row.Slot, Role: role, Model: v,
					Want: row.Provider, Got: ModelProvider(v),
					Targets: append([]string(nil), row.Targets...),
				})
			}
			break // the first non-empty role wins, refused or not
		}
	}
	return out
}

// Refusal is one assignment a vendor-locked slot would not take.
type Refusal struct {
	Slot, Role, Model string
	Want, Got         string
	Targets           []string
}

// Reason is the one line proveo prints, naming the variable, the vendor it takes
// and the vendor it was handed.
func (r Refusal) Reason() string {
	return fmt.Sprintf("%s=%s resolves to %s, and %s takes models from %s only — the slot is left "+
		"unset, so the agent falls back to its own default",
		r.Role, r.Model, r.Got, strings.Join(r.Targets, "/"), r.Want)
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

// ResolvedEnv applies the whole bridge table for a harness and returns the env vars
// it produces, already transformed.
//
// This is the same walk apply_model_bridges does in shell, run on the host instead.
// Under sbx it has to be: a setup command cannot export into the agent, so the Kit
// carries ANTHROPIC_MODEL decided rather than shipping the tables and a bridge to
// recompute it inside. Defaults are deliberately not applied — the header states
// what the operator chose, and a default that fires in-container is not a choice.
func (t BridgeTable) ResolvedEnv(harness string, r Roles) map[string]string {
	rows, ok := t[harness]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, row := range rows {
		var val string
		for _, role := range row.Roles {
			if v := r[role]; v != "" {
				val = v
				break
			}
		}
		if val == "" || !row.Accepts(val) {
			continue
		}
		val = applyTransform(row.Transform, val)
		for _, target := range row.Targets {
			out[target] = val
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
