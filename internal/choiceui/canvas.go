// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

import "charm.land/lipgloss/v2"

// canvas writes rows onto a screen through a window: a write to a row outside it is dropped,
// and a write past its right edge is cut there.
type canvas struct {
	s        *screen
	y        int
	top, bot int // half-open; writes outside are dropped
	right    int // the first column a write may not reach
}

func newCanvas(s *screen) *canvas {
	w, h := s.Size()
	return &canvas{s: s, top: 0, bot: h, right: w}
}

func (c *canvas) put(x int, style lipgloss.Style, text string) {
	if c.y < c.top || c.y >= c.bot || x >= c.right {
		return
	}
	c.s.put(x, c.y, style, cutCols(text, c.right-x))
}

func (c *canvas) clipTo(top, bot int) { c.top, c.bot = top, bot }

func (c *canvas) unclip() {
	w, h := c.s.Size()
	c.top, c.bot, c.right = 0, h, w
}

// cutCols is text cut to at most n columns.
func cutCols(text string, n int) string {
	if textWidth(text) <= n {
		return text
	}
	cols := 0
	for i, r := range text {
		if cols+runeCols(r) > n {
			return text[:i]
		}
		cols += runeCols(r)
	}
	return text
}
