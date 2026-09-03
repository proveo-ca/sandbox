// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import "github.com/gdamore/tcell/v2"

// canvas is the one writer draw() paints through. It owns the current row plus
// an open window: a write outside the window is DROPPED, not clipped. Vertical
// only — horizontal clipping is clip().
type canvas struct {
	s        tcell.Screen
	y        int
	top, bot int // half-open; writes outside are dropped
}

func newCanvas(s tcell.Screen) *canvas {
	_, h := s.Size()
	return &canvas{s: s, top: 0, bot: h}
}

// put writes text at the canvas's current row. The signature matches the
// closure putHeader already takes, so that caller compiles unchanged.
func (c *canvas) put(x int, style tcell.Style, text string) {
	if c.y < c.top || c.y >= c.bot {
		return
	}
	for i, r := range []rune(text) {
		c.s.SetContent(x+i, c.y, r, nil, style)
	}
}

// clipTo narrows the window to a half-open row range.
func (c *canvas) clipTo(top, bot int) { c.top, c.bot = top, bot }

// unclip reopens the window to the whole screen.
func (c *canvas) unclip() {
	_, h := c.s.Size()
	c.top, c.bot = 0, h
}
