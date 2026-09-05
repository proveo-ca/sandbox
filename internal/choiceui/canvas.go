// SPEC: _spec/internal/choiceui/viewport.puml
//
// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import "github.com/gdamore/tcell/v2"

type canvas struct {
	s        tcell.Screen
	y        int
	top, bot int // half-open; writes outside are dropped
}

func newCanvas(s tcell.Screen) *canvas {
	_, h := s.Size()
	return &canvas{s: s, top: 0, bot: h}
}

func (c *canvas) put(x int, style tcell.Style, text string) {
	if c.y < c.top || c.y >= c.bot {
		return
	}
	for i, r := range []rune(text) {
		c.s.SetContent(x+i, c.y, r, nil, style)
	}
}

func (c *canvas) clipTo(top, bot int) { c.top, c.bot = top, bot }

func (c *canvas) unclip() {
	_, h := c.s.Size()
	c.top, c.bot = 0, h
}
