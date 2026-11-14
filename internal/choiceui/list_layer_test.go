// SPEC: _spec/internal/choiceui/wizard-keys.puml, _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// portsFold is a wizard page whose ports row folds two ports behind ...More.
func portsFold() *Form {
	return &Form{Title: "run claudecode — confirm or change this run", Step: Step{At: 2, Of: 2}, Rows: []Row{
		{Label: "outputs", Options: []string{"filtered", "verbose"}, Selected: 1, Cols: []int{0, 1}},
		{Label: "ports", Multi: true, Selected: 3, Cols: []int{0, 1, 2, 3},
			Options: []string{"3000 next", "5173 vite", "8080 web", "...More"},
			On:      []bool{false, false, false, true},
			More:    &More{Cell: 3, Options: []string{"4000 jekyll", "1313 hugo"}, On: []bool{true, false}}},
	}}
}

// listCase is one kind of list with the cursor on its cell: the keys that bring it to the state under
// test, what Esc gives once it is closed, and the key line while it is open at 120 columns.
type listCase struct {
	name   string
	form   func() *Form
	cursor int
	keys   []press
	back   Outcome
	line   string
}

func listCases() []listCase {
	creds := func() *Form {
		f := envForm()
		f.Rows[0].Hover = 0
		return f
	}
	standing := append(append([]press{key('/')}, pressesOf("mod")...), named("enter"))
	return []listCase{
		{"a credentials list", creds, 0, nil, Back, "↑↓ entry · enter close · esc close · ctrl+c quit"},
		{"a credentials list on a lone form", listsForm, 0, nil, Cancelled, "↑↓ entry · enter close · esc close"},
		{"the .env list", envForm, 0, nil, Back, "↑↓ entry · / filter · enter close · esc close · ctrl+c quit"},
		{"the .env list with a query", envForm, 0, standing, Back, "↑↓ entry · / filter · enter close · esc close · ctrl+c quit"},
		{"the ports fold", portsFold, 1, nil, Back, "↑↓ entry · space toggle · enter close · esc close · ctrl+c quit"},
	}
}

// open is the case's form with its keys pressed and the list under the cursor open and not typing.
func (c listCase) open(t *testing.T) (*Form, *More) {
	t.Helper()
	f := c.form()
	cursor := c.cursor
	for _, p := range c.keys {
		if out, done := f.key(&cursor, p, 120, 36); done {
			t.Fatalf("%s: %q ended the form with %v", c.name, p.key+p.text, out)
		}
	}
	m := f.openList(cursor)
	if cursor != c.cursor || m == nil || m.typing {
		t.Fatalf("%s: no list open and resting under the cursor (row %d)", c.name, cursor)
	}
	return f, m
}

// cellName is the open list's title: the name of the cell it hangs from, without its count.
func cellName(f *Form, row int, m *More) string {
	return f.Rows[row].listName(m.Cell)
}

func TestEnterClosesAnOpenListAndNeverAccepts(t *testing.T) {
	t.Parallel()
	for _, c := range listCases() {
		f, m := c.open(t)
		cursor := c.cursor
		if out, done := f.key(&cursor, named("enter"), 120, 36); done {
			t.Fatalf("%s: enter on the open list ended the form with %v", c.name, out)
		}
		if cursor != c.cursor || f.openList(cursor) != nil || f.listAt(cursor) != m || m.query != "" || m.typing {
			t.Fatalf("%s: enter left the list open %v, query %q, cursor %d; want it closed under the cursor with no query",
				c.name, f.openList(cursor) != nil, m.query, cursor)
		}
		if top, _ := listBox(renderAt(t, f, cursor, 120, 36), cellName(f, cursor, m)); top >= 0 {
			t.Errorf("%s: the closed list still draws at row %d", c.name, top)
		}
		if out, done := f.key(&cursor, named("enter"), 120, 36); !done || out != Accepted {
			t.Errorf("%s: a second enter = %v %v, want Accepted", c.name, out, done)
		}
	}
}

func TestEscClosesAnOpenListAndNeverGoesBack(t *testing.T) {
	t.Parallel()
	for _, c := range listCases() {
		f, m := c.open(t)
		cursor := c.cursor
		if out, done := f.key(&cursor, named("esc"), 120, 36); done {
			t.Fatalf("%s: esc on the open list ended the form with %v", c.name, out)
		}
		if cursor != c.cursor || f.openList(cursor) != nil || f.listAt(cursor) != m || m.query != "" {
			t.Fatalf("%s: esc left the list open %v, query %q, cursor %d; want it closed with its query dropped",
				c.name, f.openList(cursor) != nil, m.query, cursor)
		}
		if out, done := f.key(&cursor, named("esc"), 120, 36); !done || out != c.back {
			t.Errorf("%s: a second esc = %v %v, want %v", c.name, out, done, c.back)
		}
	}
}

func TestAClosedListOpensAgainWhenTheCursorLeavesItsCellAndReturns(t *testing.T) {
	t.Parallel()
	for _, c := range listCases() {
		away, back := named("down"), named("up")
		if c.cursor > 0 {
			away, back = back, away
		}
		for _, trip := range [][2]press{{away, back}, {named("left"), named("right")}} {
			f, m := c.open(t)
			cursor := c.cursor
			f.key(&cursor, named("esc"), 120, 36)
			on, cells := slices.Clone(m.On), slices.Clone(f.Rows[cursor].On)
			for _, p := range []press{key(' '), named("tab")} {
				f.key(&cursor, p, 120, 36)
				if f.openList(cursor) != nil || !slices.Equal(m.On, on) || !slices.Equal(f.Rows[cursor].On, cells) {
					t.Errorf("%s: %q on the closed list's cell opened it or changed a tick: %v %v", c.name, p.key+p.text, m.On, f.Rows[cursor].On)
				}
			}
			f.key(&cursor, trip[0], 120, 36)
			if f.listAt(cursor) == m {
				t.Fatalf("%s: %s never left the cell", c.name, trip[0].key)
			}
			f.key(&cursor, trip[1], 120, 36)
			if cursor != c.cursor || f.openList(cursor) != m || m.typing || m.query != "" {
				t.Errorf("%s: %s then %s left the list open %v (cursor %d); want it open again with no query",
					c.name, trip[0].key, trip[1].key, f.openList(cursor) == m, cursor)
			}
		}
	}
}

func TestSlashOpensAClosedFilterableListTyping(t *testing.T) {
	t.Parallel()
	for _, c := range listCases() {
		f, m := c.open(t)
		cursor := c.cursor
		f.key(&cursor, named("enter"), 120, 36)
		if out, done := f.key(&cursor, key('/'), 120, 36); done {
			t.Fatalf("%s: / on a closed list ended the form with %v", c.name, out)
		}
		switch open := f.openList(cursor); {
		case m.Filter && (open != m || !m.typing):
			t.Errorf("%s: / left the list open %v typing %v, want it open and typing", c.name, open == m, m.typing)
		case !m.Filter && open != nil:
			t.Errorf("%s: / opened a list without a filter", c.name)
		}
	}
}

func TestTheKeyLineNamesWhatEnterAndEscCloseOnAList(t *testing.T) {
	t.Parallel()
	for _, c := range listCases() {
		f, _ := c.open(t)
		if got := f.hint(120, 0, c.cursor); got != c.line {
			t.Errorf("%s: open, the key line = %q, want %q", c.name, got, c.line)
		}
		if frame := strings.Join(renderAt(t, f, c.cursor, 120, 36), "\n"); !strings.Contains(frame, c.line) {
			t.Errorf("%s: the drawn key line must read %q:\n%s", c.name, c.line, frame)
		}
		for w := 12; w <= 160; w++ {
			if hint := f.hint(w, 7, c.cursor); strings.Contains(hint, "accept") {
				t.Fatalf("%s: width %d: the key line offers accept while a list is open: %q", c.name, w, hint)
			}
		}
		if f.Step.wizard() {
			long, _ := c.open(t)
			long.Title = strings.Repeat("t", 110)
			if chip := long.headChip(120, c.cursor); chip != "" {
				t.Fatalf("%s: a 110-rune title still drew the chip %q", c.name, chip)
			}
			if got, want := long.hint(120, 0, c.cursor), "page 2/2 · "+c.line; got != want {
				t.Errorf("%s: with no chip the key line = %q, want %q", c.name, got, want)
			}
		}
		cursor := c.cursor
		f.key(&cursor, named("esc"), 120, 36)
		if hint := f.hint(120, 0, cursor); !strings.Contains(hint, "enter accept") || strings.Contains(hint, "close") {
			t.Errorf("%s: closed, the key line = %q, want the form's `enter accept` and no `close`", c.name, hint)
		}
	}
	typing := filtering("mod")
	if got, want := typing.hint(120, 0, 0), "↑↓ row · type to narrow · enter close · esc clear · ctrl+c quit"; got != want {
		t.Errorf("typing, the key line = %q, want %q", got, want)
	}
}

func TestEnterAndEscEachCloseOneLayerInnermostFirst(t *testing.T) {
	t.Parallel()
	up := teaKey(tea.KeyUp, 0)
	for _, c := range []struct {
		name  string
		key   tea.KeyPressMsg
		query string
		out   Outcome
	}{
		{"enter", teaEnter, "qjk ", Accepted},
		{"esc", teaEsc, "", Back},
	} {
		f := envForm()
		m, _ := pressKeys(formAt(f, 120, 36), up)
		env := envOpen(f)
		if m.(formModel).cursor != 0 || f.openList(0) != env {
			t.Fatalf("%s: up from model did not open the .env list", c.name)
		}
		m, cmd := pressKeys(m, append(typed("/"), typed("qjk ")...)...)
		if quits(cmd) || m.(formModel).cursor != 0 || env.query != "qjk " || !env.typing {
			t.Fatalf("%s: q j k and space while typing: query %q typing %v cursor %d, want every key as text",
				c.name, env.query, env.typing, m.(formModel).cursor)
		}
		m, cmd = pressKeys(m, c.key)
		if quits(cmd) || env.typing || env.query != c.query || f.openList(0) != env {
			t.Fatalf("%s 1: ended %v, typing %v, query %q, open %v; want the typing stopped and the list open",
				c.name, quits(cmd), env.typing, env.query, f.openList(0) == env)
		}
		m, cmd = pressKeys(m, c.key)
		if quits(cmd) || f.openList(0) != nil || env.query != "" {
			t.Fatalf("%s 2: ended %v, open %v, query %q; want the list closed and the form kept",
				c.name, quits(cmd), f.openList(0) != nil, env.query)
		}
		m, cmd = pressKeys(m, c.key)
		if !quits(cmd) || m.(formModel).out != c.out {
			t.Errorf("%s 3: ended %v with %v, want %v", c.name, quits(cmd), m.(formModel).out, c.out)
		}
	}
}

func TestCtrlCAsksFromEveryLayer(t *testing.T) {
	t.Parallel()
	up := teaKey(tea.KeyUp, 0)
	for name, keys := range map[string][]tea.KeyPressMsg{
		"the form":       nil,
		"an open list":   {up},
		"a typing query": append([]tea.KeyPressMsg{up}, typed("/mo")...),
	} {
		m, cmd := pressKeys(formAt(envForm(), 120, 36), append(keys, teaCtrlC)...)
		if quits(cmd) || !m.(formModel).quit.up() {
			t.Errorf("%s: ctrl+c ended %v, asked %v; want the question", name, quits(cmd), m.(formModel).quit.up())
		}
	}
}

func TestEnterAndEscReachTheQuitGateBeforeTheFormUnderIt(t *testing.T) {
	t.Parallel()
	up := teaKey(tea.KeyUp, 0)
	for _, under := range []struct {
		name string
		keys []tea.KeyPressMsg
	}{
		{"an open list", []tea.KeyPressMsg{up}},
		{"a typing query", append([]tea.KeyPressMsg{up}, typed("/mo")...)},
	} {
		for _, k := range []tea.KeyPressMsg{teaEsc, teaEnter} {
			f := envForm()
			m, _ := pressKeys(formAt(f, 120, 36), under.keys...)
			env := envOpen(f)
			query, typing := env.query, env.typing
			m, cmd := pressKeys(m, teaCtrlC, k)
			if quits(cmd) || m.(formModel).quit.up() {
				t.Fatalf("%s, %s: ended %v, still asking %v; want the question gone and the page kept", under.name, k, quits(cmd), m.(formModel).quit.up())
			}
			if f.openList(0) != env || env.query != query || env.typing != typing {
				t.Errorf("%s, %s: the key reached the page: open %v, query %q typing %v; want %q typing %v",
					under.name, k, f.openList(0) == env, env.query, env.typing, query, typing)
			}
		}
		m, cmd := pressKeys(formAt(envForm(), 120, 36), append(under.keys, append(append([]tea.KeyPressMsg{teaCtrlC}, typed("q")...), teaEnter)...)...)
		if !quits(cmd) || m.(formModel).out != Quit {
			t.Errorf("%s: ctrl+c, q, enter = ended %v with %v; want Quit", under.name, quits(cmd), m.(formModel).out)
		}
	}
}

func TestEnterAndEscReachTheQuitGateBeforeTheScopePage(t *testing.T) {
	t.Parallel()
	for _, k := range []tea.KeyPressMsg{teaEsc, teaEnter} {
		sp := scopePage()
		m, cmd := pressKeys(scopeAt(sp, 100, 30), append(append(typed("/"), typed("web")...), teaCtrlC, k)...)
		if quits(cmd) || m.(scopeModel).quit.up() || !sp.filtering || sp.Tree.Filter() != "web" {
			t.Errorf("%s: ended %v, asking %v, filtering %v %q; want the question gone and the page's filter kept",
				k, quits(cmd), m.(scopeModel).quit.up(), sp.filtering, sp.Tree.Filter())
		}
	}
	m, cmd := pressKeys(scopeAt(scopePage(), 100, 30), teaCtrlC, teaRight, teaEnter)
	if !quits(cmd) || m.(scopeModel).out != Quit {
		t.Errorf("ctrl+c, right, enter = ended %v with %v; want Quit", quits(cmd), m.(scopeModel).out)
	}
}

func TestEnterAnswersTheSelectedButtonAndEscTheSafeOneInEveryDialog(t *testing.T) {
	t.Parallel()
	for name, d := range theFiveDialogs() {
		for i := range d.buttons {
			moves := slices.Repeat([]tea.KeyPressMsg{teaRight}, i)
			if got, ended := answered(t, d, append(moves, teaEnter)...); got != i || (!ended && i != d.holdOn) {
				t.Errorf("%s: button %d selected, enter answered %d (ended %v)", name, i, got, ended)
			}
			if got, ended := answered(t, d, append(moves, teaEsc)...); !ended || got != d.safe {
				t.Errorf("%s: button %d selected, esc answered %d (ended %v); want the safe answer %d", name, i, got, ended, d.safe)
			}
		}
	}
}
