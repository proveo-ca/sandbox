// SPEC: _spec/internal/envreg/env-registry.puml
package envreg

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	literalName = regexp.MustCompile(`^PROVEO_[A-Z0-9_]+$`)
	familyName  = regexp.MustCompile(`^PROVEO_[A-Z0-9_]*<[A-Z]+>[A-Z0-9_]*$`)
)

const maxDoc = 90

func TestNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Vars {
		if seen[v.Name] {
			t.Errorf("%s is registered twice", v.Name)
		}
		seen[v.Name] = true
	}
}

func TestNamesAreLiteralOrOneFamily(t *testing.T) {
	for _, v := range Vars {
		literal, family := literalName.MatchString(v.Name), familyName.MatchString(v.Name)
		if !literal && !family {
			t.Errorf("%q is neither a PROVEO_* literal nor a family with one <PLACEHOLDER>", v.Name)
		}
	}
}

func TestFieldsAreOneLine(t *testing.T) {
	for _, v := range Vars {
		if strings.TrimSpace(v.Doc) == "" {
			t.Errorf("%s has no Doc", v.Name)
		}
		if strings.TrimSpace(v.Default) == "" {
			t.Errorf("%s has no Default", v.Name)
		}
		if strings.ContainsAny(v.Doc+v.Default, "\r\n") {
			t.Errorf("%s: Doc or Default spans more than one line", v.Name)
		}
		if n := utf8.RuneCountInString(v.Doc); n > maxDoc {
			t.Errorf("%s: Doc is %d runes, want ≤ %d", v.Name, n, maxDoc)
		}
	}
}

func TestScopesAreKnown(t *testing.T) {
	want := map[Scope]string{Operator: "operator", Maintainer: "maintainer", Internal: "internal"}
	for s, name := range want {
		if got := s.String(); got != name {
			t.Errorf("Scope(%d).String() = %q, want %q", int(s), got, name)
		}
	}
	for _, v := range Vars {
		if _, ok := want[v.Scope]; !ok {
			t.Errorf("%s has unknown scope %d", v.Name, int(v.Scope))
		}
	}
	total := 0
	for _, s := range []Scope{Operator, Maintainer, Internal} {
		total += len(ByScope(s))
	}
	if total != len(Vars) {
		t.Errorf("ByScope over every scope covers %d vars, want %d", total, len(Vars))
	}
}

func TestVarsSortByScopeThenName(t *testing.T) {
	less := func(i, j int) bool {
		if Vars[i].Scope != Vars[j].Scope {
			return Vars[i].Scope < Vars[j].Scope
		}
		return Vars[i].Name < Vars[j].Name
	}
	if !sort.SliceIsSorted(Vars, less) {
		for i := 1; i < len(Vars); i++ {
			if less(i, i-1) {
				t.Errorf("%s sorts before %s", Vars[i].Name, Vars[i-1].Name)
			}
		}
	}
}
