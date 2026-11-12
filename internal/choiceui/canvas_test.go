// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPutKeepsAWideRuneAndWhatFollowsIt(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 20, 2)
	c := newCanvas(s)
	wide := "Ａ" // fullwidth A — two columns, not an emoji
	c.put(0, lipgloss.NewStyle(), wide+"x")
	c.put(3, lipgloss.NewStyle(), "|")
	if got := screenLines(s)[0]; got != wide+"x|" {
		t.Errorf("row = %q, want the wide rune, then x in the third column, then the bar", got)
	}
}

func TestPutPlacesANerdGlyphInOneColumn(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 20, 2)
	c := newCanvas(s)
	key := glyphsFor(GlyphsNerd).key
	c.put(0, lipgloss.NewStyle(), key+"x")
	c.put(2, lipgloss.NewStyle(), "|")
	if got := screenLines(s)[0]; got != key+"x|" {
		t.Errorf("row = %q, want the key glyph in one column: a two-column measure would have put the bar over x", got)
	}
}

func TestAWriteOutsideTheWindowIsDropped(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 20, 6)
	c := newCanvas(s)
	c.clipTo(2, 4)
	for y := 0; y < 6; y++ {
		c.y = y
		c.put(0, lipgloss.NewStyle(), "row")
	}
	lines := screenLines(s)
	for y, want := range []string{"", "", "row", "row", "", ""} {
		if lines[y] != want {
			t.Errorf("row %d = %q, want %q: a write outside the window must be dropped whole", y, lines[y], want)
		}
	}
	c.unclip()
	c.y = 5
	c.put(0, lipgloss.NewStyle(), "foot")
	if got := screenLines(s)[5]; got != "foot" {
		t.Errorf("after unclip row 5 = %q", got)
	}
}
