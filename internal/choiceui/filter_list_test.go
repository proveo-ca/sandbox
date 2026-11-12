// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func runList() *FilterList {
	return &FilterList{Prompt: "attach> ", Items: []string{
		"attach  proveo-claudecode-1b3fcb7f  monorepo · running 2h",
		"attach  proveo-hermes-f8781ef6      muse · stopped",
		"run     claudecode                  a new sandbox",
		"run     hermes                      a new sandbox",
	}}
}

func listAt(l *FilterList, w, h int) tea.Model {
	m, _ := listModel{list: l}.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func listLines(l *FilterList, w, h int) []string {
	s := newScreen(w, h)
	l.draw(s)
	return screenLines(s)
}

func TestTheFilterListsKeys(t *testing.T) {
	t.Parallel()
	down, tab := teaKey(tea.KeyDown, 0), teaKey(tea.KeyTab, 0)
	for _, c := range []struct {
		name string
		keys []tea.KeyPressMsg
		want []int
		out  Outcome
		ends bool
	}{
		{"enter takes the first row", []tea.KeyPressMsg{teaEnter}, []int{0}, Accepted, true},
		{"down then enter", []tea.KeyPressMsg{down, down, teaEnter}, []int{2}, Accepted, true},
		{"the cursor stops at the last row", []tea.KeyPressMsg{down, down, down, down, down, teaEnter}, []int{3}, Accepted, true},
		{"typing narrows, and enter takes the first match", append(typed("hermes"), teaEnter), []int{1}, Accepted, true},
		{"every typed word must be in the row", append(typed("run herm"), teaEnter), []int{3}, Accepted, true},
		{"case does not matter", append(typed("MUSE"), teaEnter), []int{1}, Accepted, true},
		{"enter with nothing matching does nothing", append(typed("zzz"), teaEnter), nil, Cancelled, false},
		{"backspace widens again", append(append(typed("zz"), teaKey(tea.KeyBackspace, 0), teaKey(tea.KeyBackspace, 0)), teaEnter), []int{0}, Accepted, true},
		{"esc cancels", []tea.KeyPressMsg{down, teaEsc}, nil, Cancelled, true},
		{"ctrl+c cancels", []tea.KeyPressMsg{teaCtrlC}, nil, Cancelled, true},
		{"tab does nothing in a list of one answer", []tea.KeyPressMsg{tab, teaEnter}, []int{0}, Accepted, true},
	} {
		l := runList()
		m, cmd := pressKeys(listAt(l, 100, 20), c.keys...)
		got := m.(listModel).out
		var picked []int
		if quits(cmd) && got == Accepted {
			picked = l.picked()
		}
		if quits(cmd) != c.ends || (c.ends && got != c.out) || !slices.Equal(picked, c.want) {
			t.Errorf("%s: ended %v, outcome %v, picked %v; want %v, %v, %v", c.name, quits(cmd), got, picked, c.ends, c.out, c.want)
		}
	}
}

func TestTheFilterListShowsItsPromptItsCursorAndItsKeys(t *testing.T) {
	t.Parallel()
	l := runList()
	l.key(press{text: "new"}, 20)
	joined := strings.Join(listLines(l, 100, 20), "\n")
	for _, want := range []string{"attach> new_", "› run     claudecode", "  run     hermes",
		"↑↓ row · type to narrow · enter choose · esc cancel"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the list lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "proveo-hermes") || strings.Contains(joined, "tab mark") {
		t.Errorf("the list shows a row the text excludes, or a key it does not take:\n%s", joined)
	}
	l.key(press{text: "zzz"}, 20)
	if joined = strings.Join(listLines(l, 100, 20), "\n"); !strings.Contains(joined, "nothing matches") {
		t.Errorf("an empty list must say so:\n%s", joined)
	}
}

func TestAShortTerminalScrollsTheListAndATinyOneOnlyCancels(t *testing.T) {
	t.Parallel()
	l := runList()
	l.Title = "attach to a run"
	for _, k := range []string{"down", "down", "down"} {
		l.key(press{key: k}, 7)
	}
	joined := strings.Join(listLines(l, 100, 7), "\n")
	if !strings.Contains(joined, "› run     hermes") || !strings.Contains(joined, "enter choose") {
		t.Errorf("the cursor's row or the key line left a short terminal:\n%s", joined)
	}
	if strings.Contains(joined, "1b3fcb7f") {
		t.Errorf("a window of two rows still shows the first of four:\n%s", joined)
	}
	tiny := runList()
	joined = strings.Join(listLines(tiny, 100, 3), "\n")
	if !strings.Contains(joined, "terminal too small") || !strings.Contains(joined, "esc cancels") {
		t.Errorf("a terminal under the floor must say so:\n%s", joined)
	}
	if _, done := tiny.key(press{key: "enter"}, 3); done {
		t.Error("enter chose a row from a list nobody can see")
	}
	if out, done := tiny.key(press{key: "esc"}, 3); !done || out != Cancelled {
		t.Error("esc must still leave a list too small to draw")
	}
}

// TestTheFilterListMatchesItsGoldenFrames holds the list to recorded frames.
// PROVEO_UPDATE_FRAMES=1 records them.
func TestTheFilterListMatchesItsGoldenFrames(t *testing.T) {
	dir := filepath.Join("testdata", "list")
	update := os.Getenv("PROVEO_UPDATE_FRAMES") != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		list func() *FilterList
	}{
		{"open", runList},
		{"narrowed", func() *FilterList { l := runList(); l.key(press{text: "hermes"}, 24); return l }},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		for _, size := range [][2]int{{120, 24}, {60, 8}, {30, 5}} {
			name := fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1])
			seen[name] = true
			got := frameText(listLines(c.list(), size[0], size[1]))
			path := filepath.Join(dir, name)
			if update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: no golden frame (%v); record it with PROVEO_UPDATE_FRAMES=1", name, err)
				continue
			}
			if got != string(want) {
				t.Errorf("%s differs from its golden frame:\n--- got ---\n%s--- want ---\n%s", name, got, want)
			}
		}
	}
	if !update {
		files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
		for _, f := range files {
			if !seen[filepath.Base(f)] {
				t.Errorf("%s is a golden frame no case draws; delete it", f)
			}
		}
	}
}
