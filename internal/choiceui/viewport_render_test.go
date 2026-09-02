package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// tallForm is the shape the viewport exists for: more rows than a small
// terminal can hold, with dividers so lines and rows are not the same thing.
func tallForm() *Form {
	f := &Form{
		Banner: Banner(),
		Title:  "run claudecode — confirm or change this run",
		Header: []string{"git:  monorepo on fix-tools", "keys: ANTHROPIC_API_KEY", "llms: main=claude-opus-5"},
		Rows: []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}},
			{Label: "credentials", Options: []string{"forward", "broker"}},
			{Label: "auth", Options: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}},
			{Label: "execution", Options: []string{"host", "docker (sandbox)"}, Multi: true, Divider: true,
				Help: map[string]string{"docker (sandbox)": "a microVM with its own Docker daemon (sbx), and enough further text to wrap this help across more than one line of the reserved slot"}},
			{Label: "interface", Options: []string{"tui", "browser", "chrome"}, Multi: true, Divider: true},
			{Label: "agent evidence", Options: []string{"default", "verbose"}, Multi: true},
		},
	}
	f.Topology = func(*Form, int) *Frame {
		return &Frame{Square: "sbx · claudecode", Hop: "sbx proxy", Interface: "interface",
			Caption: "deny-all · broker — only proveo's allowlist gets out", Lane: LaneScreened, Open: 1, Refused: 2}
	}
	return f
}

// The failure this replaces: draw() painted straight down and tcell discarded
// the overflow, so a short terminal silently lost the hint and the help — the
// two things telling the operator what the keys do and what the option means.
func TestTheFootSurvivesEveryHeight(t *testing.T) {
	t.Parallel()
	for _, h := range []int{12, 16, 20, 24, 30, 40} {
		f := tallForm()
		rows := renderAt(t, f, 1, 80, h)
		joined := strings.Join(rows, "\n")
		if !strings.Contains(joined, "enter accept") {
			t.Errorf("h=%d: the hint was lost\n%s", h, joined)
		}
		hint := -1
		for y, line := range rows {
			if strings.Contains(line, "enter accept") {
				hint = y
			}
		}
		if hint < 0 {
			continue
		}
		// Nothing from the body may appear at or below the hint: a scrolled-off
		// row painting over the foot is the silent failure being ended here.
		for _, cursor := range []int{0, 2, 5} {
			probe := tallForm()
			pr := renderAt(t, probe, cursor, 80, h)
			ph := -1
			for y, line := range pr {
				if strings.Contains(line, "enter accept") {
					ph = y
				}
			}
			if ph < 0 {
				t.Errorf("h=%d cursor=%d: the hint was lost", h, cursor)
				continue
			}
			for y := ph; y < len(pr); y++ {
				for _, r := range probe.Rows {
					// Every row, at several cursors, so the scrolled-off ones are
					// the ones actually tested against the clip.
					if strings.Contains(pr[y], "  "+r.Label+" ") || strings.Contains(pr[y], "› "+r.Label+" ") {
						t.Errorf("h=%d cursor=%d: body row %q painted over the foot at %d",
							h, cursor, r.Label, y)
					}
				}
			}
		}
	}
}

// Whatever the height and wherever the cursor, the row it is on is painted.
func TestTheCursorsRowIsAlwaysPainted(t *testing.T) {
	t.Parallel()
	for h := 8; h <= 40; h++ {
		for cursor := range tallForm().Rows {
			f := tallForm()
			rows := renderAt(t, f, cursor, 80, h)
			joined := strings.Join(rows, "\n")
			if strings.Contains(joined, "terminal too small") {
				continue // it said so, loudly, which is the contract down here
			}
			// The cursor MARKER plus the row's last option: the marker only ever
			// appears on the cursor's own row, and matching bare option text let
			// two cursors pass on strings the header and the figure also paint
			// ("ANTHROPIC_API_KEY" is in the header, "host" is in the figure).
			opts := f.Rows[cursor].Options
			hint := len(rows)
			for y, line := range rows {
				if strings.Contains(line, "enter accept") {
					hint = y
				}
			}
			marked := false
			// Above the hint only: the help block below it also opens with "› "
			// and quotes the same option text. And matched with Contains rather
			// than a prefix, because the gutter glyph now precedes the marker.
			for _, line := range rows[:hint] {
				if strings.Contains(line, "› ") && strings.Contains(line, opts[len(opts)-1]) {
					marked = true
				}
			}
			if !marked {
				t.Errorf("h=%d cursor=%d: the cursor's row %q is not on screen\n%s",
					h, cursor, f.Rows[cursor].Label, joined)
			}
		}
	}
}

// A body that fits must render exactly as it did before the viewport existed:
// no offset, no gutter, and the foot following the body rather than pinned to
// the bottom edge.
func TestAFittingBodyScrollsNothingAndDrawsNoGutter(t *testing.T) {
	t.Parallel()
	f := tallForm()
	lay := f.layout(80, 60)
	if lay.body < f.rowsHeight() {
		t.Fatalf("60 rows should hold the whole body: %d of %d", lay.body, f.rowsHeight())
	}
	rows := renderAt(t, f, 1, 80, 60)
	if f.scroll != 0 {
		t.Errorf("a body that fits must sit at offset 0, got %d", f.scroll)
	}
	for _, line := range rows {
		for _, g := range []string{"▲", "▼", "░"} {
			if strings.HasPrefix(line, g) {
				t.Errorf("a body that fits must draw no gutter, found %q in %q", g, line)
			}
		}
	}
}

// When it does not fit, the gutter says so — and says how much is left.
func TestAScrollingBodyMarksItsTravel(t *testing.T) {
	t.Parallel()
	// Derived, not guessed: the height at which this fixture scrolls is a
	// property of the budget, and the budget moves whenever a region's cost does.
	h := scrollingHeight(t, tallForm())
	f := tallForm()
	rows := renderAt(t, f, 0, 80, h)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "more below") {
		t.Errorf("the hint must say how much is off screen:\n%s", joined)
	}
	gut := false
	for _, line := range rows {
		if strings.HasPrefix(line, "▼") || strings.HasPrefix(line, "░") {
			gut = true
		}
	}
	if !gut {
		t.Errorf("a scrolling body must draw its gutter:\n%s", joined)
	}
	// At the far end there is nothing below, and the hint stops claiming there is.
	end := tallForm()
	tail := strings.Join(renderAt(t, end, len(end.Rows)-1, 80, h), "\n")
	if strings.Contains(tail, "more below") {
		t.Errorf("at the end of travel nothing is below:\n%s", tail)
	}
}

// Below the floor the prompt says so instead of painting a broken form, and it
// still says how to leave: a terminal too small to draw the form must never be
// a terminal the operator cannot escape.
func TestBelowTheFloorTheFailureIsLoud(t *testing.T) {
	t.Parallel()
	joined := strings.Join(renderAt(t, tallForm(), 0, 80, 6), "\n")
	if !strings.Contains(joined, "terminal too small") {
		t.Errorf("a terminal under the floor must say so:\n%s", joined)
	}
	if !strings.Contains(joined, "esc cancels") {
		t.Errorf("it must still say how to leave:\n%s", joined)
	}
}

// The offset is a cache, so a second paint at a different size must re-clamp it
// rather than inherit an offset that no longer makes sense.
func TestASecondPaintDoesNotInheritAStaleOffset(t *testing.T) {
	t.Parallel()
	f := tallForm()
	renderAt(t, f, len(f.Rows)-1, 80, 14) // scrolled to the end
	if f.scroll == 0 {
		t.Fatal("the fixture should have scrolled")
	}
	rows := renderAt(t, f, 0, 80, 60) // now everything fits
	if f.scroll != 0 {
		t.Errorf("a body that now fits must return to 0, got %d", f.scroll)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "› egress") {
		t.Error("the cursor's row is missing after the resize")
	}
}

// A group's checkboxes with no name at all is worse than the duplication the
// divider exists to avoid, so the label returns when its heading scrolls away.
func TestADividerRowRegainsItsLabelWhenTheHeadingScrollsOff(t *testing.T) {
	t.Parallel()
	// Driven to a state where the heading really is off screen, rather than
	// skipping when it happens not to be: the previous version of this test
	// always skipped and covered neither branch.
	f := tallForm()
	lines := f.bodyLines()
	from, to := rowSpan(lines, 3)
	var joined string
	for h := 40; h >= 8; h-- {
		lay := f.layout(80, h)
		if lay.tooSmall || lay.body >= to-from {
			continue // the window can still hold the whole group
		}
		probe := tallForm()
		joined = strings.Join(renderAt(t, probe, 3, 80, h), "\n")
		if !strings.Contains(joined, "────── execution ──────") {
			break
		}
		joined = ""
	}
	if joined == "" {
		t.Fatal("never reached a height with the heading off screen; the test proves nothing")
	}
	if !strings.Contains(joined, "› execution") {
		t.Errorf("with its heading off screen the row must name itself:\n%s", joined)
	}
	// ...and it does not say it twice when the heading IS visible.
	tall := strings.Join(renderAt(t, tallForm(), 3, 80, 60), "\n")
	if strings.Contains(tall, "────── execution ──────") && strings.Contains(tall, "› execution ") {
		t.Errorf("the name must not appear both on the heading and the row:\n%s", tall)
	}
}

// scrollingHeight is the tallest terminal at which this form still scrolls —
// the interesting height, and the one that moves whenever a region's cost does.
func scrollingHeight(t *testing.T, f *Form) int {
	t.Helper()
	for h := 40; h >= 8; h-- {
		lay := f.layout(80, h)
		if !lay.tooSmall && lay.body < f.rowsHeight() && lay.body >= minBodyLines {
			return h
		}
	}
	t.Fatal("this form never scrolls; the fixture is too short to test a viewport")
	return 0
}

// A screen too small to draw the form must not accept edits to the posture it
// cannot show. Accepting one unseen is bad enough; changing one unseen is worse
// than the truncation the viewport replaced.
func TestATooSmallScreenIsNotNavigable(t *testing.T) {
	t.Parallel()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	f := tallForm()
	s.SetSize(80, 6)
	if f.navigable(s) {
		t.Error("six rows cannot draw this form, so it must not be steerable")
	}
	s.SetSize(80, 40)
	if !f.navigable(s) {
		t.Error("forty rows draws it comfortably and must be steerable")
	}
}
