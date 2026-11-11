// SPEC: _spec/internal/choiceui/wizard-keys.puml
package choiceui

import (
	tea "charm.land/bubbletea/v2"
)

const quitTitle = "leave the wizard?"

const (
	quitStay = iota
	quitLeave
)

// quitGate asks before Ctrl+C leaves a wizard page. Its zero value asks nothing.
type quitGate struct {
	ask *dialog
}

// open puts the question up on a terminal of w by h.
func (g quitGate) open(w, h int) quitGate {
	d := newDialog(quitTitle, []string{
		quitTitle,
		"",
		"Answers on this page are not saved.",
		"What an earlier page did stays done.",
	}, quitStay, dialogButton{Label: "Stay", Key: 's'}, dialogButton{Label: "Quit", Key: 'q'})
	d.onCtrlC = quitLeave
	d.w, d.h = w, h
	return quitGate{ask: &d}
}

// up reports whether the question is on screen.
func (g quitGate) up() bool { return g.ask != nil }

// Update gives the open question one message; quit is true once the operator chose to leave.
func (g quitGate) Update(msg tea.Msg) (next quitGate, quit bool) {
	m, _ := g.ask.Update(msg)
	d, _ := m.(dialog)
	switch {
	case d.answer == quitLeave:
		return quitGate{}, true
	case d.answer >= 0:
		return quitGate{}, false
	}
	return quitGate{ask: &d}, false
}

// view is the question, drawn over the whole screen.
func (g quitGate) view() tea.View { return g.ask.View() }
