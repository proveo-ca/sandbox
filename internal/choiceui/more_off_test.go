// SPEC: _spec/internal/devports/dev-ports.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestAGreyedDropdownEntryRefusesSpaceAndSaysWhy(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "ports", Multi: true, Selected: 3,
		Options: []string{"3000 next", "5173 vite", "8080 web", "...More"},
		On:      []bool{false, false, false, false},
		More:    &More{Cell: 3, Options: []string{"4000 jekyll", "18083 api"}, On: []bool{false, false}, Off: []bool{false, true}, At: 1},
		OffWhy:  map[string]string{"18083 api": "no host port 118083…918083 fits under 65535"},
	}}}
	space := tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone)
	f.moreKey(0, space)
	if f.Rows[0].More.On[1] || f.Rows[0].On[3] {
		t.Errorf("space ticked a greyed entry: dropdown %v cell %v", f.Rows[0].More.On, f.Rows[0].On)
	}
	var help []string
	for _, l := range f.Rows[0].helpLines(200) {
		help = append(help, l.text)
	}
	if joined := strings.Join(help, "\n"); !strings.Contains(joined, "off: no host port 118083…918083 fits under 65535") {
		t.Errorf("help for the greyed entry lacks its OffWhy:\n%s", joined)
	}
	f.Rows[0].More.On[1] = true
	if got := f.Selections("ports"); len(got) != 0 {
		t.Errorf("selections = %v, want the greyed entry left out", got)
	}
}
