// SPEC: _spec/internal/choiceui/wizard-keys.puml, _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modelRowForm is claudecode's model row with no provider key: API keys is off and plan answers.
func modelRowForm() *Form {
	return &Form{Title: "run claudecode", Rows: []Row{
		{Label: "model", Options: []string{"Qwen 3.8", "API keys", "plan", "Glimmer"}, Selected: 2,
			Off:    []bool{false, true, false, false},
			OffWhy: map[string]string{"API keys": "no provider key is set — export one (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) or run `proveo init` to store it"},
			Reason: "API keys: no provider key is set — export one (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) or run `proveo init` to store it",
			Help: map[string]string{
				"Qwen 3.8": "27B, MLX, the host's Ollama serves it, no key",
				"API keys": "metered by the vendor beyond the plan's included usage",
				"plan":     "billed against the plan",
				"Glimmer":  "Muse Glimmer 30B, MLX, the host's Ollama serves it, no key",
			}},
		{Label: "outputs", Options: []string{"filtered", "verbose"}, Selected: 1},
	}}
}

// Every key that moves across a row lands on every option, a disabled one included, on a radio
// row, a checkbox row and a locked row alike.
func TestEveryMoveKeyLandsOnEveryOptionOfEveryRowKind(t *testing.T) {
	t.Parallel()
	rows := map[string]Row{
		"radio":    {Label: "r", Options: []string{"a", "b", "c", "d"}, Selected: 0, Off: []bool{false, true, true, false}},
		"checkbox": {Label: "r", Options: []string{"a", "b", "c"}, Multi: true, On: []bool{true, false, false}, Off: []bool{false, true, false}},
		"locked":   {Label: "r", Options: []string{"a", "b", "c"}, Locked: true, Off: []bool{false, true, false}, Reason: "fixed here"},
	}
	for kind, row := range rows {
		keys := []string{"right", "left", "l", "h"}
		if !row.Multi {
			keys = append(keys, " ")
		}
		for _, k := range keys {
			f := &Form{Rows: []Row{row}}
			var seen []string
			for range row.Options {
				seen = append(seen, f.Walk("r", k)[0].Option)
			}
			slices.Sort(seen)
			if !slices.Equal(seen, row.Options) {
				t.Errorf("%s row, %q: the cursor landed on %q, want every option once", kind, k, seen)
			}
		}
	}
}

// Landing on a disabled option of a radio row hovers it: the answer stays, Enter accepts that
// answer, and the next press past it chooses again.
func TestARadioRowHoversADisabledOptionAndEnterKeepsTheAnswer(t *testing.T) {
	t.Parallel()
	f := modelRowForm()
	m := formAt(f, 120, 40)
	m, _ = pressKeys(m, teaLeft)
	r := m.(formModel).f.Rows[0]
	if r.Options[r.cursorAt()] != "API keys" || f.Selection("model") != "plan" {
		t.Fatalf("← from plan: cursor on %q, answer %q; want API keys hovered, plan kept", r.Options[r.cursorAt()], f.Selection("model"))
	}
	m, cmd := pressKeys(m, teaEnter)
	if cmd == nil {
		t.Fatal("enter on a hovered disabled option did not end the form")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok || m.(formModel).out != Accepted || f.Selection("model") != "plan" {
		t.Errorf("enter: outcome %v, answer %q; want Accepted with plan", m.(formModel).out, f.Selection("model"))
	}

	g := modelRowForm()
	var path, answers []string
	for _, k := range []string{"left", "left", "right", "right", "right"} {
		path = append(path, g.Walk("model", k)[0].Option)
		answers = append(answers, g.Selection("model"))
	}
	if want := []string{"API keys", "Qwen 3.8", "API keys", "plan", "Glimmer"}; !slices.Equal(path, want) {
		t.Errorf("the cursor walked %q, want %q", path, want)
	}
	if want := []string{"plan", "Qwen 3.8", "Qwen 3.8", "plan", "Glimmer"}; !slices.Equal(answers, want) {
		t.Errorf("the answer read %q, want %q: a hover keeps the answer before it", answers, want)
	}
}

// The hovered disabled option keeps its answer bold and takes the grey cursor fill, struck through.
func TestAHoveredDisabledOptionTakesTheGreyFillAndTheAnswerStaysBold(t *testing.T) {
	t.Parallel()
	f := modelRowForm()
	f.Walk("model", "left")
	s := simScreen(t, 120, 40)
	f.draw(s, 0, 0)
	lines := screenLines(s)
	at := func(text string) lipgloss.Style {
		t.Helper()
		for y, l := range lines {
			if before, _, ok := strings.Cut(l, text); ok {
				_, _, st, _ := s.GetContent(textWidth(before), y)
				return st
			}
		}
		t.Fatalf("%q is not on the frame:\n%s", text, strings.Join(lines, "\n"))
		return lipgloss.Style{}
	}
	p := styles()
	if got := at("( ) API keys"); !sameStyle(got, p.focusOff.Strikethrough(true)) {
		t.Errorf("the hovered disabled option is drawn %q, want the grey fill struck through", got.Render("x"))
	}
	if got := at("(•) plan"); !sameStyle(got, p.chosen) {
		t.Errorf("the answer under a hovered disabled option is drawn %q, want bold", got.Render("x"))
	}
}

// The help names the hovered disabled option and its status, once: "› <option> — <help>", then
// "off: <OffWhy>"; the row's Reason does not repeat it.
func TestTheHelpNamesAHoveredDisabledOptionAndItsStatus(t *testing.T) {
	t.Parallel()
	f := modelRowForm()
	hv := f.Walk("model", "left")[0]
	want := []string{"› API keys — metered by the vendor beyond the plan's included usage"}
	if !slices.Equal(hv.Help, want) {
		t.Errorf("help = %q, want %q", hv.Help, want)
	}
	status := strings.Join(hv.Status, " ")
	if !strings.HasPrefix(status, "off: no provider key is set") || strings.Count(status, "no provider key is set") != 1 {
		t.Errorf("status = %q, want one off: line with the OffWhy", hv.Status)
	}
	rest := strings.Join(f.Walk("model", "right")[0].Status, " ")
	if !strings.Contains(rest, "API keys: no provider key is set") {
		t.Errorf("back on plan the row's reason must show as before, got %q", rest)
	}
	if got := f.maxHelpLines(helpWidth(cardMax)); got < len(hv.Help)+len(hv.Status) {
		t.Errorf("the help slot holds %d lines; hovering API keys draws %d", got, len(hv.Help)+len(hv.Status))
	}
}

// A disabled option without an OffWhy still says why: its row's Reason, else "off: not supported
// on this run". A disabled option without help, a locked row's included, is still named.
func TestADisabledOptionWithoutAnOffWhyStillStatesItsStatus(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		row    Row
		help   []string
		status []string
	}{
		{"no OffWhy, no Reason, no help", Row{Label: "r", Options: []string{"a", "b"}, Off: []bool{false, true}},
			[]string{"› b"}, []string{"off: not supported on this run"}},
		{"no OffWhy, a Reason", Row{Label: "r", Options: []string{"a", "b"}, Off: []bool{false, true}, Reason: "b: needs a key",
			Help: map[string]string{"b": "the vendor's API"}},
			[]string{"› b — the vendor's API"}, []string{"b: needs a key"}},
		{"a checkbox, no OffWhy", Row{Label: "r", Options: []string{"a", "b"}, Multi: true, On: []bool{true, false}, Off: []bool{false, true}},
			[]string{"› b"}, []string{"off: not supported on this run"}},
		{"a checkbox in force, no OffWhy", Row{Label: "r", Options: []string{"a", "b"}, Multi: true, On: []bool{true, true}, Off: []bool{false, true}},
			nil, nil},
		{"a locked row, a Reason, no help", Row{Label: "r", Options: []string{"a", "b"}, Locked: true, Reason: "fixed here"},
			[]string{"› b"}, []string{"fixed here"}},
		{"a locked row, no Reason, no help", Row{Label: "r", Options: []string{"a", "b"}, Locked: true},
			[]string{"› b"}, []string{"off: not supported on this run"}},
		{"a locked row's answer, no Reason", Row{Label: "r", Options: []string{"a", "b"}, Locked: true, Selected: 1},
			[]string{"› b"}, nil},
	} {
		f := &Form{Rows: []Row{c.row}}
		hv := f.Walk("r", "right")[0]
		if hv.Option != "b" || !slices.Equal(hv.Help, c.help) || !slices.Equal(hv.Status, c.status) {
			t.Errorf("%s: on %q help %q status %q; want help %q status %q", c.name, hv.Option, hv.Help, hv.Status, c.help, c.status)
		}
	}
}

// The cursor still opens on the answer: a hover from an earlier visit is dropped, and a start
// option that names a disabled option hovers it without choosing it.
func TestTheCursorOpensOnTheAnswerAndNeverChoosesADisabledStart(t *testing.T) {
	t.Parallel()
	f := modelRowForm()
	f.Walk("model", "left")
	if row := f.startCursor(); f.Rows[row].Options[f.Rows[row].cursorAt()] != "plan" {
		t.Errorf("a revisited form opens on %q, want the answer, plan", f.Rows[row].Options[f.Rows[row].cursorAt()])
	}
	named := modelRowForm()
	named.StartRow, named.StartOption = "model", "API keys"
	row := named.startCursor()
	if r := named.Rows[row]; r.Options[r.cursorAt()] != "API keys" || named.Selection("model") != "plan" {
		t.Errorf("a disabled start option: cursor %q, answer %q; want API keys hovered, plan kept", r.Options[r.cursorAt()], named.Selection("model"))
	}
}

// A checklist's help holds two lines; a hovered disabled option keeps its status line in them, the
// help's first line cut with … before it.
func TestAChecklistKeepsAHoveredDisabledOptionsStatusInItsTwoLines(t *testing.T) {
	t.Parallel()
	f := modelRowForm()
	f.Checklist = true
	f.Rows[0].Help["API keys"] = strings.Repeat("a help that runs over three lines ", 10)
	hv := f.Walk("model", "left")[0]
	if len(hv.Help) != 1 || len(hv.Status) != 1 || !strings.HasSuffix(hv.Help[0], "…") || !strings.HasPrefix(hv.Status[0], "off: no provider key is set") {
		t.Errorf("help %q status %q; want the help's first line cut with …, then the off: line", hv.Help, hv.Status)
	}
	at := f.Walk("model", "right")[0]
	if len(at.Help)+len(at.Status) > helpMost {
		t.Errorf("back on plan the help draws %d lines, over %d", len(at.Help)+len(at.Status), helpMost)
	}
}

// A radio row whose every option but the answer is off still walks every option and never
// fires OnChange for a hover.
func TestHoveringADisabledOptionChangesNoAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	f := &Form{Rows: []Row{{Label: "r", Options: []string{"a", "b", "c"}, Selected: 1, Off: []bool{true, false, true}}}}
	f.OnChange = func(*Form) { calls++ }
	for _, hv := range f.Walk("r", "right", "right", "right") {
		if hv.Option != "b" && len(hv.Status) == 0 {
			t.Errorf("%q is off and shows no status", hv.Option)
		}
	}
	if calls != 1 || f.Selection("r") != "b" {
		t.Errorf("OnChange fired %d times, answer %q; want once, on landing back on b", calls, f.Selection("r"))
	}
}

// An OnChange that frees the hovered option puts the cursor back on the answer, and one that
// disables it again leaves the cursor there.
func TestAHoverFreedByOnChangeStaysOnTheAnswer(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "r", Options: []string{"a", "b", "c"}, Off: []bool{false, true, false}},
		{Label: "s", Options: []string{"x", "y", "z"}},
	}}
	f.OnChange = func(f *Form) { f.Rows[0].Off[1] = f.Rows[1].Selected != 1 }
	f.Walk("r", "right")
	r := &f.Rows[0]
	for _, want := range []string{"a", "a"} {
		f.Walk("s", "right")
		if got := r.Options[r.cursorAt()]; got != want || f.Selection("r") != "a" {
			t.Errorf("after s reads %q: r's cursor on %q, answer %q; want the cursor on %q, answer a", f.Selection("s"), got, f.Selection("r"), want)
		}
	}
}
