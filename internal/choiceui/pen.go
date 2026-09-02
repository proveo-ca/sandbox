// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
)

// pen writes styled runs left to right on one row, advancing by DISPLAY width
// rather than by rune count.
//
// The strip is assembled from variable-width pieces — a devicon here, a label
// of unknown length there — so no caller can precompute the columns the way the
// row painter does. `put` is left alone for exactly that reason: its callers do
// their own arithmetic (`x += len(glyph) + len(opt) + 3`), and making it
// width-aware without rewriting them would only let the two disagree.
//
// A zero-width rune is attached to the PRECEDING cell as a tcell combining rune
// instead of being given a column. U+FE0F is the case that matters: it is what
// makes a variation-selected emoji two runes and one grapheme, and go-runewidth
// measures the pair as one column while a terminal draws two.
type pen struct {
	s     tcell.Screen
	x, y  int
	last  int // display width of the last rune written, so combining marks land on its BASE cell
	wrote bool
}

func newPen(s tcell.Screen, x, y int) *pen { return &pen{s: s, x: x, y: y} }

func (p *pen) col() int { return p.x }

// write paints text at the pen and leaves the pen after it.
func (p *pen) write(style tcell.Style, text string) *pen {
	for _, r := range text {
		if zeroWidth(r) {
			// Nothing of ours to combine onto yet, so there is nowhere to put it:
			// writing to p.x-1 would scribble on a cell another writer owns.
			if !p.wrote {
				continue
			}
			// p.x-1 is the CONTINUATION cell of a wide rune, not its base. Writing
			// the mark there would attach it to a cell tcell owns rather than to
			// the glyph it decorates.
			base := p.x - p.last
			mainc, combc, st, _ := p.s.GetContent(base, p.y)
			p.s.SetContent(base, p.y, mainc, append(append([]rune(nil), combc...), r), st)
			continue
		}
		w := runewidth.RuneWidth(r)
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

// padTo advances to an absolute column, writing nothing if already past it.
func (p *pen) padTo(col int) *pen {
	for p.x < col {
		p.s.SetContent(p.x, p.y, ' ', nil, tcell.StyleDefault)
		p.x++
	}
	return p
}

// zeroWidth reports a rune that decorates the cell before it instead of taking
// a column of its own.
//
// It is asked directly rather than through go-runewidth, which reports U+FE0F —
// the variation selector — as ONE column. That single disagreement is why emoji
// are kept out of the strip entirely: a writer that trusts the width table
// silently smears every variation-selected glyph one cell to the right.
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

// textWidth is the columns text will occupy once written. Asked rather than
// assumed: a Nerd Font devicon lives in the private-use area, which Unicode
// classes as AMBIGUOUS width — one column in a Latin locale and two in a CJK
// one — so the figure's own budget has to be measured the same way tcell will
// measure it when it paints.
func textWidth(text string) int {
	w := 0
	for _, r := range text {
		if zeroWidth(r) {
			continue
		}
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		w += rw
	}
	return w
}
