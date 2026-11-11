// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// formAt is a form's model on a terminal of the given size.
func formAt(f *Form, w, h int) tea.Model {
	m, _ := formModel{f: f, cursor: f.startCursor(), tick: 1}.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func teaKey(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }

func TestTheFormsKeysMeanWhatTheyMeantOnTcell(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		step Step
		keys []tea.KeyPressMsg
		out  Outcome
		ends bool
	}{
		{"enter accepts", Step{}, []tea.KeyPressMsg{teaEnter}, Accepted, true},
		{"esc cancels", Step{}, []tea.KeyPressMsg{teaEsc}, Cancelled, true},
		{"ctrl+c cancels", Step{}, []tea.KeyPressMsg{teaCtrlC}, Cancelled, true},
		{"q cancels", Step{}, typed("q"), Cancelled, true},
		{"shift+tab steps back past the first screen", Step{At: 2, Of: 3}, []tea.KeyPressMsg{teaKey(tea.KeyTab, tea.ModShift)}, Back, true},
		{"shift+tab on the first screen does nothing", Step{At: 1, Of: 3}, []tea.KeyPressMsg{teaKey(tea.KeyTab, tea.ModShift)}, Cancelled, false},
		{"moving does not end the form", Step{}, []tea.KeyPressMsg{teaKey(tea.KeyDown, 0), teaRight, teaLeft, teaKey(tea.KeyUp, 0)}, Cancelled, false},
	} {
		f := stepForm(c.step.At, c.step.Of)
		m := formAt(f, 100, 30)
		var cmd tea.Cmd
		for _, k := range c.keys {
			m, cmd = m.Update(k)
		}
		ended := false
		if cmd != nil {
			_, ended = cmd().(tea.QuitMsg)
		}
		if got := m.(formModel).out; ended != c.ends || (c.ends && got != c.out) {
			t.Errorf("%s: outcome %v, ended %v; want %v, %v", c.name, got, ended, c.out, c.ends)
		}
	}
}

func TestArrowsMoveTheCursorAndChangeTheAnswer(t *testing.T) {
	t.Parallel()
	f := stepForm(1, 1)
	m := formAt(f, 100, 30)
	m, _ = m.Update(teaKey(tea.KeyDown, 0))
	if got := m.(formModel).cursor; got != 1 {
		t.Fatalf("down moved the cursor to %d, want row 1", got)
	}
	m, _ = m.Update(teaRight)
	if got := f.Selection("b"); got != "y" {
		t.Errorf("right on row b selected %q, want y", got)
	}
	m, _ = m.Update(typed("k")[0])
	m, _ = m.Update(typed("l")[0])
	if got := f.Selection("a"); got != "y" {
		t.Errorf("k then l selected %q on row a, want y", got)
	}
	if !strings.Contains(plain(m.View().Content), "› a") {
		t.Errorf("the cursor's row is not marked:\n%s", plain(m.View().Content))
	}
}

func TestTypingGoesToAFieldAndNotToTheForm(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	m := formAt(f, 100, 30)
	m, _ = m.Update(teaKey(tea.KeyDown, 0))
	var cmd tea.Cmd
	for _, k := range typed("jkq sk-1") {
		m, cmd = m.Update(k)
	}
	if cmd != nil {
		t.Fatal("a typed q ended the form from inside a field")
	}
	if got := f.FieldValue("openai"); got != "jkq sk-1" {
		t.Errorf("field = %q, want every typed character", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := f.FieldValue("openai"); got != "jkq sk-" {
		t.Errorf("after backspace: %q", got)
	}
	_, _ = m.Update(teaKey('u', tea.ModCtrl))
	if got := f.FieldValue("openai"); got != "" {
		t.Errorf("after ctrl+u: %q, want the field cleared", got)
	}
}

func TestTheFigureMovesOnTheClockAndTheFormDrawsOnTheAlternateScreen(t *testing.T) {
	t.Parallel()
	f := tallForm()
	m := formModel{f: f, cursor: f.startCursor(), tick: 1}
	if m.Init() == nil {
		t.Fatal("the form starts no clock for the figure's motion")
	}
	next, cmd := m.Update(formTick{})
	if next.(formModel).tick != 2 || cmd == nil {
		t.Errorf("a tick left the frame at %d (clock re-armed: %v)", next.(formModel).tick, cmd != nil)
	}
	sized := formAt(f, 150, 40)
	v := sized.View()
	if !v.AltScreen {
		t.Error("the form must draw on the alternate screen")
	}
	if got, want := frameText(strings.Split(plain(v.Content), "\n")), frameText(frameLines(t, tallForm(), f.startCursor(), 150, 40, 1)); got != want {
		t.Errorf("the model's view is not the form's frame:\n--- view ---\n%s--- frame ---\n%s", got, want)
	}
	if got := plain(formModel{f: f}.View().Content); strings.TrimSpace(got) != "" {
		t.Errorf("a form with no size yet drew %q", got)
	}
}

func TestATerminalTooSmallTakesOnlyTheKeysThatLeave(t *testing.T) {
	t.Parallel()
	f := tallForm()
	m := formAt(f, 80, 6)
	before := f.Selection("egress")
	m, cmd := m.Update(teaRight)
	if cmd != nil || f.Selection("egress") != before {
		t.Error("a form too small to draw changed an answer nobody could see")
	}
	if _, cmd = m.Update(teaEnter); cmd == nil {
		t.Error("enter must still accept the resolved values on a terminal too small to draw the form")
	}
}

func skipForm() *Form {
	f := stepForm(2, 3)
	f.Skip = "Skip"
	return f
}

func TestDownPastTheLastRowFocusesSkipAndEnterThereSkips(t *testing.T) {
	t.Parallel()
	down, up := teaKey(tea.KeyDown, 0), teaKey(tea.KeyUp, 0)
	for _, c := range []struct {
		name string
		keys []tea.KeyPressMsg
		out  Outcome
	}{
		{"enter on a row accepts", []tea.KeyPressMsg{down, teaEnter}, Accepted},
		{"down past the last row, then enter, skips", []tea.KeyPressMsg{down, down, teaEnter}, Skipped},
		{"up leaves the button", []tea.KeyPressMsg{down, down, up, teaEnter}, Accepted},
		{"arrows on the button change no answer", []tea.KeyPressMsg{down, down, teaRight, teaLeft, teaEnter}, Skipped},
		{"esc on the button still steps back", []tea.KeyPressMsg{down, down, teaEsc}, Back},
	} {
		f := skipForm()
		m, cmd := pressKeys(formAt(f, 100, 30), c.keys...)
		if !quits(cmd) || m.(formModel).out != c.out {
			t.Errorf("%s: ended %v, outcome %v; want %v", c.name, quits(cmd), m.(formModel).out, c.out)
		}
		if f.Rows[1].Selected != 0 {
			t.Errorf("%s: a key on the Skip button changed row b: %d", c.name, f.Rows[1].Selected)
		}
	}
	plain := stepForm(2, 3)
	m, cmd := pressKeys(formAt(plain, 100, 30), down, down, down, teaEnter)
	if !quits(cmd) || m.(formModel).out != Accepted {
		t.Error("a form with no Skip button must accept from its last row")
	}
}

func TestTheSkipButtonIsDrawnAndTheKeyLineSaysWhatEnterDoesOnIt(t *testing.T) {
	t.Parallel()
	f := skipForm()
	joined := strings.Join(renderAt(t, f, 0, 100, 30), "\n")
	if !strings.Contains(joined, "[ Skip ]") || !strings.Contains(joined, "enter accept") {
		t.Errorf("an unfocused Skip button, and \"enter accept\", must show:\n%s", joined)
	}
	cursor := 1
	f.key(&cursor, named("down"), 100, 30)
	joined = strings.Join(renderAt(t, f, cursor, 100, 30), "\n")
	for _, want := range []string{"› ", " Skip ", "enter skip"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the focused button lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "enter accept") || strings.Contains(joined, "› b") {
		t.Errorf("with the button focused no row carries the cursor, and enter does not accept:\n%s", joined)
	}
}

func TestCtrlCOnAWizardPageAsksAndASecondOneQuits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		keys []tea.KeyPressMsg
		out  Outcome
		ends bool
	}{
		{"ctrl+c alone asks", []tea.KeyPressMsg{teaCtrlC}, Cancelled, false},
		{"ctrl+c twice quits", []tea.KeyPressMsg{teaCtrlC, teaCtrlC}, Quit, true},
		{"ctrl+c then enter stays", []tea.KeyPressMsg{teaCtrlC, teaEnter}, Cancelled, false},
		{"ctrl+c then esc stays", []tea.KeyPressMsg{teaCtrlC, teaEsc}, Cancelled, false},
		{"ctrl+c, q, enter quits", append(append([]tea.KeyPressMsg{teaCtrlC}, typed("q")...), teaEnter), Quit, true},
		{"after staying, enter accepts the page", []tea.KeyPressMsg{teaCtrlC, teaEnter, teaEnter}, Accepted, true},
		{"q asks too", typed("q"), Cancelled, false},
	} {
		m, cmd := pressKeys(formAt(stepForm(2, 3), 100, 30), c.keys...)
		if got := m.(formModel).out; quits(cmd) != c.ends || (c.ends && got != c.out) {
			t.Errorf("%s: ended %v, outcome %v; want %v, %v", c.name, quits(cmd), got, c.ends, c.out)
		}
	}
	m, _ := pressKeys(formAt(stepForm(2, 3), 100, 30), teaCtrlC)
	view := plain(m.(formModel).View().Content)
	for _, want := range []string{"leave the wizard?", "Stay", "Quit", "Answers on this page are not saved."} {
		if !strings.Contains(view, want) {
			t.Errorf("the question lacks %q:\n%s", want, view)
		}
	}
	worded := stepForm(2, 3)
	worded.Step.Leave = Leave{Title: "Leave proveo run?", Body: "No sandbox has been created or resumed."}
	m, _ = pressKeys(formAt(worded, 100, 30), teaCtrlC)
	view = plain(m.(formModel).View().Content)
	if !strings.Contains(view, "Leave proveo run?") || !strings.Contains(view, "No sandbox has been created or resumed.") ||
		strings.Contains(view, "leave the wizard?") || strings.Contains(view, "Answers on this page") {
		t.Errorf("a wizard that words its question must get its own words, and only those:\n%s", view)
	}
	lone, cmd := pressKeys(formAt(stepForm(0, 0), 100, 30), teaCtrlC)
	if !quits(cmd) || lone.(formModel).out != Cancelled {
		t.Error("ctrl+c on a lone form must cancel at once, as before")
	}
	tiny, cmd := pressKeys(formAt(stepForm(2, 3), 100, 3), teaCtrlC)
	if !quits(cmd) || tiny.(formModel).out != Quit {
		t.Error("ctrl+c on a page too small to draw must quit without a question nobody can read")
	}
}

func enterForm(n int) *Form {
	f := stepForm(0, 0)
	f.OnEnter = func(*Form) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "action " + string(rune('a'+i))
		}
		return out
	}
	return f
}

func TestTheCountOfWhatEnterRunsFollowsEnterAcceptAndNeverReplacesIt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		n    int
		want string
	}{
		{3, "enter accept · 3 steps · esc cancel"},
		{1, "enter accept · 1 step · esc cancel"},
		{0, "enter accept · esc cancel"},
	} {
		if hint := enterForm(c.n).hint(200, 0); !strings.Contains(hint, c.want) {
			t.Errorf("%d actions: hint %q lacks %q", c.n, hint, c.want)
		}
	}
	f := enterForm(4)
	for w := len("enter accept"); w <= 160; w++ {
		if hint := f.hint(w, 7); !strings.Contains(hint, "enter accept") {
			t.Fatalf("width %d: %q lost \"enter accept\" to the count", w, hint)
		}
	}
	skip := enterForm(2)
	skip.Skip, skip.onSkip = "Skip", true
	if hint := skip.hint(200, 0); strings.Contains(hint, "steps") {
		t.Errorf("with Skip focused Enter runs nothing, yet the hint counts steps: %q", hint)
	}
}

func TestTheOnEnterRegionFillsOnlyTheRowsLeftAndNeverMovesTheKeyLine(t *testing.T) {
	t.Parallel()
	keyRow := func(lines []string) int {
		for y, l := range lines {
			if strings.Contains(l, "enter accept") {
				return y
			}
		}
		return -1
	}
	bare := keyRow(renderAt(t, stepForm(0, 0), 0, 100, 30))
	tall := renderAt(t, enterForm(3), 0, 100, 30)
	joined := strings.Join(tall, "\n")
	for _, want := range []string{"on enter", "1  action a", "2  action b", "3  action c"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the region lacks %q:\n%s", want, joined)
		}
	}
	if got := keyRow(tall); got != bare {
		t.Errorf("the region moved the key line from row %d to %d", bare, got)
	}
	h := bare + 5
	short := strings.Join(renderAt(t, enterForm(6), 0, 100, h), "\n")
	if !strings.Contains(short, "1  action a") || !strings.Contains(short, "… and ") || strings.Contains(short, "action f") {
		t.Errorf("a region with too few rows must end on a count of what it left out:\n%s", short)
	}
	none := renderAt(t, enterForm(3), 0, 100, bare+2)
	if joined := strings.Join(none, "\n"); strings.Contains(joined, "on enter") || !strings.Contains(joined, "3 steps") {
		t.Errorf("with no row to spare the region goes and the count stays:\n%s", joined)
	}
}
