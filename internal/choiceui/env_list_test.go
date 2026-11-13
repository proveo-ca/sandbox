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
		help[n] = "set in ~/monorepo/.env; the value is never shown"
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

func TestTheEnvHelpNamesTheEntryOnceAndTheFile(t *testing.T) {
	t.Parallel()
	f := envForm()
	f.Rows[1].Help = map[string]string{"ARCHITECT_MODEL": "a row help that must not answer for the list"}
	f.Rows[0].Help = map[string]string{"ARCHITECT_MODEL": "a cell help that must not answer for the list"}
	lines := f.Rows[0].helpLines(116)
	if len(lines) != 1 || lines[0].text != "› ARCHITECT_MODEL — set in ~/monorepo/.env; the value is never shown" {
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
	values := []string{"xq-value-7731", "xq-value-5508", "xq-value-2264", "xq-value-9140"}
	body := fmt.Sprintf("ANTHROPIC_API_KEY=%s\nexport DATABASE_URL=\"%s\"\n# NOTE=%s\nsmall_model='%s'\n", values[0], values[1], values[2], values[3])
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	names := credentials.EnvFileNames(path)
	if !slices.Equal(names, []string{"ANTHROPIC_API_KEY", "DATABASE_URL", "small_model"}) {
		t.Fatalf("names = %q", names)
	}
	m := envList(names)
	f := envFormWith(m)
	row := fmt.Sprintf("%#v %#v", f.Rows[0], *m)
	frames := []string{row}
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
