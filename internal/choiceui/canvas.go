// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import "github.com/gdamore/tcell/v2"

// canvas is the one writer draw() paints through. It owns the current row —
// the `y` the paint used to close over — plus an open window: a write outside
// the window is DROPPED, not clipped.
//
// Dropped rather than clipped, because a half-painted row is worse than an
// absent one: tcell would happily let a scrolled-off body row paint over the
// hint, and a foot silently painted over is the exact failure the viewport
// exists to end. Vertical only — horizontal clipping already exists as clip(),
// and the row painter's column arithmetic is left alone.
//
// With the window wide open it is byte-for-byte the closure it replaces, which
// is what let it land as a proven no-op before anything scrolled.
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
