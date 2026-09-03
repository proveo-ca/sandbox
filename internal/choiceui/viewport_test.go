package choiceui

import "testing"

func scrollForm() *Form {
	return &Form{Rows: []Row{
		{Label: "egress", Options: []string{"a", "b"}},
		{Label: "credentials", Options: []string{"c", "d"}},
		{Label: "execution", Options: []string{"e"}, Multi: true, Divider: true},
		{Label: "interface", Options: []string{"f"}, Multi: true, Divider: true},
		{Label: "agent evidence", Options: []string{"g"}, Multi: true},
	}}
}

// A divider costs four lines and a plain row one, which is exactly why the
// viewport counts lines and not rows.
func TestBodyLinesCountsDividerFurniture(t *testing.T) {
	t.Parallel()
	lines := scrollForm().bodyLines()
	if got, want := len(lines), 5+3+3; got != want {
		t.Fatalf("%d lines, want %d (five rows, two of them dividers)", got, want)
	}
	// It is also the ONE description of the body's height.
	if got := scrollForm().rowsHeight(); got != len(lines) {
		t.Errorf("rowsHeight = %d but the enumeration is %d; the two have drifted", got, len(lines))
	}
	from, to := rowSpan(lines, 2)
	if to-from != 4 {
		t.Errorf("a divider row spans %d lines, want 4", to-from)
	}
	if lines[from].kind != lineBlank || lines[from+1].kind != lineHeading || lines[to-1].kind != lineRow {
		t.Errorf("a divider must read blank, heading, blank, row; got %v", lines[from:to])
	}
	if f, to := rowSpan(lines, 99); f != 0 || to != 0 {
		t.Errorf("an absent row spans nothing, got (%d, %d)", f, to)
	}
}

// Whatever the height and whatever the cursor, the row the cursor is on must be
// painted. This is the property the whole viewport exists to provide.
func TestScrollToAlwaysKeepsTheCursorOnScreen(t *testing.T) {
	t.Parallel()
	f := scrollForm()
	lines := f.bodyLines()
	for window := 1; window <= len(lines)+2; window++ {
		off := 0
		for _, cursor := range []int{0, 1, 2, 3, 4, 3, 2, 1, 0, 4} {
			off = scrollTo(off, window, lines, cursor)
			_, to := rowSpan(lines, cursor)
			target := to - 1
			if target < off || target >= off+window {
				t.Errorf("window=%d cursor=%d: the row's line %d is outside [%d,%d)",
					window, cursor, target, off, off+window)
			}
			if off < 0 || (off > 0 && off > len(lines)-window) {
				t.Errorf("window=%d cursor=%d: offset %d is out of range", window, cursor, off)
			}
		}
	}
}

// When everything fits there is nothing to scroll, and the body must sit at the
// top — otherwise the render at a comfortable size would differ from today's.
func TestNothingScrollsWhenEverythingFits(t *testing.T) {
	t.Parallel()
	lines := scrollForm().bodyLines()
	for _, cursor := range []int{0, 2, 4} {
		if off := scrollTo(0, len(lines), lines, cursor); off != 0 {
			t.Errorf("cursor=%d: offset %d with a window that fits everything", cursor, off)
		}
	}
	if g := newGutter(0, len(lines), len(lines)); g.scrolls() {
		t.Error("a body that fits must draw no gutter at all")
	}
}

// A stale offset — from a previous paint at another size — is re-clamped rather
// than trusted: the field is a cache, never a source of truth.
func TestScrollToReclampsAStaleOffset(t *testing.T) {
	t.Parallel()
	lines := scrollForm().bodyLines()
	if off := scrollTo(999, 5, lines, 0); off != 0 {
		t.Errorf("a wild offset must be clamped back to the cursor, got %d", off)
	}
	if off := scrollTo(-7, 5, lines, 0); off < 0 {
		t.Errorf("a negative offset must be clamped, got %d", off)
	}
	big := scrollTo(0, len(lines)+10, lines, 4)
	if big != 0 {
		t.Errorf("a window larger than the body must sit at 0, got %d", big)
	}
}

// A group's checkboxes arriving without the heading that names them is a group
// the operator cannot identify, so the whole span comes on screen when it fits.
func TestScrollToPullsAGroupsHeadingWithIt(t *testing.T) {
	t.Parallel()
	f := scrollForm()
	lines := f.bodyLines()
	from, _ := rowSpan(lines, 3) // the second divider
	off := scrollTo(0, 6, lines, 3)
	if off > from {
		t.Errorf("offset %d cuts off the group's heading at line %d", off, from)
	}
	// ...but a window too small for the furniture still shows the row itself.
	tight := scrollTo(0, minBodyLines, lines, 3)
	_, to := rowSpan(lines, 3)
	if target := to - 1; target < tight || target >= tight+minBodyLines {
		t.Errorf("a %d-line window must still show the row, got offset %d for line %d",
			minBodyLines, tight, target)
	}
}

// The gutter says what is off screen: the ends of travel, and a thumb that is
// never rounded away to nothing.
func TestGutterMarksTheEndsAndSizesItsThumb(t *testing.T) {
	t.Parallel()
	total, window := 40, 4
	lines := make([]bodyLine, total)
	for i := range lines {
		lines[i] = bodyLine{row: i, kind: lineRow}
	}
	top := newGutter(0, window, total)
	if g, _ := top.glyph(0, false); g == "▲" {
		t.Error("at the top there is nothing above, so no up mark")
	}
	if g, _ := top.glyph(window-1, false); g != "▼" {
		t.Errorf("more below must be marked, got %q", g)
	}
	if top.hiddenRows(lines) != total-window {
		t.Errorf("hidden = %d, want %d", top.hiddenRows(lines), total-window)
	}
	bottom := newGutter(total-window, window, total)
	if g, _ := bottom.glyph(0, false); g != "▲" {
		t.Errorf("more above must be marked, got %q", g)
	}
	if g, _ := bottom.glyph(window-1, false); g == "▼" {
		t.Error("at the bottom there is nothing below, so no down mark")
	}
	if bottom.hiddenRows(lines) != 0 {
		t.Errorf("nothing is hidden at the end of travel, got %d", bottom.hiddenRows(lines))
	}
	thumb := 0
	for i := 0; i < window; i++ {
		if _, isThumb := top.glyph(i, false); isThumb {
			thumb++
		}
	}
	if thumb < 1 {
		t.Error("a body ten times the window still needs a thumb of at least one line")
	}
}

// The gutter follows the same ladder as everything else the terminal draws.
func TestGutterHasAnASCIITier(t *testing.T) {
	t.Parallel()
	g := newGutter(2, 4, 40)
	for i := 0; i < 4; i++ {
		s, _ := g.glyph(i, true)
		for _, r := range s {
			if r > 127 {
				t.Errorf("the ASCII gutter drew %q (U+%04X)", r, r)
			}
		}
	}
}

// Paging moves the cursor, because a viewport that drifts away from the cursor
// makes the arrow keys teleport. Locked rows are skipped, as move already does.
func TestPageMovesWholeScreenfulsOfSelectableRows(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: make([]Row, 12)}
	f.Rows[3].Locked = true
	if got := f.page(0, +1); got != pageRows+1 {
		t.Errorf("a page down from 0 landed on %d; it must skip the locked row", got)
	}
	if got := f.page(0, -1); got != 0 {
		t.Errorf("a page up from the top must stay at the top, got %d", got)
	}
	if got := f.page(11, +1); got != 11 {
		t.Errorf("a page down from the end must stay at the end, got %d", got)
	}
	if got := f.lastSelectable(); got != 11 {
		t.Errorf("lastSelectable = %d, want 11", got)
	}
	allLocked := &Form{Rows: []Row{{Locked: true}}}
	if got := allLocked.lastSelectable(); got != 0 {
		t.Errorf("a form with nothing selectable must still answer, got %d", got)
	}
}

// The thumb must reach the last line at the end of travel, or the gutter says
// there is somewhere further to go when there is not.
func TestTheThumbReachesBothEnds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ window, total int }{{5, 6}, {4, 40}, {11, 12}, {3, 100}} {
		last := c.total - c.window
		atTop, atEnd := newGutter(0, c.window, c.total), newGutter(last, c.window, c.total)
		if _, thumb := atTop.glyph(0, false); !thumb {
			// Row 0 may be the ▲ mark instead, but only when there IS travel above.
			t.Errorf("window=%d total=%d: the thumb does not start at the top", c.window, c.total)
		}
		if _, thumb := atEnd.glyph(c.window-1, false); !thumb {
			t.Errorf("window=%d total=%d: the thumb never reaches the bottom", c.window, c.total)
		}
	}
}

// The hint counts ROWS, because that is what the operator counts; a body with
// dividers hides more lines than rows.
func TestHiddenIsCountedInRowsNotLines(t *testing.T) {
	t.Parallel()
	f := scrollForm()
	lines := f.bodyLines() // 11 lines, 5 rows
	g := newGutter(0, 4, len(lines))
	if got := g.hiddenRows(lines); got >= len(lines)-4 {
		t.Errorf("hiddenRows = %d; that is a count of lines, not of rows", got)
	}
	// Two, not three: the window ends inside the `execution` group, and a row
	// already partly on screen is not one the operator has to scroll to find.
	if got, want := g.hiddenRows(lines), 2; got != want {
		t.Errorf("hiddenRows = %d, want %d whole rows below the fold", got, want)
	}
}
