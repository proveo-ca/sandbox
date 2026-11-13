// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import "strings"

const (
	cardMax  = 120 // the widest card, walls included
	cardMin  = 40  // a narrower terminal draws no frame
	cardRows = 5   // top border, head, foot rule, key line, bottom border
	stubMin  = 16  // a stub's columns when its name is short
	factX    = 4   // the facts' column
)

// cardWidth is the card's width on a w-column terminal.
func cardWidth(w int) int { return min(w, cardMax) }

// framed reports whether a w-column terminal draws the card's frame.
func framed(w int) bool { return w >= cardMin }

// inset is the column the title and the key line start at.
func inset(w int) int {
	if framed(w) {
		return 2
	}
	return 0
}

// edge is the first column text inside the card may not reach.
func edge(w int) int {
	if framed(w) {
		return cardWidth(w) - 2
	}
	return w
}

// keyRoom is the key line's width.
func keyRoom(w int) int { return edge(w) - inset(w) }

// helpWidth is where the help under the card wraps.
func helpWidth(w int) int { return cardWidth(w) - 4 }

// chipFits reports whether a step chip fits beside a title of titleLen inside a cw-column card.
func chipFits(titleLen, chipLen, cw int) bool { return titleLen+2+chipLen <= cw-4 }

// card draws one page's frame on a w-column terminal.
type card struct {
	w      int // the card's columns, walls included
	framed bool
	edge   int
	g      glyphSet
	p      palette
}

func newCard(w int, tier GlyphTier, p palette) card {
	return card{w: cardWidth(w), framed: framed(w), edge: edge(w), g: glyphsFor(tier), p: p}
}

// head draws the top border and the title row with its chip; unframed, the title takes the border's row.
func (k card) head(c *canvas, title, chip string) {
	if !k.framed {
		c.put(0, k.p.title, clip(title, k.w))
		c.y += 2
		return
	}
	k.border(c, k.g.frameTL, k.g.frameTR)
	c.y++
	c.put(2, k.p.title, clip(title, k.edge-2))
	if chip != "" {
		c.put(k.edge-textWidth(chip), k.p.chip, chip)
	}
	k.walls(c, false)
	c.y++
}

// foot draws the rule over the key line.
func (k card) foot(c *canvas) { k.border(c, k.g.joinL, k.g.joinR) }

// bottom draws the bottom border.
func (k card) bottom(c *canvas) { k.border(c, k.g.frameBL, k.g.frameBR) }

func (k card) border(c *canvas, left, right string) {
	if k.framed {
		c.put(0, k.p.frame, left+strings.Repeat(k.g.rule, k.w-2)+right)
	}
}

// walls draws both walls on the canvas row; lit draws the left one as the rail.
func (k card) walls(c *canvas, lit bool) {
	k.wallL(c, lit)
	k.wallR(c)
}

func (k card) wallL(c *canvas, lit bool) {
	if !k.framed {
		return
	}
	st := k.p.frame
	if lit {
		st = k.p.rail
	}
	c.put(0, st, k.g.wallL)
}

func (k card) wallR(c *canvas) {
	if k.framed {
		c.put(k.w-1, k.p.frame, k.g.wallR)
	}
}

// gutterX is the column the scroll marks ride: the right wall, or column 0 unframed.
func (k card) gutterX() int {
	if k.framed {
		return k.w - 1
	}
	return 0
}

// stub draws a section's opening line off the left wall: the name at column 4, a rule to at least column 15.
func (k card) stub(c *canvas, name string, lit bool) {
	rule, text := k.p.frame, k.p.frame
	if lit {
		rule, text = k.p.rail, k.p.title
	}
	lead := k.g.joinL
	if !k.framed {
		lead = k.g.rule
	}
	name = clipRunes(name, max(1, k.edge-7))
	tail := max(2, stubMin-5-textWidth(name))
	c.put(0, rule, lead+k.g.rule+k.g.rule)
	c.put(4, text, name)
	c.put(5+textWidth(name), rule, strings.Repeat(k.g.rule, tail))
}

// fitCell is a cell cut to room columns; the label gives way first, with … before the arrow.
func fitCell(glyph, cell, arrow string, room int) string {
	if whole := glyph + cell + arrow; textWidth(whole) <= room {
		return whole
	}
	if n := room - textWidth(glyph+arrow); n >= 1 {
		return glyph + clipRunes(cell, n) + arrow
	}
	return clip(glyph+cell+arrow, room)
}
