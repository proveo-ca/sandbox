// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import "charm.land/lipgloss/v2"

// canvas writes rows onto a screen through a window: a write to a row outside it is dropped.
type canvas struct {
	s        *screen
	y        int
	top, bot int // half-open; writes outside are dropped
}

func newCanvas(s *screen) *canvas {
	_, h := s.Size()
	return &canvas{s: s, top: 0, bot: h}
}

func (c *canvas) put(x int, style lipgloss.Style, text string) {
	if c.y < c.top || c.y >= c.bot {
		return
	}
	c.s.put(x, c.y, style, text)
}

func (c *canvas) clipTo(top, bot int) { c.top, c.bot = top, bot }

func (c *canvas) unclip() {
	_, h := c.s.Size()
	c.top, c.bot = 0, h
}
