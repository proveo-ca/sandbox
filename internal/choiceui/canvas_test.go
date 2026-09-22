package choiceui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestPutAdvancesByDisplayWidth(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(20, 2)

	c := newCanvas(s)
	wide := "\uff21" // fullwidth A — two columns, not an emoji
	c.put(0, tcell.StyleDefault, wide+"x")
	s.Show()

	r0, _, _, _ := s.GetContent(0, 0)
	if r0 != 'Ａ' {
		t.Errorf("cell 0: %q, want the wide rune (a wipe looks like the follower)", r0)
	}
	r2, _, _, _ := s.GetContent(2, 0)
	if r2 != 'x' {
		t.Errorf("cell 2: %q, want x sitting after the wide rune", r2)
	}
}

func TestPutPlacesANerdGlyphInOneColumn(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(20, 2)

	c := newCanvas(s)
	key := glyphsFor(GlyphsNerd).key
	c.put(0, tcell.StyleDefault, key+"x")
	s.Show()

	got, _, _, _ := s.GetContent(0, 0)
	if string(got) != key {
		t.Errorf("cell 0: %q, want the key glyph", got)
	}
	x, _, _, _ := s.GetContent(1, 0)
	if x != 'x' {
		t.Errorf("cell 1: %q, want x; a two-column measure of the key would have pushed it to cell 2", x)
	}
}
