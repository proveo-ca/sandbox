package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func budgetForm() *Form {
	return &Form{
		Banner: Banner(),
		Title:  "run claudecode — confirm or change this run",
		Header: []string{"git: r", "keys: K", "tooling: t", "lsp: l", "subagents: 7", "hooks: h", "llms: m"},
		Rows: []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}},
			{Label: "credentials", Options: []string{"forward", "broker"}},
			{Label: "execution", Options: []string{"host", "docker (sandbox)"}, Multi: true, Divider: true,
				Help: map[string]string{"docker (sandbox)": "a microVM with its own Docker daemon, and a good deal more text so this option wraps to several lines of help"}},
			{Label: "agent evidence", Options: []string{"default", "verbose"}, Multi: true},
		},
		Topology: func(*Form, int) *Frame { return &Frame{Caption: "c"} },
	}
}

// The drop ladder is banner, figure, header, axis — the figure outranks the
// wordmark, because this prompt is about posture and the figure is the only
// thing on it that draws posture. Anything still drawn implies everything below
// it on the ladder is too. Asserted as an ordering rather than as a table of
// heights: the costs move whenever a row is added, and magic numbers would only
// record whatever the code did that day.
func TestLayoutObeysTheDropLadder(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	for h := 8; h <= 60; h++ {
		lay := f.layout(120, h)
		if lay.banner && lay.strip != stripBlock {
			t.Errorf("h=%d: the banner is dropped FIRST, so it cannot outlive the figure", h)
		}
		if lay.strip != stripNone && !lay.header {
			t.Errorf("h=%d: the figure is dropped before the header, so it cannot outlive it", h)
		}
		if lay.header && !lay.axis {
			t.Errorf("h=%d: the header is dropped before the axis legend", h)
		}
	}
}

// Growing the terminal never takes a region away.
func TestLayoutIsMonotonicInHeight(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	prev := f.layout(120, 8)
	for h := 9; h <= 60; h++ {
		lay := f.layout(120, h)
		for _, c := range []struct {
			name       string
			was, isNow bool
		}{
			{"banner", prev.banner, lay.banner},
			{"header", prev.header, lay.header},
			{"axis", prev.axis, lay.axis},
		} {
			if c.was && !c.isNow {
				t.Errorf("h=%d: %s disappeared when the terminal GREW", h, c.name)
			}
		}
		if prev.strip > lay.strip {
			t.Errorf("h=%d: the strip shrank when the terminal grew (%v -> %v)", h, prev.strip, lay.strip)
		}
		// The body too. Leaving it out of this loop is exactly how a decoration
		// came to be bought with rows the body was already using: growing the
		// terminal by one row bought the header and started the rows scrolling.
		if prev.body > lay.body {
			t.Errorf("h=%d: the body shrank when the terminal GREW (%d -> %d)", h, prev.body, lay.body)
		}
		prev = lay
	}
}

// The whole point of the budget, stated as a test: the strip is never what
// pushed the hint or the help off the screen. The mandatory regions are priced
// before any optional one, so whenever the figure is drawn there was room for
// everything that is not decoration.
//
// It MAY cost the banner, deliberately: the figure outranks the wordmark on a
// prompt about posture. What it may never cost is anything mandatory, which is
// why this asserts the floor rather than that nothing else moved.
func TestTheStripNeverEvictsTheHintOrHelp(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	for h := 8; h <= 60; h++ {
		lay := f.layout(120, h)
		if lay.strip == stripNone {
			continue
		}
		// The body's FLOOR, not its full height: the rows scroll now, so what the
		// budget guarantees is a navigable window rather than all of them.
		floor := 2 + lay.body + 2 + lay.helpSlot
		if lay.helpSlot > 0 {
			floor++
		}
		cost := digestRows
		if lay.strip == stripBlock {
			cost = stripRows
		}
		if floor+cost > h {
			t.Errorf("h=%d: the strip (%d rows) was drawn over the mandatory %d", h, cost, floor)
		}
	}
	without := budgetForm()
	without.Topology = nil
	for h := 8; h <= 60; h++ {
		a, b := f.layout(120, h), without.layout(120, h)
		if a.helpSlot != b.helpSlot {
			t.Errorf("h=%d: the strip changed the reserved help slot, %d vs %d", h, a.helpSlot, b.helpSlot)
		}
		if a.header != b.header || a.axis != b.axis {
			t.Errorf("h=%d: the figure evicted the header or the axis legend", h)
		}
	}
}

// 72 columns is the figure's floor; below it the caption alone carries the same
// facts, so a narrow terminal loses the picture rather than the information.
func TestNarrowTerminalKeepsTheCaption(t *testing.T) {
	t.Parallel()
	if got := budgetForm().layout(70, 60).strip; got != stripDigest {
		t.Errorf("below %d columns the figure is replaced by its caption, got %v", stripCols, got)
	}
	// Somewhere between "nothing fits" and "everything fits" the digest is what
	// stands in for the figure. Asserted as existence rather than at one height,
	// because the exact row is a property of the fixture, not of the design.
	seen := false
	for h := 8; h <= 60; h++ {
		if budgetForm().layout(120, h).strip == stripDigest {
			seen = true
		}
	}
	if !seen {
		t.Error("no terminal height falls back to the one-line digest")
	}
}

// The slot is reserved at the tallest help this form can EVER show, not at the
// current one — reserving the current height would still jump the moment the
// cursor reached a wordier option.
func TestHelpSlotIsReservedAtTheMaximum(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	most := f.maxHelpLines(116)
	if most < 2 {
		t.Fatalf("the fixture's wordy option should wrap to several lines, got %d", most)
	}
	for i := range f.Rows {
		for j := range f.Rows[i].Options {
			f.Rows[i].Selected = j
			if n := len(f.Rows[i].helpLines(116)); n > most {
				t.Errorf("row %d option %d needs %d lines, more than the reserved %d", i, j, n, most)
			}
		}
	}
}

// A form with no projection asks for no strip, and must not be charged for one.
func TestNoTopologyCostsNoRows(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Topology = nil
	if got := f.layout(120, 60).strip; got != stripNone {
		t.Errorf("a nil projection must draw nothing, got %v", got)
	}
}

// The budget is only worth having if it predicts what draw() actually paints.
// This walks the real paint and checks the figure lands exactly where layout
// reserved it — the one bug class the arithmetic invites, and the one that would
// silently overwrite the help block.
func TestTheBudgetMatchesThePaint(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Topology = func(*Form, int) *Frame {
		return &Frame{Square: "sbx · claudecode", Hop: "sbx proxy", Interface: "interface",
			Caption: "CAPTION", Lane: LaneWatched, Open: 1}
	}
	for h := 20; h <= 60; h++ {
		lay := f.layout(120, h)
		if lay.strip != stripBlock {
			continue
		}
		rows := renderAt(t, f, 2, 120, h)
		hint, help, figure := -1, -1, -1
		for y, line := range rows {
			switch {
			case strings.Contains(line, "enter accept"):
				hint = y
			// BELOW the hint: the body's cursor marker is also "  › " now that the
			// gutter has pushed the body right, so the glyph alone no longer
			// distinguishes a help line from the row the cursor is on.
			case hint >= 0 && y > hint && strings.HasPrefix(line, "  › ") && help < 0:
				help = y
			case strings.Contains(line, "() host"):
				figure = y
			}
		}
		if hint < 0 || figure < 0 {
			t.Fatalf("h=%d: hint=%d figure=%d — the paint is missing a region", h, hint, figure)
		}
		if help >= 0 && help != hint+2 {
			t.Errorf("h=%d: help at %d, want one blank line under the hint at %d", h, help, hint+2)
		}
		// hint, blank, the reserved slot, the figure's own leading blank, then its
		// first lane row — the spine carrying "() host" is the SECOND figure row.
		if want := hint + 2 + lay.helpSlot + 2; figure != want {
			t.Errorf("h=%d: the figure painted at %d but the budget reserved %d", h, figure, want)
		}
		if caption := figure + 2; caption >= h {
			t.Errorf("h=%d: the figure's caption fell off the bottom, at row %d", h, caption)
		}
	}
}

// renderAt paints at an explicit size, keeping blank rows so positions are real.
func renderAt(t *testing.T, f *Form, cursor, w, h int) []string {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(w, h)
	f.draw(s, cursor, 0)
	cells, cw, ch := s.GetContents()
	out := make([]string, ch)
	for y := 0; y < ch; y++ {
		var b strings.Builder
		for x := 0; x < cw; x++ {
			b.WriteString(string(cells[y*cw+x].Runes))
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// A decoration may only ever be bought with rows that are genuinely spare.
// "What a short terminal loses is decoration" has to hold in both directions:
// no region above the body on the ladder may cost the body a single line.
func TestNoDecorationIsBoughtWithABodyLine(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	full := f.rowsHeight()
	for h := 8; h <= 60; h++ {
		lay := f.layout(120, h)
		if lay.tooSmall {
			continue
		}
		bought := lay.banner || lay.header || lay.axis || lay.strip != stripNone
		if bought && lay.body < full {
			t.Errorf("h=%d: the body scrolls (%d of %d) while a decoration was still afforded",
				h, lay.body, full)
		}
	}
}
