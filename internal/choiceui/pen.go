// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
	runewidth.CreateLUT()
}

// pen writes runs left to right on one row of a screen and keeps the column it reached.
type pen struct {
	s     *screen
	x, y  int
	wrote bool
}

func newPen(s *screen, x, y int) *pen { return &pen{s: s, x: x, y: y} }

func (p *pen) col() int { return p.x }

// write puts text at the pen and moves it past; a combining rune with nothing before it is dropped.
func (p *pen) write(style lipgloss.Style, text string) *pen {
	if !p.wrote {
		text = strings.TrimLeftFunc(text, zeroWidth)
	}
	if text == "" {
		return p
	}
	p.s.put(p.x, p.y, style, text)
	p.x += textWidth(text)
	p.wrote = true
	return p
}

// padTo blanks the row up to col, covering what an earlier run left there.
func (p *pen) padTo(col int) *pen {
	if p.x < col {
		p.s.put(p.x, p.y, lipgloss.NewStyle(), strings.Repeat(" ", col-p.x))
		p.x = col
	}
	return p
}

func zeroWidth(r rune) bool {
	switch {
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r == 0x200D: // zero-width joiner
		return true
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		return true
	}
	return false
}

func runeCols(r rune) int {
	if zeroWidth(r) {
		return 0
	}
	if r >= 0xE000 && r <= 0xF8FF {
		return 1
	}
	w := runewidth.RuneWidth(r)
	if w < 1 {
		return 1
	}
	return w
}

func textWidth(text string) int {
	w := 0
	for _, r := range text {
		w += runeCols(r)
	}
	return w
}
