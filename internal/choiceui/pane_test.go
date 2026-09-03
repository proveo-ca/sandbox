package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// paneFrame is the worst case the pane has to hold, taken from what
// internal/run/topology.go can actually produce: the longest hop label, beside
// the widest key, with the longest square and the longest interface label.
func paneFrame() Frame {
	return Frame{
		Host: "pluvo", HostOS: "(macOS)",
		Square: "dind · claudecode", Hop: "mitm + squid",
		Interface: "tui + browser + chrome",
		Key:       KeyAtHop, Lane: LaneScreened, Open: 1, Refused: 2, Speaking: true,
		Caption: "deny-all · broker — only proveo's allowlist gets out; the key stops at the hop",
	}
}

// The pane is a fidelity of the figure, not a fourth drawing: it may drop the
// connector runs and the caption's explanatory clause, but every FACT survives.
func TestPaneKeepsEveryFact(t *testing.T) {
	t.Parallel()
	for _, tier := range []GlyphTier{GlyphsNerd, GlyphsASCII} {
		g := glyphsFor(tier)
		s := simScreen(t, 140, 8)
		drawFigure(s, 0, 0, paneCols, paneFrame(), tier, styles(), 0)
		s.Show()
		joined := strings.Join(screenLines(s), "\n")
		s.Fini()

		for _, want := range []string{"pluvo", "(macOS)", "dind · claudecode", "mitm + squid",
			"tui + browser + chrome"} {
			if !strings.Contains(joined, want) {
				t.Errorf("tier %v: the pane dropped %q, which is a fact and not decoration:\n%s",
					tier, want, joined)
			}
		}
		// Located, not counted. A one- or two-column glyph turns up inside labels
		// and inside other glyphs — "x" lives in "sbx", "o-" is a node on a rule —
		// so the honest question is whether the key is where the frame says the
		// credential rests, and nowhere else it could be mistaken for.
		if !strings.Contains(hopRow(joined, g.cornerTL), g.key) {
			t.Errorf("tier %v: the key is not beside the hop:\n%s", tier, joined)
		}
		// Counted as the END OF A LANE, not as a bare glyph: "sbx · claudecode"
		// contains the ASCII refusal glyph.
		// A lane that SURVIVES ends in a cloud; the bare arrow also appears on the
		// connector, so the cloud is what counts.
		if n := strings.Count(joined, g.east+" "+g.cloud); n != 1 {
			t.Errorf("tier %v: %d surviving lanes, want the frame's 1", tier, n)
		}
		if n := strings.Count(joined, g.screened+g.refused); n != 2 {
			t.Errorf("tier %v: %d refused lanes, want the frame's 2", tier, n)
		}
		if !strings.Contains(joined, g.speaking) {
			t.Errorf("tier %v: verbose evidence is a fact and must survive", tier)
		}
	}
}

// Nothing may reach past the pane's declared width, or it would paint over
// whatever the terminal has to the right of it.
func TestPaneStaysInsideItsColumns(t *testing.T) {
	t.Parallel()
	frames := []Frame{paneFrame()}
	rev := paneFrame()
	rev.Lane, rev.Open, rev.Refused = LaneAsked, 0, 0
	byp := paneFrame()
	byp.Hop, byp.Key, byp.Lane, byp.Open, byp.Refused = "", KeyInSquare, LaneWatched, 3, 0
	frames = append(frames, rev, byp)

	for _, tier := range []GlyphTier{GlyphsNerd, GlyphsASCII} {
		for i, fr := range frames {
			const origin = 40
			s2 := simScreen(t, 240, 8)
			// tick > 0 as well: the mote is drawn on a lane, and a pane lane is one
			// column wide, so an animated frame is where an overrun would show.
			drawFigure(s2, origin, 0, paneCols, fr, tier, styles(), 3)
			s2.Show()
			lines := screenLines(s2)
			for y, last := range lastPaintedCol(s2) {
				if last >= origin+paneCols.width {
					t.Errorf("tier %v frame %d row %d reaches column %d, past the pane's %d:\n%q",
						tier, i, y, last+1, origin+paneCols.width, lines[y])
				}
				if last >= 0 && last < origin {
					t.Errorf("tier %v frame %d row %d painted left of its origin, at %d",
						tier, i, y, last)
				}
			}
			s2.Fini()
		}
	}
}

// The pane owns exactly paneRows and has no leading blank: it is anchored to the
// body's top line, not floated below one.
func TestPaneOwnsExactlyItsRows(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 140, 8)
	drawFigure(s, 0, 0, paneCols, paneFrame(), GlyphsNerd, styles(), 0)
	s.Show()
	lines := screenLines(s)
	s.Fini()
	// Row 0 is the question row, blank on every tier but review. The figure
	// proper starts on row 1 with the container's lid.
	for _, y := range []int{1, 2, 3, 4, 5} {
		if lines[y] == "" {
			t.Errorf("row %d of the figure is empty; nothing was drawn", y)
		}
	}
	for y := paneRows; y < len(lines); y++ {
		if lines[y] != "" {
			t.Errorf("the pane painted past its %d rows, at %d: %q", paneRows, y, lines[y])
		}
	}
}

// The pane's caption is the leading clause, which is what fits; the block keeps
// the whole sentence.
func TestCaptionHeadKeepsTheFactsAndDropsTheGloss(t *testing.T) {
	t.Parallel()
	if got := captionHead("deny-all · broker — only proveo's allowlist gets out"); got != "deny-all · broker" {
		t.Errorf("captionHead = %q", got)
	}
	// A caption with nothing to cut on is left whole for the caller to clip.
	if got := captionHead("no clause here"); got != "no clause here" {
		t.Errorf("captionHead = %q, want the caption unchanged", got)
	}
}

// lastPaintedCol is the last non-blank COLUMN of each row. Measured off the cell
// grid rather than a string's len(), which counts bytes: every rule and devicon
// in this figure is multi-byte, so a byte count reads far past the real edge.
func lastPaintedCol(s tcell.SimulationScreen) []int {
	cells, w, h := s.GetContents()
	out := make([]int, h)
	for y := 0; y < h; y++ {
		last := -1
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 && r[0] != ' ' && r[0] != 0 {
				last = x
			}
		}
		out[y] = last
	}
	return out
}

// paneWidth is the narrowest terminal at which this form draws the pane, and
// blockWidth the widest at which it still draws the block. Derived for the same
// reason scrollingHeight is: the breakpoint is a property of the form's own
// rows, and a literal would only record whatever the columns were that day.
// paneHeight is too short for the block figure and tall enough for the pane.
const paneHeight = 18

func paneWidth(f *Form) int  { return f.bodyRight() + paneGutter + paneCols.width }
func blockWidth(f *Form) int { return paneWidth(f) - 1 }

// THE load-bearing test of the whole feature: the budget must agree with the
// painter, or the pane silently overwrites an option. Asserted as "the gutter
// is empty", which is the invariant itself rather than a proxy for it.
func TestTheRowsNeverReachIntoThePane(t *testing.T) {
	t.Parallel()
	f := tallForm()
	// A locked row with a long reason: the one thing the limit actually clips.
	f.Rows[0].Locked = true
	f.Rows[0].Reason = "change it with `sbx policy reset && sbx policy init --deny-all`"
	w := paneWidth(f)
	lay := f.layout(w, 30)
	if lay.strip != stripPane {
		t.Fatalf("the fixture must draw the pane at width %d, or this proves nothing", w)
	}
	rows := renderAt(t, f, 0, w, 30)
	for _, y := range gutterBreaches(rows, lay.pane) {
		t.Errorf("row %d reached into the pane's gutter:\n%q", y, rows[y])
	}
}

// gutterBreaches reports rows where the body reached into the pane's gutter,
// scoped to the rows the pane actually occupies. The hint and help block sit
// BELOW the figure and legitimately span the full width.
func gutterBreaches(rows []string, pane int) []int {
	spine := -1
	for y, line := range rows {
		if strings.Contains(line, figureMark()) {
			spine = y
		}
	}
	if spine < 1 {
		return []int{-1} // the figure was not drawn; the caller's fixture is wrong
	}
	var bad []int
	for y := spine - 1; y < spine-1+paneRows && y < len(rows); y++ {
		r := []rune(rows[y])
		for x := pane - paneGutter; x < pane && x < len(r); x++ {
			if r[x] != ' ' {
				bad = append(bad, y)
				break
			}
		}
	}
	return bad
}

// The pane is free: turning it on takes no row from anything.
func TestThePaneCostsNoHeight(t *testing.T) {
	t.Parallel()
	f := tallForm()
	seen := 0
	for h := 12; h <= 50; h++ {
		wide, narrow := f.layout(paneWidth(f), h), f.layout(blockWidth(f), h)
		if wide.strip != stripPane {
			continue
		}
		seen++
		if wide.body < narrow.body {
			t.Errorf("h=%d: the pane cost the body %d lines", h, narrow.body-wide.body)
		}
		// It is the only fidelity that can leave the banner standing where the
		// block could not — that is the whole payoff.
		if narrow.banner && !wide.banner {
			t.Errorf("h=%d: the pane evicted the banner the block had kept", h)
		}
		// The payoff, asserted rather than assumed. It is NOT that the pane buys
		// body rows back — since the ladder charges the body in full, no rung can
		// take one. It is that the figure is drawn at heights where the block
		// would have been refused outright for want of five rows.
		if narrow.strip == stripNone {
			seen += 1000
		}
	}
	if seen == 0 {
		t.Fatal("no height drew the pane, so nothing above was checked")
	}
	if seen < 1000 {
		t.Error("no height showed the pane drawn where the block was refused — " +
			"the fixture cannot demonstrate what the pane is for")
	}
}

// Widening a terminal never takes the figure away. Swept at paneHeight, where
// the block does not fit and the pane is therefore the outcome.
func TestThePaneIsMonotonicInWidth(t *testing.T) {
	t.Parallel()
	f := tallForm()
	prev := f.layout(60, paneHeight)
	reached := false
	for w := 61; w <= 260; w++ {
		lay := f.layout(w, paneHeight)
		if prev.strip > lay.strip {
			t.Errorf("w=%d: the figure shrank when the terminal WIDENED (%v -> %v)",
				w, prev.strip, lay.strip)
		}
		if lay.strip == stripPane {
			reached = true
		}
		prev = lay
	}
	if !reached {
		t.Fatal("the sweep never reached the pane, so it only checked block monotonicity")
	}
}

// The pane sits BESIDE the rows, not under them, and never on top of one.
func TestThePaneIsBesideTheBodyAndOverwritesNothing(t *testing.T) {
	t.Parallel()
	f := tallForm()
	w := paneWidth(f)
	rows := renderAt(t, f, 1, w, 30)
	joined := strings.Join(rows, "\n")

	if f.layout(w, 30).strip != stripPane {
		t.Fatalf("the fixture must draw the pane at width %d, or this proves nothing", w)
	}
	// Every option of every row survives intact.
	for _, r := range f.Rows {
		for _, opt := range r.Options {
			if !strings.Contains(joined, opt) {
				t.Errorf("the pane overwrote the option %q:\n%s", opt, joined)
			}
		}
	}
	// The figure shares a line with a body row rather than living below the hint.
	hint, figure := -1, -1
	for y, line := range rows {
		if strings.Contains(line, "enter accept") {
			hint = y
		}
		if strings.Contains(line, figureMark()) {
			figure = y
		}
	}
	if figure < 0 || hint < 0 {
		t.Fatalf("figure=%d hint=%d", figure, hint)
	}
	if figure > hint {
		t.Errorf("the pane was drawn below the hint at %d; it belongs beside the rows", figure)
	}
	if !strings.Contains(rows[figure], "credentials") && !strings.Contains(rows[figure], "egress") {
		t.Errorf("the figure's spine should share a line with a body row, got %q", rows[figure])
	}
}

// The pane no longer waits for a reason: the row does not draw one, so its
// width is its label and its options.
// SPEC: _spec/internal/choiceui/choice-prompt-render.puml
func TestALongReasonNoLongerWidensTheRow(t *testing.T) {
	t.Parallel()
	f := tallForm()
	f.Rows[0].Locked = true
	f.Rows[0].Reason = "change it with `sbx policy reset && sbx policy init --deny-all`"

	bare := tallForm()
	if got, want := paneWidth(f), paneWidth(bare); got != want {
		t.Errorf("the reason still influences the pane breakpoint: %d vs %d", got, want)
	}
	// And it is nowhere on the row, at any width the pane appears at.
	w := paneWidth(f)
	if f.layout(w, 30).strip != stripPane {
		t.Fatalf("the fixture must draw the pane at %d", w)
	}
	onOtherRow := strings.Join(renderAt(t, f, 1, w, 30), "\n")
	if strings.Contains(onOtherRow, "sbx policy reset") {
		t.Errorf("the reason must not be drawn inline:\n%s", onOtherRow)
	}
	// Hovering it is what shows it, in full.
	onRow := strings.Join(renderAt(t, f, 0, w, 30), "\n")
	if !strings.Contains(onRow, "sbx policy reset") {
		t.Errorf("hovering must reveal the reason:\n%s", onRow)
	}
}

// A form with no projection must not have its rows narrowed for a figure that
// is never drawn.
func TestNoTopologyMeansNoPaneAtAnyWidth(t *testing.T) {
	t.Parallel()
	f := tallForm()
	f.Topology = nil
	for w := 60; w <= 300; w += 7 {
		if lay := f.layout(w, 40); lay.strip == stripPane {
			t.Fatalf("w=%d: a nil projection produced a pane", w)
		}
	}
	if got := f.paneOrigin(300); got != -1 {
		t.Errorf("paneOrigin = %d for a nil projection, want -1", got)
	}
}

// The budget must cover everything the body PAINTS, not just its options:
// a centred divider heading and a row label are both outside the limit.
func TestTheBudgetCoversLabelsAndHeadings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		row  Row
	}{
		{"a label longer than the option column", Row{
			Label: "an extraordinarily long row label that outruns every option column here", Options: []string{"a"}}},
		{"a heading wider than the 72 it centres on", Row{
			Label:   "an extraordinarily long divider heading that runs past seventy-two columns",
			Options: []string{"a"}, Multi: true, Divider: true}},
	} {
		f := &Form{
			Title: "t",
			// The pane is refused unless the body can hold the figure's rows.
			Rows: []Row{c.row,
				{Label: "egress", Options: []string{"open", "allowlist"}},
				{Label: "credentials", Options: []string{"forward", "broker"}},
				{Label: "auth", Options: []string{"A", "B"}},
				{Label: "one", Options: []string{"a", "b"}},
				{Label: "two", Options: []string{"a", "b"}},
				{Label: "agent evidence", Options: []string{"default", "verbose"}}},
			Topology: func(*Form, int) *Frame {
				return &Frame{Host: "pluvo", HostOS: "(macOS)", Square: "sbx · x", Hop: "mitm", Interface: "interface",
					Caption: "cap", Lane: LaneWatched, Open: 1}
			},
		}
		w := paneWidth(f)
		// paneHeight, not a tall terminal: the pane is what a form falls back to
		// when the block will not fit, so a tall fixture draws the block instead.
		lay := f.layout(w, paneHeight)
		if lay.strip != stripPane {
			t.Fatalf("%s: the fixture must draw the pane at %d", c.name, w)
		}
		rows := renderAt(t, f, 1, w, paneHeight)
		for _, y := range gutterBreaches(rows, lay.pane) {
			t.Errorf("%s: row %d reached into the pane's gutter:\n%q", c.name, y, rows[y])
		}
	}
}

// figureMark is the container's top-left corner — the one glyph every frame
// draws and nothing else on the screen does, so it is how a test finds the
// figure's first row without depending on a label that may be clipped.
func figureMark() string { return glyphsFor(GlyphsNerd).cornerTL }

// hopRow is the figure's third row, where the two outside nodes name themselves.
func hopRow(joined, corner string) string {
	lines := strings.Split(joined, "\n")
	for i, l := range lines {
		if strings.Contains(l, corner) && i+2 < len(lines) {
			return lines[i+2]
		}
	}
	return ""
}

// The pane is a HEIGHT fallback, never a preference.
// SPEC: _spec/internal/choiceui/topology-strip.puml
func TestTheBlockWinsWheneverItFits(t *testing.T) {
	t.Parallel()
	f := tallForm()
	f.Rows[0].Locked = true
	f.Rows[0].Reason = "host-wide, not per-run — to change, run on the host: `sbx policy reset && sbx policy init deny-all`"

	w := paneWidth(f) + 60 // ample margin: the pane would fit at every height
	if got := f.layout(w, paneHeight).strip; got != stripPane {
		t.Fatalf("at h=%d the block cannot fit, so the pane should carry it; got strip=%d", paneHeight, got)
	}
	for _, h := range []int{40, 50, 60} {
		if got := f.layout(w, h).strip; got != stripBlock {
			t.Errorf("h=%d: the full figure fits, so it must be the block; got strip=%d", h, got)
		}
	}
	// And the figure really is below the form there, not beside it.
	rows := renderAt(t, f, 1, w, 44)
	hint, figure := -1, -1
	for i, l := range rows {
		switch {
		case strings.Contains(l, "enter accept"):
			hint = i
		case strings.Contains(l, "sbx · claudecode"):
			figure = i
		}
	}
	if hint < 0 || figure < 0 || figure < hint {
		t.Errorf("the figure must be drawn below the hint (hint=%d figure=%d)", hint, figure)
	}
}
