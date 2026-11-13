// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/credentials"
)

// envNames is a project's .env names, A to Z.
var envNames = []string{"ARCHITECT_MODEL", "CODE_THEME", "DARK_MODE", "DATABASE_URL", "EDITOR_MODEL", "GITHUB_TOKEN",
	"MONGO_DSN", "NODE_ENV", "OPENAI_API_KEY", "REDIS_URL", "SMALL_MODEL", "STRIPE_KEY"}

// envList is the .env cell's list over names, as the run form builds it.
func envList(names []string) *More {
	help := map[string]string{}
	for _, n := range names {
		help[n] = "set in ~/monorepo/.env; this list shows names only"
	}
	return &More{Cell: 3, Options: slices.Clone(names), ReadOnly: true, Filter: true, Bare: true, Help: help}
}

// envForm is the run form with the cursor on the credentials row's .env cell and its list open.
func envForm() *Form {
	return envFormWith(envList(envNames))
}

func envFormWith(env *More) *Form {
	lists := map[int]*More{0: {Cell: 0, Options: []string{"anthropic"}, On: []bool{true}, ReadOnly: true}}
	on := []bool{true, false, false, false}
	if env != nil {
		lists[3], on[3] = env, true
	}
	return &Form{
		Title:  "run claudecode — confirm or change this run",
		Step:   Step{At: 2, Of: 2},
		Header: []string{"git: monorepo on feat-look", "tooling: go  node  python"},
		OnEnter: func(*Form) []string {
			return []string{"create proveo-claudecode-1b3f", "clone monorepo", "start the agent"}
		},
		Rows: []Row{
			{Label: "credentials", Divider: true, Heading: "agent", Multi: true, Locked: true, Hover: 3,
				Options: []string{"API keys", "OAuth", "SSH", ".env"}, Cols: []int{0, 1, 2, 3},
				On: on, Off: []bool{true, true, true, true}, Lists: lists,
				Reason: "host credential inventory; read-only"},
			{Label: "model", Options: []string{"Qwen 3.8", "API keys"}, Selected: 1, Cols: []int{0, 1}},
			{Label: "OS", Divider: true, Heading: "execution", Multi: true, Radio: true,
				Options: []string{"host", "sandbox"}, On: []bool{false, true}, Cols: []int{0, 1}},
			{Label: "trust repo", Options: []string{"on", "off"}, Selected: 1, Cols: []int{0, 1}},
			{Label: "egress", Divider: true, Heading: "reach", Options: []string{"allow-all", "balanced", "deny-all"}, Cols: []int{0, 1, 2}},
			{Label: "mcp", Options: []string{"on", "off"}, Selected: 1, Cols: []int{0, 1}},
			{Label: "devices", Divider: true, Heading: "interface", Multi: true,
				Options: []string{"tui", "browser", "android", "ios"}, On: []bool{true, false, false, false}, Cols: []int{0, 1, 2, 3}},
			{Label: "outputs", Options: []string{"filtered", "verbose"}, Selected: 1, Cols: []int{0, 1}},
		},
	}
}

// filtering is envForm with `/` pressed and text typed on the .env cell.
func filtering(text string) *Form { return filterOn(envForm(), 0, text) }

// filterOn is f with `/` pressed and text typed with the cursor on row cursor.
func filterOn(f *Form, cursor int, text string) *Form {
	for _, p := range append([]press{key('/')}, pressesOf(text)...) {
		f.key(&cursor, p, 120, 36)
	}
	return f
}

// pressesOf is text as the form reads it, one key per rune.
func pressesOf(text string) []press {
	var out []press
	for _, r := range text {
		out = append(out, key(r))
	}
	return out
}

// envOpen is the .env cell's list on f.
func envOpen(f *Form) *More { return f.Rows[0].Lists[3] }

func shownNames(m *More) []string {
	var out []string
	for _, i := range m.shown() {
		out = append(out, m.Options[i])
	}
	return out
}

func TestSubsequenceMatchesLettersInOrderInAnyCase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		q, s string
		want []int
	}{
		{"", "ANY", []int{}},
		{"mod", "ARCHITECT_MODEL", []int{10, 11, 12}},
		{"mod", "DARK_MODE", []int{5, 6, 7}},
		{"mdl", "SMALL_MODEL", []int{1, 8, 10}},
		{"MoD", "mongo_dsn", []int{0, 1, 6}},
		{"dom", "MODEL", nil},
		{"zz", "Z", nil},
		{"é", "CAFÉ", []int{3}},
	} {
		if got := subsequence(tc.q, tc.s); !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
			t.Errorf("subsequence(%q, %q) = %v, want %v", tc.q, tc.s, got, tc.want)
		}
	}
}

func TestTypedLettersNarrowTheEnvListInItsOrder(t *testing.T) {
	t.Parallel()
	f := filtering("mod")
	want := []string{"ARCHITECT_MODEL", "DARK_MODE", "EDITOR_MODEL", "MONGO_DSN", "SMALL_MODEL"}
	if got := shownNames(envOpen(f)); !slices.Equal(got, want) {
		t.Fatalf("shown = %q, want %q", got, want)
	}
	cursor := 0
	f.key(&cursor, named("backspace"), 120, 36)
	if got := envOpen(f).query; got != "mo" {
		t.Errorf("backspace left %q, want \"mo\"", got)
	}
	f.key(&cursor, named("ctrl+u"), 120, 36)
	if m := envOpen(f); m.query != "" || !m.typing || len(m.shown()) != len(envNames) {
		t.Errorf("ctrl+u left query %q typing %v shown %d", m.query, m.typing, len(m.shown()))
	}
}

func TestEveryTextKeyIsTextWhileTyping(t *testing.T) {
	t.Parallel()
	f := filtering("qjkhl /")
	m := envOpen(f)
	if m.query != "qjkhl /" || !m.typing {
		t.Fatalf("query %q typing %v, want every key as text", m.query, m.typing)
	}
	if r := f.Rows[0]; r.Hover != 3 || r.activeMore() != m {
		t.Fatalf("a typed letter moved the cursor: hover %d", r.Hover)
	}
	cursor := 0
	if out, done := f.key(&cursor, key('q'), 120, 36); done || cursor != 0 {
		t.Fatalf("q while typing ended the form (%v) or moved the cursor to %d", out, cursor)
	}
}

func TestEnterClosesTheFilterAndNeverAccepts(t *testing.T) {
	t.Parallel()
	f := filtering("mod")
	cursor := 0
	if out, done := f.key(&cursor, named("enter"), 120, 36); done {
		t.Fatalf("enter while typing ended the form with %v", out)
	}
	m := envOpen(f)
	if m.typing || m.query != "mod" || len(m.shown()) != 5 {
		t.Fatalf("enter left typing %v query %q shown %d, want typing off and the matches kept", m.typing, m.query, len(m.shown()))
	}
	if out, done := f.key(&cursor, named("enter"), 120, 36); !done || out != Accepted {
		t.Fatalf("enter after the filter closed = %v %v, want Accepted", out, done)
	}
}

func TestEscClearsTheFilterBeforeItGoesBack(t *testing.T) {
	t.Parallel()
	f := filtering("mod")
	cursor := 0
	esc := func() (Outcome, bool) { return f.key(&cursor, named("esc"), 120, 36) }
	if _, done := esc(); done {
		t.Fatal("esc while typing left the form")
	}
	if m := envOpen(f); m.query != "" || m.typing {
		t.Fatalf("esc while typing left query %q typing %v", m.query, m.typing)
	}
	for _, p := range append([]press{key('/')}, append(pressesOf("mod"), named("enter"))...) {
		f.key(&cursor, p, 120, 36)
	}
	if _, done := esc(); done {
		t.Fatal("esc with a standing filter left the form")
	}
	if m := envOpen(f); m.query != "" || m.typing {
		t.Fatalf("esc with a standing filter left query %q typing %v", m.query, m.typing)
	}
	if out, done := esc(); !done || out != Back {
		t.Fatalf("esc with no filter = %v %v, want Back", out, done)
	}
}

func TestLeavingTheCellDropsTheFilter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		keys []press
	}{
		{"left", []press{named("left")}},
		{"right", []press{named("right")}},
		{"up past the first", []press{named("up")}},
		{"down past the last", slices.Repeat([]press{named("down")}, 5)},
	} {
		f := envForm()
		f.Rows = append([]Row{{Label: "above", Options: []string{"a", "b"}}}, f.Rows...)
		filterOn(f, 1, "mod")
		env := f.Rows[1].Lists[3]
		cursor := 1
		for _, p := range tc.keys {
			f.key(&cursor, p, 120, 36)
		}
		if env.query != "" || env.typing {
			t.Errorf("%s: the list kept query %q typing %v after the cursor left it (row %d, cell %d)", tc.name, env.query, env.typing, cursor, f.Rows[1].Hover)
		}
		if f.openList(cursor) == env {
			t.Errorf("%s: the cursor never left the .env cell", tc.name)
		}
	}
}

func TestUpAndDownWalkOnlyTheMatches(t *testing.T) {
	t.Parallel()
	f := filtering("mod")
	cursor := 0
	f.key(&cursor, named("enter"), 120, 36)
	var walked []string
	for range 4 {
		f.key(&cursor, named("down"), 120, 36)
		m := envOpen(f)
		walked = append(walked, m.Options[m.At])
	}
	if want := []string{"DARK_MODE", "EDITOR_MODEL", "MONGO_DSN", "SMALL_MODEL"}; !slices.Equal(walked, want) {
		t.Fatalf("down walked %q, want %q", walked, want)
	}
}

func TestTheKeyLineWhileTypingNeverOffersAccept(t *testing.T) {
	t.Parallel()
	f := filtering("mod")
	if got, want := f.hint(120, 0, 0), "↑↓ row · type to narrow · enter close · esc clear · ctrl+c quit"; got != want {
		t.Errorf("typing key line = %q, want %q", got, want)
	}
	for w := 12; w <= 160; w++ {
		if hint := f.hint(w, 7, 0); strings.Contains(hint, "accept") {
			t.Fatalf("width %d: the key line offers accept while typing: %q", w, hint)
		}
	}
	rest := envForm()
	hint := rest.hint(120, 0, 0)
	if !strings.Contains(hint, "/ filter") || strings.Contains(hint, "space toggle") || !strings.Contains(hint, "enter accept") {
		t.Errorf("on the .env cell the key line = %q, want `/ filter` in place of `space toggle` and `enter accept`", hint)
	}
	if hint := rest.hint(120, 0, 1); strings.Contains(hint, "/ filter") || !strings.Contains(hint, "space toggle") {
		t.Errorf("off the .env cell the key line = %q, want `space toggle`", hint)
	}
	standing := filtering("mod")
	cursor := 0
	standing.key(&cursor, named("enter"), 120, 36)
	if hint := standing.hint(120, 0, 0); !strings.Contains(hint, "enter accept") || !strings.Contains(hint, "esc clear") {
		t.Errorf("with a standing filter the key line = %q, want `enter accept` and `esc clear`", hint)
	}
}

func TestTheKeyLineWhileTypingAnEmptyQuerySaysEscClear(t *testing.T) {
	t.Parallel()
	want := "↑↓ row · type to narrow · enter close · esc clear · ctrl+c quit"
	for name, keys := range map[string][]press{
		"right after /":           {key('/')},
		"after ctrl+u":            {key('/'), key('m'), key('o'), named("ctrl+u")},
		"after backspace to none": {key('/'), key('m'), key('o'), named("backspace"), named("backspace")},
	} {
		f := envForm()
		cursor := 0
		for _, p := range keys {
			f.key(&cursor, p, 120, 36)
		}
		if m := envOpen(f); m.query != "" || !m.typing {
			t.Fatalf("%s: query %q typing %v, want an empty query while typing", name, m.query, m.typing)
		}
		if got := f.hint(120, 0, 0); got != want {
			t.Errorf("%s: key line = %q, want %q", name, got, want)
		}
		frame := strings.Join(renderAt(t, f, 0, 120, 36), "\n")
		if !strings.Contains(frame, want) || strings.Contains(frame, "esc back") {
			t.Errorf("%s: the drawn key line must read %q:\n%s", name, want, frame)
		}
	}
}

func TestTheEnvHelpNamesTheEntryOnceAndTheFile(t *testing.T) {
	t.Parallel()
	f := envForm()
	f.Rows[1].Help = map[string]string{"ARCHITECT_MODEL": "a row help that must not answer for the list"}
	f.Rows[0].Help = map[string]string{"ARCHITECT_MODEL": "a cell help that must not answer for the list"}
	lines := f.Rows[0].helpLines(116)
	if len(lines) != 1 || lines[0].text != "› ARCHITECT_MODEL — set in ~/monorepo/.env; this list shows names only" {
		t.Fatalf("help = %+v", lines)
	}
	none := filtering("zzq")
	if lines := none.Rows[0].helpLines(116); len(lines) != 0 {
		t.Errorf("with nothing shown the help names a hidden entry: %+v", lines)
	}
}

func TestTheEnvCellWithoutAFileOpensNothing(t *testing.T) {
	t.Parallel()
	f := envFormWith(nil)
	lines := renderAt(t, f, 0, 120, 36)
	row := ""
	for _, l := range lines {
		if strings.Contains(l, "› credentials") {
			row = l
		}
	}
	if !strings.Contains(row, "[ ] .env") || strings.Contains(row, ".env ▾") {
		t.Fatalf("the .env cell with no file = %q, want `[ ] .env` with no arrow", row)
	}
	if f.openList(0) != nil || strings.Contains(strings.Join(lines[1:], "\n"), "╭") {
		t.Fatalf("a .env cell with no file opened a list:\n%s", strings.Join(lines, "\n"))
	}
	cursor := 0
	f.key(&cursor, key('/'), 120, 36)
	if out, done := f.key(&cursor, named("esc"), 120, 36); !done || out != Back {
		t.Fatalf("esc on a .env cell with no file = %v %v, want Back", out, done)
	}
}

// listBox is the rows the open list's frame spans on a drawn frame, found by its title on the top border.
func listBox(lines []string, name string) (top, bottom int) {
	top, bottom = -1, -1
	for y, l := range lines {
		if y > 0 && strings.Contains(l, "╭── "+name+" ") {
			top = y
		}
		if top >= 0 && bottom < 0 && y > top && strings.Contains(l, "╰──") {
			bottom = y
		}
	}
	return top, bottom
}

func TestTheOpenListNeverCoversTheTitleItsRowOrTheFoot(t *testing.T) {
	t.Parallel()
	forms := map[string]func() *Form{
		"env":     envForm,
		"env-mod": func() *Form { return filtering("mod") },
		"lists":   listsForm,
		"long": func() *Form {
			f := envForm()
			f.Rows = append(f.Rows[:1], append(slices.Repeat([]Row{{Label: "pad", Options: []string{"a", "b"}}}, 12), f.Rows[1:]...)...)
			return f
		},
	}
	for name, form := range forms {
		for _, sz := range [][2]int{{160, 45}, {120, 36}, {120, 24}, {100, 30}, {80, 24}, {80, 16}, {80, 12}, {64, 14}, {38, 24}} {
			f := form()
			w, h := sz[0], sz[1]
			if !f.navigable(w, h) {
				continue
			}
			lines := renderAt(t, f, 0, w, h)
			frame := strings.Join(lines, "\n")
			title := clip(f.Title, max(1, edge(w)-inset(w)))
			if !strings.Contains(lines[0]+lines[1], strings.TrimSuffix(title, "…")) {
				t.Errorf("%s %dx%d: the title is covered:\n%s", name, w, h, frame)
			}
			cell := f.Rows[0].Options[f.Rows[0].cursorAt()]
			own, foot := -1, -1
			for y, l := range lines {
				if strings.Contains(l, "› credentials") {
					own = y
				}
				if framed(w) && strings.HasPrefix(l, "├") && strings.Trim(strings.TrimSuffix(l, "┤"), "├─") == "" {
					foot = y
				}
			}
			if own < 0 {
				t.Errorf("%s %dx%d: the list covers its own row's marker and label:\n%s", name, w, h, frame)
				continue
			}
			if framed(w) && (foot < 0 || !strings.HasPrefix(lines[foot+2], "╰") || strings.ContainsAny(lines[foot+1][len("│"):], "╭╮╰╯")) {
				t.Errorf("%s %dx%d: the list covers the foot:\n%s", name, w, h, frame)
			}
			top, bottom := listBox(lines, f.Rows[0].columnText(cell))
			if top < 0 || bottom < 0 {
				t.Errorf("%s %dx%d: no list drawn:\n%s", name, w, h, frame)
				continue
			}
			if top <= 1 || (own >= top && own <= bottom) {
				t.Errorf("%s %dx%d: the list (%d..%d) spans the head or its own row %d:\n%s", name, w, h, top, bottom, own, frame)
			}
			if framed(w) && top <= foot+2 && bottom >= foot {
				t.Errorf("%s %dx%d: the list (%d..%d) spans the foot at %d:\n%s", name, w, h, top, bottom, foot, frame)
			}
		}
	}
}

// listCounts is what the open list titled name shows on a drawn frame: the count on its top border
// of entries above its window, the entries in its window, and the count on its bottom border below it.
func listCounts(t *testing.T, lines []string, name string) (above int, entries []string, below int) {
	t.Helper()
	top, bottom := listBox(lines, name)
	if top < 0 || bottom < 0 {
		t.Fatalf("no list %q drawn:\n%s", name, strings.Join(lines, "\n"))
	}
	i := strings.Index(lines[top], "╭── "+name+" ")
	left := len([]rune(lines[top][:i]))
	right := left + len([]rune(strings.SplitN(lines[top][i:], "╮", 2)[0]))
	span := func(y int) string {
		r := []rune(lines[y])
		return string(r[min(left, len(r)):min(right+1, len(r))])
	}
	count := func(line, word string) int {
		f := strings.Fields(strings.ReplaceAll(line, "─", " "))
		for j := 1; j < len(f); j++ {
			if f[j] == word {
				var n int
				fmt.Sscan(f[j-1], &n)
				return n
			}
		}
		return 0
	}
	for y := top + 1; y < bottom; y++ {
		text := strings.Trim(span(y), "│ ")
		if f := strings.Fields(text); len(f) > 0 && f[0] != "/" {
			entries = append(entries, f[len(f)-1])
		}
	}
	return count(span(top), "above"), entries, count(span(bottom), "more")
}

func TestTheListCountsWhatItHidesAboveAndBelowApart(t *testing.T) {
	t.Parallel()
	reached := map[string]bool{}
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {80, 16}} {
		for down := range len(envNames) {
			f := envForm()
			cursor := 0
			for range down {
				f.key(&cursor, named("down"), sz[0], sz[1])
			}
			lines := renderAt(t, f, cursor, sz[0], sz[1])
			above, entries, below := listCounts(t, lines, ".env")
			if above+len(entries)+below != len(envNames) {
				t.Errorf("%dx%d down %d: %d above + %d shown + %d below != %d:\n%s", sz[0], sz[1], down, above, len(entries), below, len(envNames), strings.Join(lines, "\n"))
				continue
			}
			if !slices.Equal(entries, envNames[above:above+len(entries)]) || !slices.Contains(entries, envNames[down]) {
				t.Errorf("%dx%d down %d: window %q is not entries %d.. holding the cursor's %s", sz[0], sz[1], down, entries, above, envNames[down])
			}
			reached["above"] = reached["above"] || above > 0
			reached["below"] = reached["below"] || below > 0
			reached["both"] = reached["both"] || above > 0 && below > 0
		}
	}
	lines := renderAt(t, listsForm(), 0, 80, 10)
	if above, entries, below := listCounts(t, lines, "OAuth"); above != 3 || !slices.Equal(entries, []string{"credential-03"}) || below != 8 {
		t.Errorf("lists 80x10: %d above, %q, %d below; want 3, credential-03, 8:\n%s", above, entries, below, strings.Join(lines, "\n"))
	}
	if !reached["above"] || !reached["below"] || !reached["both"] {
		t.Errorf("the sweep must reach entries hidden above, below and both: %v", reached)
	}
}

// The open list draws at every width without a panic and never past the terminal's last column.
func TestTheOpenListDrawsAtEveryWidth(t *testing.T) {
	t.Parallel()
	for name, form := range map[string]func() *Form{"env": envForm, "env-mod": func() *Form { return filtering("mod") }, "lists": listsForm} {
		for w := 1; w <= 160; w++ {
			for _, h := range []int{12, 36} {
				for _, l := range renderAt(t, form(), 0, w, h) {
					if textWidth(l) > w {
						t.Fatalf("%s %dx%d: a row runs past the last column: %q", name, w, h, l)
					}
				}
			}
		}
	}
}

func TestTheOpenListKeepsItsHeightWhileTheFilterNarrows(t *testing.T) {
	t.Parallel()
	for _, sz := range [][2]int{{120, 36}, {80, 24}} {
		var spans [][2]int
		for _, q := range []string{"", "mod", "zzq"} {
			f := envForm()
			if q != "" {
				f = filtering(q)
			}
			top, bottom := listBox(renderAt(t, f, 0, sz[0], sz[1]), ".env")
			spans = append(spans, [2]int{top, bottom})
		}
		if spans[0][0] < 0 || spans[0] != spans[1] || spans[1] != spans[2] {
			t.Errorf("%dx%d: the list's rows moved as the filter narrowed: %v", sz[0], sz[1], spans)
		}
	}
}

func TestTheEnvListCountsAndDrawsItsFilterLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		form       *Form
		want, none []string
	}{
		{envForm(), []string{"── .env ──", "── 12 ─╮", "│ / filter", "│ › ARCHITECT_MODEL", "5 more"}, []string{"[x] ARCHITECT", "[ ] ARCHITECT", "nothing matches"}},
		{filtering("mod"), []string{"── 5 of 12 ─╮", "│ / mod_", "│ › ARCHITECT_MODEL", "│   SMALL_MODEL"}, []string{"CODE_THEME", "more ─"}},
		{filtering("zzq"), []string{"── 0 of 12 ─╮", "│ / zzq_", "│   nothing matches"}, []string{"ARCHITECT_MODEL"}},
	} {
		frame := strings.Join(renderAt(t, tc.form, 0, 120, 36), "\n")
		for _, s := range tc.want {
			if !strings.Contains(frame, s) {
				t.Errorf("frame lacks %q:\n%s", s, frame)
			}
		}
		for _, s := range tc.none {
			if strings.Contains(frame, s) {
				t.Errorf("frame holds %q:\n%s", s, frame)
			}
		}
	}
}

func TestTheEnvListFillsTheCursorGreyAndBoldsTheMatchedLetters(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 120, 36)
	filtering("mod").draw(s, 0, 0)
	p := styles()
	lines := screenLines(s)
	find := func(name string) (int, int) {
		for y, l := range lines {
			if i := strings.Index(l, "  "+name+" "); i >= 0 && !strings.Contains(l, "—") {
				return textWidth(l[:i]) + 2, y
			}
			if i := strings.Index(l, "› "+name+" "); i >= 0 && !strings.Contains(l, "—") {
				return textWidth(l[:i]) + 2, y
			}
		}
		t.Fatalf("%s is not on the list:\n%s", name, strings.Join(lines, "\n"))
		return 0, 0
	}
	x, y := find("ARCHITECT_MODEL")
	if _, _, st, _ := s.GetContent(x, y); !sameStyle(st, p.focusOff) {
		t.Errorf("the cursor's entry is not the grey fill: %q", st.Render("A"))
	}
	if _, _, st, _ := s.GetContent(x-2, y); !sameStyle(st, p.brand) {
		t.Errorf("the cursor's mark is not brand: %q", st.Render("›"))
	}
	x, y = find("DARK_MODE")
	for i, want := range map[int]bool{0: false, 4: false, 5: true, 6: true, 7: true, 8: false} {
		if _, _, st, _ := s.GetContent(x+i, y); st.GetBold() != want {
			t.Errorf("DARK_MODE letter %d bold = %v, want %v", i, st.GetBold(), want)
		}
	}
	if _, _, st, _ := s.GetContent(x, y); !sameStyle(st, p.fixed) {
		t.Errorf("a read-only entry is not plain: %q", st.Render("D"))
	}
}

func TestTheEnvListDrawsFromTheCellsColumnAndTheTiersRunes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tier GlyphTier
		w    int
	}{{GlyphsSafe, 120}, {GlyphsNerd, 120}, {GlyphsASCII, 120}, {GlyphsOff, 120}, {GlyphsSafe, 80}} {
		f := envForm()
		f.Glyphs = tc.tier
		g := glyphsFor(tc.tier)
		lines := renderAt(t, f, 0, tc.w, 36)
		cellX := optOrigin + 3*pitch(cardWidth(tc.w), f.lastCol())
		top := g.frameTL + g.rule + g.rule + " .env "
		entry, corner, right := -1, -1, -1
		for _, l := range lines[1:] {
			if i := strings.Index(l, "› ARCHITECT_MODEL"); i >= 0 && !strings.Contains(l, "—") {
				entry = textWidth(l[:i]) + 2
			}
			if i := strings.Index(l, top); i >= 0 {
				corner = textWidth(l[:i])
				right = corner + 1 + textWidth(strings.SplitN(l[i+len(g.frameTL):], g.frameTR, 2)[0])
			}
		}
		if entry < 0 || corner < 0 {
			t.Fatalf("tier %d at %d: no list with %q:\n%s", tc.tier, tc.w, top, strings.Join(lines, "\n"))
		}
		if fits := cellX-4+listMin-1 <= cardWidth(tc.w)-2; fits && entry != cellX {
			t.Errorf("tier %d at %d: entries start at %d, want the cell's column %d", tc.tier, tc.w, entry, cellX)
		}
		if right > cardWidth(tc.w)-2 {
			t.Errorf("tier %d at %d: the list's right corner at %d is past the card's inner edge %d", tc.tier, tc.w, right, cardWidth(tc.w)-2)
		}
		if entry != corner+4 {
			t.Errorf("tier %d at %d: entries at %d, corner at %d", tc.tier, tc.w, entry, corner)
		}
	}
}

func TestNoEnvValueReachesAFrame(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	values := []string{"xq-value-7731", "xq-value-5508", "xq-value-2264", "xq-value-9140",
		"XQPEMBODY4417", "XQ_PEM_LINE", "xq-pem-3391", "BEGIN PRIVATE", "XQJSONKEY8812", "XQ_JSON_NAME", "xq-json-6650",
		"private_key", "xq-open-1180", "XQ_OPEN_LINE", "xq-open-2291"}
	body := fmt.Sprintf("ANTHROPIC_API_KEY=%s\nexport DATABASE_URL=\"%s\"\n# NOTE=%s\nsmall_model='%s'\n", values[0], values[1], values[2], values[3]) +
		"export TLS_KEY=\"-----BEGIN PRIVATE KEY-----\nXQPEMBODY4417==\nXQ_PEM_LINE=xq-pem-3391\n-----END PRIVATE KEY-----\"\n" +
		"export SA_JSON='{\n  \"private_key\": \"XQJSONKEY8812==\",\n  \"XQ_JSON_NAME\": \"xq-json-6650\"\n}'\n" +
		"OPEN=\"xq-open-1180\nXQ_OPEN_LINE=xq-open-2291\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	names := credentials.EnvFileNames(path)
	if !slices.Equal(names, []string{"ANTHROPIC_API_KEY", "DATABASE_URL", "SA_JSON", "small_model", "TLS_KEY"}) {
		t.Fatalf("names = %q", names)
	}
	m := envList(names)
	f := envFormWith(m)
	row := fmt.Sprintf("%#v %#v", f.Rows[0], *m)
	frames := []string{row, strings.Join(names, "\n")}
	for _, sz := range [][2]int{{160, 45}, {120, 36}, {80, 24}, {80, 12}, {38, 20}} {
		for _, keys := range [][]press{nil, {key('/')}, {key('/'), key('a')}, {key('/'), key('z'), key('q')}, {named("down"), named("down")}} {
			f := envFormWith(envList(names))
			cursor := 0
			for _, p := range keys {
				f.key(&cursor, p, sz[0], sz[1])
			}
			frames = append(frames, strings.Join(renderAt(t, f, cursor, sz[0], sz[1]), "\n"), f.frame(sz[0], sz[1], cursor, 0))
		}
	}
	for _, frame := range frames {
		for _, v := range values {
			if strings.Contains(frame, v) {
				t.Fatalf("the .env value %q reached a frame:\n%s", v, frame)
			}
		}
	}
}
