// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
)

func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
	runewidth.CreateLUT()
}

type pen struct {
	s     tcell.Screen
	x, y  int
	last  int // display width of the last rune written
	wrote bool
}

func newPen(s tcell.Screen, x, y int) *pen { return &pen{s: s, x: x, y: y} }

func (p *pen) col() int { return p.x }

func (p *pen) write(style tcell.Style, text string) *pen {
	for _, r := range text {
		if zeroWidth(r) {
			if !p.wrote {
				continue
			}
			base := p.x - p.last
			mainc, combc, st, _ := p.s.GetContent(base, p.y)
			p.s.SetContent(base, p.y, mainc, append(append([]rune(nil), combc...), r), st)
			continue
		}
		w := runeCols(r)
		if w < 1 {
			w = 1
		}
		p.s.SetContent(p.x, p.y, r, nil, style)
		p.x += w
		p.last = w
		p.wrote = true
	}
	return p
}

func (p *pen) padTo(col int) *pen {
	for p.x < col {
		p.s.SetContent(p.x, p.y, ' ', nil, tcell.StyleDefault)
		p.x++
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
