// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The pen advances by DISPLAY width, so what follows a wide glyph lands where
// the eye expects it rather than one column into it.
func TestPenAdvancesByDisplayWidth(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 40, 3)
	p := newPen(s, 0, 0)
	p.write(lipgloss.NewStyle(), "ab")
	if p.col() != 2 {
		t.Errorf("two narrow runes must advance 2, got %d", p.col())
	}
	// A literal, not runewidth.StringWidth: comparing the pen against the very
	// function it calls would pass for whatever that function returned.
	p.write(lipgloss.NewStyle(), "🔑")
	if p.col() != 4 {
		t.Errorf("a two-column rune after two narrow ones must land at 4, got %d", p.col())
	}
	p.write(lipgloss.NewStyle(), "z")
	if got := screenLines(s)[0]; got != "ab🔑z" {
		t.Errorf("the drawn row is %q; the terminal and the pen disagree about a wide rune", got)
	}
}

func TestPenGivesAZeroWidthRuneNoColumn(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 40, 3)
	p := newPen(s, 0, 0)
	p.write(lipgloss.NewStyle(), "A️")
	if p.col() != 1 {
		t.Errorf("a zero-width rune must take no column, got col %d", p.col())
	}
	if len(s.runs) != 1 || !strings.HasSuffix(s.runs[0].text, "️") {
		t.Errorf("the variation selector must stay with the rune before it, got %+v", s.runs)
	}
}

// A leading zero-width rune has nothing to attach to; it must be dropped rather
// than written into the cell to the left of the pen's own origin.
func TestPenDropsALeadingZeroWidthRune(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 40, 3)
	p := newPen(s, 0, 0)
	p.write(lipgloss.NewStyle(), "️")
	if p.col() != 0 || len(s.runs) != 0 {
		t.Errorf("nothing to combine with: the pen must not move or draw, got col %d and %d runs", p.col(), len(s.runs))
	}
}

// padTo is a floor, never a rewind: a run already past the column stays put.
func TestPenPadToNeverRewinds(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 40, 3)
	p := newPen(s, 0, 0).write(lipgloss.NewStyle(), "0123456789")
	p.padTo(4)
	if p.col() != 10 {
		t.Errorf("padTo must not move the pen backwards, got %d", p.col())
	}
}

// padTo covers what an earlier run left on the row, as the tcell pen's blanks did.
func TestPenPadToCoversAnEarlierRun(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 20, 1)
	newPen(s, 0, 0).write(lipgloss.NewStyle(), "underneath-text")
	newPen(s, 0, 0).write(lipgloss.NewStyle(), "top").padTo(8).write(lipgloss.NewStyle(), "|")
	if got := screenLines(s)[0]; got != "top     |h-text" {
		t.Errorf("row = %q, want the pad to blank columns 3 to 7", got)
	}
}
