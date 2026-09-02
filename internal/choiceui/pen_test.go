package choiceui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

// The pen advances by DISPLAY width, so what follows a wide glyph lands where
// the eye expects it rather than one column into it.
func TestPenAdvancesByDisplayWidth(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 3)

	p := newPen(s, 0, 0)
	p.write(tcell.StyleDefault, "ab")
	if p.col() != 2 {
		t.Errorf("two narrow runes must advance 2, got %d", p.col())
	}
	// A literal, not runewidth.StringWidth: comparing the pen against the very
	// function it calls would pass for whatever that function returned.
	p.write(tcell.StyleDefault, "🔑")
	if p.col() != 4 {
		t.Errorf("a two-column rune after two narrow ones must land at 4, got %d", p.col())
	}
}

// A zero-width rune joins the cell before it instead of taking a column. U+FE0F
// is the case that matters: it is what makes a variation-selected emoji two
// runes and one grapheme, and it is why emoji were kept out of the strip.
func TestPenAttachesZeroWidthRunes(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 3)

	p := newPen(s, 0, 0)
	p.write(tcell.StyleDefault, "A️")
	if p.col() != 1 {
		t.Errorf("a zero-width rune must take no column, got col %d", p.col())
	}
	_, combc, _, _ := s.GetContent(0, 0)
	if len(combc) != 1 || combc[0] != '️' {
		t.Errorf("the variation selector must be combined onto the cell before it, got %v", combc)
	}
}

// A leading zero-width rune has nothing to attach to; it must be dropped rather
// than written into the cell to the left of the pen's own origin.
func TestPenDropsALeadingZeroWidthRune(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 3)
	p := newPen(s, 0, 0)
	p.write(tcell.StyleDefault, "️")
	if p.col() != 0 {
		t.Errorf("nothing to combine with: the pen must not move, got %d", p.col())
	}
}

// padTo is a floor, never a rewind: a run already past the column stays put.
func TestPenPadToNeverRewinds(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 3)
	p := newPen(s, 0, 0).write(tcell.StyleDefault, "0123456789")
	p.padTo(4)
	if p.col() != 10 {
		t.Errorf("padTo must not move the pen backwards, got %d", p.col())
	}
}
