// SPEC: _spec/internal/choiceui/viewport.puml
package choiceui

const (
	// bodyIndent is the two columns the gutter takes from the scrolling body.
	// The head and the foot keep their own columns: they never scroll, so they
	// have no gutter to make room for.
	bodyIndent = 2
	// minBodyLines is the cursor's line plus one of context either side. Below
	// this the form stops being navigable and the prompt says so instead.
	minBodyLines = 3
	// scrollMargin keeps a line of context ahead of the cursor, so arriving at
	// the edge of the window shows there is something past it.
	scrollMargin = 1
	// pageRows is how far PgUp/PgDn move, in SELECTABLE rows. Deliberately a
	// fixed approximation of a screenful: Run has no layout to ask, and
	// threading one into the event loop to make a page exact is not worth it.
	pageRows = 5
)

// lineKind is what one painted line of the body is.
type lineKind int

const (
	lineBlank   lineKind = iota // a divider's leading and trailing blank
	lineHeading                 // a divider's centred heading
	lineRow                     // the row itself
)

// bodyLine is one screen line of the scrolling body, and the row it belongs to.
// The body scrolls by LINE, never by row index; `row` is valid on every line,
// furniture included.
type bodyLine struct {
	row  int
	kind lineKind
}

// bodyLines enumerates every line the body paints, in order — the ONE
// description of the body's height.
func (f *Form) bodyLines() []bodyLine {
	lines := make([]bodyLine, 0, len(f.Rows)+4)
	for i, r := range f.Rows {
		if r.Divider {
			lines = append(lines,
				bodyLine{i, lineBlank}, bodyLine{i, lineHeading}, bodyLine{i, lineBlank})
		}
		lines = append(lines, bodyLine{i, lineRow})
	}
	return lines
}

// rowSpan is the half-open line range one row occupies, or (0, 0) when absent.
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

// scrollTo chooses the offset that keeps the cursor's row on screen. `prev` is
// a cache and never a source of truth.
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
	// Pull the WHOLE group on screen when it fits: a set of checkboxes arriving
	// without the heading that names it is a set the operator cannot identify.
	if to-from <= window {
		if from < off {
			off = from
		}
		if to > off+window {
			off = to - window
		}
	}
	// Then guarantee the row's own line with its margin. This runs second so it
	// wins: when the window is too small for the furniture, the row the cursor
	// is on is the thing that must be visible.
	target := to - 1
	// The margin is what the window can afford, not a constant: asking for a
	// line of context either side of a two-line window pushes the cursor's own
	// line off the top, which is the one thing this function must never do.
	margin := clampInt(scrollMargin, 0, (window-1)/2)
	if target-margin < off {
		off = target - margin
	}
	if target+margin >= off+window {
		off = target + margin - window + 1
	}
	return clampInt(off, 0, limit)
}

// gutter is the one-column scrollbar drawn at the body's left edge: the ends of
// travel, and a thumb whose size says how much of the body is on screen.
type gutter struct {
	off, window, total int
}

func newGutter(off, window, total int) gutter {
	return gutter{off: off, window: window, total: total}
}

// scrolls reports whether there is anything off screen to indicate.
func (g gutter) scrolls() bool { return g.total > g.window && g.window > 0 }

// glyph is what to draw beside visible line i, and whether it is the thumb
// rather than the track — the caller styles the two differently.
func (g gutter) glyph(i int, ascii bool) (string, bool) {
	if !g.scrolls() {
		return "", false
	}
	up, down, bar := "▲", "▼", "░"
	if ascii {
		up, down, bar = "^", "v", "|"
	}
	switch {
	case i == 0 && g.off > 0:
		return up, false
	case i == g.window-1 && g.off+g.window < g.total:
		return down, false
	}
	// A proportional thumb, never shorter than one line, positioned by scaling the
	// offset over its TRAVEL rather than over the total.
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

// hiddenRows is how many ROWS lie below the window, for the hint to name.
func (g gutter) hiddenRows(lines []bodyLine) int {
	if !g.scrolls() {
		return 0
	}
	seen := map[int]bool{}
	for i := g.off + g.window; i < len(lines); i++ {
		seen[lines[i].row] = true
	}
	// A row straddling the fold is already partly visible, so it is not "below".
	for i := g.off; i < g.off+g.window && i < len(lines); i++ {
		delete(seen, lines[i].row)
	}
	return len(seen)
}

// clampInt confines v to [lo, hi]. An inverted range collapses to lo rather
// than answering differently depending on which side v fell — a shared helper
// with three callers should not have a silent second meaning.
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

// lastSelectable is End's destination, and the other half of firstSelectable.
func (f *Form) lastSelectable() int {
	last := f.firstSelectable()
	for i := range f.Rows {
		if !f.Rows[i].Locked {
			last = i
		}
	}
	return last
}

// page moves the cursor by whole screenfuls, counted in selectable rows.
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
