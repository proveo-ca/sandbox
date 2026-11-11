// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modalPad is the blank columns between a dialog's border and its text.
const modalPad = 2

func hexOf(c int) color.Color { return lipgloss.Color(fmt.Sprintf("#%06X", c)) }

// modalLineTea styles one body line: the title bold, a consequence in the warning colour.
func modalLineTea(p palette, text, title string) lipgloss.Style {
	switch {
	case text == title:
		return p.title
	case modalWarns(text):
		return p.warn
	}
	return p.body
}

// modalWarns reports whether a body line states a consequence.
func modalWarns(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "sbx rm --force") ||
		strings.Contains(text, "are lost") ||
		strings.Contains(text, "is lost") ||
		strings.Contains(text, "permanently lost") ||
		strings.Contains(text, "deletes the session") ||
		strings.Contains(text, "does not persist") ||
		strings.Contains(text, "lost unless fetched") ||
		strings.Contains(text, "not imported") ||
		strings.Contains(text, "does not survive")
}

// askDialog runs d on the operator's own terminal and returns the accepted button.
// asked is false when no terminal opens or the Program fails; the answer is then the safe one.
var askDialog = func(d dialog) (answer int, asked bool) {
	in, out, done, err := openTerminal()
	if err != nil {
		return d.safe, false
	}
	defer done()
	final, err := tea.NewProgram(d, tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
	got, ok := dialogAnswer(final)
	if err != nil || !ok {
		return d.safe, false
	}
	return got.answer, true
}
