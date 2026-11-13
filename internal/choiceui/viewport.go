// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

const (
	bodyIndent   = 2
	labelWidth   = 16                      // marker plus the row name; "› credentials" is 13
	optOrigin    = bodyIndent + labelWidth // column 0 of the four-column grid
	colPitch     = 22                      // the widest column: glyph, a 10-rune cell, a gap
	minPitch     = 16
	lastCell     = 14 // the last column's cell: glyph and a 10-rune label
	columnRunes  = 10
	minBodyLines = 3
	scrollMargin = 1
	pageRows     = 5
)

type lineKind int

const (
	lineHeading lineKind = iota // a divider's stub
	lineRow                     // the row itself
)

type bodyLine struct {
	row  int
	kind lineKind
}

func (f *Form) bodyLines() []bodyLine {
	lines := make([]bodyLine, 0, 2*len(f.Rows))
	for i, r := range f.Rows {
		if r.Divider {
			lines = append(lines, bodyLine{i, lineHeading})
		}
		lines = append(lines, bodyLine{i, lineRow})
	}
	return lines
}

// groups is each row's section: the index of the nearest divider at or above it, -1 before any.
func (f *Form) groups() []int {
	out := make([]int, len(f.Rows))
	at := -1
	for i, r := range f.Rows {
		if r.Divider {
			at = i
		}
		out[i] = at
	}
	return out
}

// lastCol is the largest grid column any row pins a cell to.
func (f *Form) lastCol() int {
	last := 0
	for _, r := range f.Rows {
		for _, c := range r.Cols {
			last = max(last, c)
		}
	}
	return last
}

// pitch is the grid's column pitch in a cw-column card whose last pinned column is last.
func pitch(cw, last int) int {
	if last < 1 {
		return colPitch
	}
	return clampInt((cw-optOrigin-lastCell)/last, minPitch, colPitch)
}

func rowSpan(lines []bodyLine, row int) (from, to int) {
	from, to = -1, -1
	for i, l := range lines {
		if l.row != row {
			continue
		}
		if from < 0 {
			from = i
		}
		to = i + 1
	}
	if from < 0 {
		return 0, 0
	}
	return from, to
}

func scrollTo(prev, window int, lines []bodyLine, cursor int) int {
	limit := len(lines) - window
	if limit < 0 {
		limit = 0
	}
	off := clampInt(prev, 0, limit)
	from, to := rowSpan(lines, cursor)
	if to == 0 {
		return off
	}
	if to-from <= window {
		if from < off {
			off = from
		}
		if to > off+window {
			off = to - window
		}
	}
	target := to - 1
	margin := clampInt(scrollMargin, 0, (window-1)/2)
	if target-margin < off {
		off = target - margin
	}
	if target+margin >= off+window {
		off = target + margin - window + 1
	}
	return clampInt(off, 0, limit)
}

type gutter struct {
	off, window, total int
}

func newGutter(off, window, total int) gutter {
	return gutter{off: off, window: window, total: total}
}

func (g gutter) scrolls() bool { return g.total > g.window && g.window > 0 }

func (g gutter) glyph(i int, ascii bool) (string, bool) {
	if !g.scrolls() {
		return "", false
	}
	up, down, bar := "▲", "▼", "░"
	if ascii {
		up, down, bar = "^", "v", "#"
	}
	switch {
	case i == 0 && g.off > 0:
		return up, false
	case i == g.window-1 && g.off+g.window < g.total:
		return down, false
	}
	size := clampInt(g.window*g.window/g.total, 1, g.window)
	travel, room := g.total-g.window, g.window-size
	start := 0
	if travel > 0 && room > 0 {
		start = g.off * room / travel
	}
	start = clampInt(start, 0, room)
	if i >= start && i < start+size {
		return bar, true
	}
	return "", false
}

func (g gutter) hiddenRows(lines []bodyLine) int {
	if !g.scrolls() {
		return 0
	}
	seen := map[int]bool{}
	for i := g.off + g.window; i < len(lines); i++ {
		seen[lines[i].row] = true
	}
	for i := g.off; i < g.off+g.window && i < len(lines); i++ {
		delete(seen, lines[i].row)
	}
	return len(seen)
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (f *Form) lastSelectable() int {
	last := f.firstSelectable()
	for i := range f.Rows {
		if !f.Rows[i].Locked {
			last = i
		}
	}
	return last
}

func (f *Form) page(cursor, delta int) int {
	for n := 0; n < pageRows; n++ {
		next := f.move(cursor, delta)
		if next == cursor {
			break
		}
		cursor = next
	}
	return cursor
}
