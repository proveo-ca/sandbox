// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A dialog card's columns: the title and the loss mark, the body, the button row, the key line; and
// the columns right of its widest row, the wall included.
const (
	modalTitleX  = 2
	modalBodyX   = 4
	modalButtonX = 3
	modalKeyX    = 2
	modalRight   = 3
)

func hexOf(c int) color.Color { return lipgloss.Color(fmt.Sprintf("#%06X", c)) }

// modalWarns reports whether a body line states a consequence; a leading list number does not count.
func modalWarns(text string) bool {
	text = unnumbered(strings.TrimSpace(text))
	return strings.HasPrefix(text, "sbx rm --force") ||
		strings.Contains(text, "are lost") ||
		strings.Contains(text, "is lost") ||
		strings.Contains(text, "permanently lost") ||
		strings.Contains(text, "deletes the session") ||
		strings.Contains(text, "does not persist") ||
		strings.Contains(text, "lost unless fetched") ||
		strings.Contains(text, "not imported") ||
		strings.Contains(text, "does not survive")
}

// unnumbered is text without a leading list number such as "2. ".
func unnumbered(text string) string {
	digits := strings.TrimLeft(text, "0123456789")
	if len(digits) == len(text) {
		return text
	}
	if rest, ok := strings.CutPrefix(digits, ". "); ok {
		return strings.TrimLeft(rest, " ")
	}
	return text
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
