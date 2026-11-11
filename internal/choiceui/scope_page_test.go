// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func scopePage() *ScopePage {
	return &ScopePage{
		Banner: Banner(),
		Title:  "run claudecode — where the agent works",
		Root:   "monorepo",
		Tree:   monoTree(),
		Step:   Step{At: 1, Of: 2},
	}
}

// scopeAt is the page's model on a terminal of the given size.
func scopeAt(sp *ScopePage, w, h int) tea.Model {
	m, _ := scopeModel{page: sp}.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// scopeLines draws the page and returns the screen's rows.
func scopeLines(sp *ScopePage, w, h int) []string {
	s := newScreen(w, h)
	sp.draw(s)
	return screenLines(s)
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestEnterAloneKeepsTheRepoRoot(t *testing.T) {
	t.Parallel()
	sp := scopePage()
	m, cmd := pressKeys(scopeAt(sp, 100, 30), teaEnter)
	if !quits(cmd) || m.(scopeModel).out != Accepted || sp.Tree.At() != "" {
		t.Errorf("enter: ended %v, outcome %v, scope %q; want accepted at the root", quits(cmd), m.(scopeModel).out, sp.Tree.At())
	}
}

func TestTheScopePagesKeys(t *testing.T) {
	t.Parallel()
	down, up := teaKey(tea.KeyDown, 0), teaKey(tea.KeyUp, 0)
	for _, c := range []struct {
		name  string
		keys  []tea.KeyPressMsg
		scope string
		out   Outcome
		ends  bool
	}{
		{"down then enter picks the first folder", []tea.KeyPressMsg{down, teaEnter}, "apps", Accepted, true},
		{"two down picks a project", []tea.KeyPressMsg{down, down, teaEnter}, "apps/api", Accepted, true},
		{"up at the root stays on it", []tea.KeyPressMsg{up, teaEnter}, "", Accepted, true},
		{"esc cancels", []tea.KeyPressMsg{down, teaEsc}, "apps", Cancelled, true},
		{"ctrl+c asks first", []tea.KeyPressMsg{teaCtrlC}, "", Cancelled, false},
		{"ctrl+c twice quits", []tea.KeyPressMsg{teaCtrlC, teaCtrlC}, "", Quit, true},
		{"ctrl+c then enter stays, and the page still takes keys", []tea.KeyPressMsg{teaCtrlC, teaEnter, down, teaEnter}, "apps", Accepted, true},
		{"left folds, and the next row is the next folder", []tea.KeyPressMsg{down, teaLeft, down, teaEnter}, "packages", Accepted, true},
		{"right reopens a folded folder", []tea.KeyPressMsg{down, teaLeft, teaRight, down, teaEnter}, "apps/api", Accepted, true},
		{"a filter narrows and enter picks the match", append(append(typed("/"), typed("worker")...), teaEnter), "services/billing/worker", Accepted, true},
		{"esc clears a filter and does not cancel", append(append(typed("/"), typed("web")...), teaEsc), "apps/web", Cancelled, false},
		{"backspace on an empty filter closes it, and letters stop narrowing", append(append(typed("/"), teaKey(tea.KeyBackspace, 0)), typed("web")...), "", Cancelled, false},
		{"a letter outside a filter does nothing", typed("q"), "", Cancelled, false},
		{"end goes to the last row", []tea.KeyPressMsg{teaKey(tea.KeyEnd, 0), teaEnter}, "tools", Accepted, true},
	} {
		sp := scopePage()
		m, cmd := pressKeys(scopeAt(sp, 100, 30), c.keys...)
		got := m.(scopeModel).out
		if quits(cmd) != c.ends || (c.ends && got != c.out) || sp.Tree.At() != c.scope {
			t.Errorf("%s: ended %v, outcome %v, scope %q; want %v, %v, %q",
				c.name, quits(cmd), got, sp.Tree.At(), c.ends, c.out, c.scope)
		}
	}
}

func TestTheRootRowIsMarkedDefaultAndTheLineUnderTheTreeFollowsTheCursor(t *testing.T) {
	t.Parallel()
	sp := scopePage()
	joined := strings.Join(scopeLines(sp, 100, 30), "\n")
	for _, want := range []string{
		"› monorepo  repo root  default",
		"enter starts the agent at the repo root",
		"scope · 1/2",
		"page 1/2 · ↑↓ row · ←→ fold · / filter · enter choose · esc cancel · ctrl+c quit",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the page lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "enter accept") {
		t.Errorf("the scope page says \"enter accept\", which e2e reads as the form:\n%s", joined)
	}
	sp.Tree.Place("apps/web")
	joined = strings.Join(scopeLines(sp, 100, 30), "\n")
	if !strings.Contains(joined, "enter starts the agent in apps/web") || !strings.Contains(joined, "›     web") {
		t.Errorf("the cursor's folder is not named under the tree:\n%s", joined)
	}
}

func TestAShortTerminalScrollsTheTreeAndKeepsTheCursorAndTheFoot(t *testing.T) {
	t.Parallel()
	for h := 7; h <= 16; h++ {
		for _, at := range []string{"", "apps/web", "services/billing/worker", "tools"} {
			sp := scopePage()
			sp.Tree.Place(at)
			joined := strings.Join(scopeLines(sp, 80, h), "\n")
			name := at[strings.LastIndex(at, "/")+1:]
			if at == "" {
				name = "monorepo"
			}
			for _, want := range []string{"› ", name, "enter choose", "enter starts the agent"} {
				if !strings.Contains(joined, want) {
					t.Errorf("h=%d at=%q: the page lacks %q:\n%s", h, at, want, joined)
				}
			}
		}
	}
	sp := scopePage()
	if joined := strings.Join(scopeLines(sp, 80, 9), "\n"); !strings.Contains(joined, "more below") {
		t.Errorf("a tree taller than its window does not say rows are hidden:\n%s", joined)
	}
}

func TestBelowItsFloorTheScopePageSaysSoAndStillLeaves(t *testing.T) {
	t.Parallel()
	sp := scopePage()
	joined := strings.Join(scopeLines(sp, 80, 5), "\n")
	if !strings.Contains(joined, "terminal too small") || !strings.Contains(joined, "esc cancels") {
		t.Errorf("a terminal under the floor must say so:\n%s", joined)
	}
	m, cmd := pressKeys(scopeAt(sp, 80, 5), teaKey(tea.KeyDown, 0))
	if quits(cmd) || sp.Tree.At() != "" {
		t.Errorf("a key moved a cursor nobody can see: %q", sp.Tree.At())
	}
	if _, cmd = pressKeys(m, teaEsc); !quits(cmd) {
		t.Error("esc must still leave a page too small to draw")
	}
}

func TestTheScopePagePaintsTheCursorsRowInTheBrandColour(t *testing.T) {
	t.Parallel()
	sp := scopePage()
	sp.Tree.Place("apps")
	s := newScreen(100, 30)
	sp.draw(s)
	lines := screenLines(s)
	p := styles()
	for y, line := range lines {
		x := strings.Index(line, "apps")
		if x < 0 || !strings.HasPrefix(line, "› ") {
			continue
		}
		col := textWidth(line[:x])
		if _, _, st, _ := s.GetContent(col, y); !sameStyle(st, p.brand) {
			t.Errorf("the cursor's row is not in the brand style")
		}
		return
	}
	t.Fatalf("no cursor row on apps:\n%s", strings.Join(lines, "\n"))
}

// TestTheScopePageMatchesItsGoldenFrames holds the page to recorded frames.
// PROVEO_UPDATE_FRAMES=1 records them.
func TestTheScopePageMatchesItsGoldenFrames(t *testing.T) {
	dir := filepath.Join("testdata", "scope")
	update := os.Getenv("PROVEO_UPDATE_FRAMES") != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		page func() *ScopePage
	}{
		{"root", scopePage},
		{"project", func() *ScopePage { sp := scopePage(); sp.Tree.Place("apps/web"); return sp }},
		{"folded", func() *ScopePage { sp := scopePage(); sp.Tree.Place("services"); sp.Tree.Fold(); return sp }},
		{"filter", func() *ScopePage {
			sp := scopePage()
			sp.filtering = true
			sp.Tree.SetFilter("bill")
			return sp
		}},
		{"alone", func() *ScopePage { sp := scopePage(); sp.Step = Step{}; return sp }},
		{"ascii", func() *ScopePage {
			sp := scopePage()
			sp.Glyphs = GlyphsASCII
			sp.Tree.Place("apps")
			sp.Tree.Fold()
			return sp
		}},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		for _, size := range [][2]int{{160, 45}, {80, 24}, {60, 12}, {40, 8}} {
			name := fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1])
			seen[name] = true
			got := frameText(scopeLines(c.page(), size[0], size[1]))
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
