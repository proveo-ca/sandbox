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
