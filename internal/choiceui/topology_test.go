// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
)

// paint renders one Frame on its own and returns the rows it touched.
func paint(t *testing.T, fr Frame, tier GlyphTier, tick int) []string {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 20)
	drawTopology(s, 0, fr, tier, styles(), tick)
	s.Show()

	cells, w, h := s.GetContents()
	out := make([]string, 0, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

func base() Frame {
	return Frame{Host: "pluvo", HostOS: "(macOS)", Square: "sbx · claudecode", Hop: "sbx proxy", Interface: "interface",
		Caption: "allow-all · broker — the hop watches", Key: KeyAtHop,
		Lane: LaneWatched, Open: 3}
}

// The figure occupies exactly the rows the layout reserved for it. A picture
// whose height moved with its content would shove whatever came after it.
func TestTopologyFitsItsReservedRows(t *testing.T) {
	t.Parallel()
	for _, fr := range []Frame{
		base(),
		func() Frame { f := base(); f.Lane, f.Open, f.Refused = LaneScreened, 1, 2; return f }(),
		func() Frame { f := base(); f.Lane, f.Open, f.Refused = LaneAsked, 0, 0; return f }(),
		func() Frame { f := base(); f.Hop = ""; f.Key = KeyInSquare; return f }(),
	} {
		rows := paint(t, fr, GlyphsNerd, 0)
		// Load-bearing: without this the assertions below pass on a blank screen.
		for _, y := range []int{1, 2, 3, 4, 5} {
			if rows[y] == "" {
				t.Fatalf("row %d of the figure is empty; nothing was drawn", y)
			}
		}
		for y := figureRows; y < len(rows); y++ {
			if rows[y] != "" {
				t.Errorf("the figure painted past its %d rows, at y=%d: %q", figureRows, y, rows[y])
			}
		}
	}
}

// The key is drawn at exactly one of its three homes — never at two, never at
// none. Two keys would say the credential is in both places at once.
func TestKeyAppearsAtExactlyOneHome(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	for _, c := range []struct {
		home KeyHome
		near string
	}{
		// The host's key rides beside its DOT on the spine; the host's name is on
		// the row beneath, as every node's now is.
		{KeyAtHost, g.node},
		{KeyInSquare, "claudecode"},
		{KeyAtHop, "sbx proxy"},
	} {
		fr := base()
		fr.Key = c.home
		joined := strings.Join(paint(t, fr, GlyphsNerd, 0), "\n")
		if n := strings.Count(joined, g.key); n != 1 {
			t.Errorf("home %v: key drawn %d times, want exactly 1", c.home, n)
		}
		line := ""
		for _, l := range strings.Split(joined, "\n") {
			if strings.Contains(l, g.key) {
				line = l
			}
		}
		if !strings.Contains(line, c.near) {
			t.Errorf("home %v: key is not beside %q, got %q", c.home, c.near, line)
		}
	}
}

// review is the only tier with somebody to ask, so it is the only one that
// draws a question walking back to the host.
func TestOnlyReviewRoutesTheQuestionBackToTheHost(t *testing.T) {
	t.Parallel()
	asked := base()
	asked.Lane, asked.Open, asked.Refused = LaneAsked, 0, 0
	rows := paint(t, asked, GlyphsNerd, 0)
	if !strings.Contains(rows[0], "?") || !strings.Contains(rows[0], "▴") {
		t.Errorf("review must draw the question returning to the host, got %q", rows[0])
	}
	if !strings.Contains(rows[0], "asks you") {
		t.Errorf("the return lane must name who is being asked, got %q", rows[0])
	}
	quiet := paint(t, base(), GlyphsNerd, 0)
	if strings.Contains(quiet[0], "?") {
		t.Errorf("a tier with nobody to ask must draw no question, got %q", quiet[0])
	}
}

// The strictest posture still reaches the provider: a frame with no lane at all
// would say the agent cannot think.
func TestSurvivingLanesAreDrawnAsClouds(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	for _, c := range []struct{ open, refused int }{{3, 0}, {2, 1}, {1, 2}} {
		fr := base()
		fr.Lane, fr.Open, fr.Refused = LaneScreened, c.open, c.refused
		joined := strings.Join(paint(t, fr, GlyphsNerd, 0), "\n")
		if n := strings.Count(joined, g.cloud); n != c.open {
			t.Errorf("open=%d: %d clouds, want %d", c.open, n, c.open)
		}
		if n := strings.Count(joined, g.refused); n != c.refused {
			t.Errorf("refused=%d: %d refusals, want %d", c.refused, n, c.refused)
		}
	}
}

// The ASCII tier is the one that is correct on every terminal, so it may not
// smuggle in a rune wider or narrower than one column.
func TestASCIITierIsOneColumnPerRune(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsASCII)
	for _, s := range []string{g.cloud, g.key, g.quiet, g.speaking, g.spine, g.screened, g.watched,
		g.refused, g.asking, g.east, g.west, g.north, g.up} {
		for _, r := range s {
			if r > 127 {
				t.Errorf("the ASCII tier carries a non-ASCII rune %q (U+%04X)", r, r)
			}
			if runewidth.RuneWidth(r) != 1 {
				t.Errorf("%q is %d columns; the ASCII tier must be one per rune", r, runewidth.RuneWidth(r))
			}
		}
	}
}

// Emoji were rejected for the terminal: every decorated glyph must be a single
// rune of a single column, which is what a devicon is and an emoji is not.
func TestNerdTierGlyphsAreSingleRuneSingleColumn(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	for _, s := range []string{g.cloud, g.key, g.quiet, g.speaking} {
		if n := len([]rune(s)); n != 1 {
			t.Errorf("%q is %d runes; a variation-selected emoji is exactly the hazard this avoids", s, n)
		}
		if w := runewidth.StringWidth(s); w != 1 {
			t.Errorf("%q measures %d columns, want 1", s, w)
		}
	}
}

// The focused row raises its own element and dims the rest; nothing appears or
// vanishes, so the figure never re-flows under a cursor that is only moving.
func TestFocusChangesEmphasisNotContent(t *testing.T) {
	t.Parallel()
	want := strings.Join(paint(t, base(), GlyphsNerd, 0), "\n")
	if !strings.Contains(want, "pluvo") || !strings.Contains(want, glyphsFor(GlyphsNerd).cornerTL) {
		t.Fatal("nothing was drawn, so comparing two blank renders proves nothing")
	}
	for _, f := range []Focus{FocusHop, FocusKey, FocusSquare, FocusReturn, FocusSay} {
		fr := base()
		fr.Focus = f
		if got := strings.Join(paint(t, fr, GlyphsNerd, 0), "\n"); got != want {
			t.Errorf("focus %v changed the CONTENT of the figure:\n got %q\nwant %q", f, got, want)
		}
	}
}

// The one shape with nothing in the path holds its columns open rather than
// closing them up, so the figure does not shift sideways between tiers.
func TestTheHoplessFrameKeepsItsColumns(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	fr := base()
	fr.Hop, fr.Key = "", KeyInSquare
	rows := paint(t, fr, GlyphsNerd, 0)
	if strings.Contains(strings.Join(rows, "\n"), "proxy") {
		t.Errorf("a hopless frame must draw no hop:\n%s", strings.Join(rows, "\n"))
	}
	withHop := paint(t, base(), GlyphsNerd, 0)
	if a, b := indexOf(rows[1], g.cornerTL), indexOf(withHop[1], g.cornerTL); a != b {
		t.Errorf("the container moved between hop and no-hop: col %d vs %d", a, b)
	}
}

func indexOf(s, sub string) int { return strings.Index(s, sub) }

func TestTheFigureDoesNotJumpWhenTheHelpChangesHeight(t *testing.T) {
	t.Parallel()
	f := &Form{
		Title: "run claudecode",
		Rows: []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced"},
				Help: map[string]string{"allow-all": "short"}},
			{Label: "credentials", Options: []string{"forward", "broker"},
				Help: map[string]string{"forward": "a much longer sentence about forwarding that is certain to wrap across at least three separate lines of the reserved help slot below the hint"}},
		},
		Topology: func(*Form, int) *Frame {
			return &Frame{Host: "pluvo", HostOS: "(macOS)", Square: "sbx · claudecode", Hop: "sbx proxy", Interface: "interface",
				Caption: "c", Lane: LaneWatched, Open: 1}
		},
	}
	at := func(cursor int) int {
		s := tcell.NewSimulationScreen("UTF-8")
		if err := s.Init(); err != nil {
			t.Fatal(err)
		}
		defer s.Fini()
		s.SetSize(120, 40)
		f.draw(s, cursor, 0)
		cells, w, h := s.GetContents()
		for y := 0; y < h; y++ {
			row := ""
			for x := 0; x < w; x++ {
				row += string(cells[y*w+x].Runes)
			}
			if strings.Contains(row, figureMark()) {
				return y
			}
		}
		return -1
	}
	short, tall := at(0), at(1)
	if short < 0 || tall < 0 {
		t.Fatalf("the figure was not drawn at all: %d / %d", short, tall)
	}
	if short != tall {
		t.Errorf("the figure moved from row %d to row %d as the help grew", short, tall)
	}
}

func simScreen(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	return s
}

func screenLines(s tcell.SimulationScreen) []string {
	cells, w, h := s.GetContents()
	out := make([]string, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func TestGlyphsOffDrawsTheASCIISet(t *testing.T) {
	t.Parallel()
	if glyphsFor(GlyphsOff) != glyphsFor(GlyphsASCII) {
		t.Error("GlyphsOff must draw the ASCII figure, not the nerd one")
	}
	if glyphsFor(GlyphsNerd) == glyphsFor(GlyphsASCII) {
		t.Error("nerd and ascii must differ, or there is only one tier")
	}
	// And through a paint, not only through the table: the figure must contain
	// no private-use rune when the operator has said the font has none.
	for _, row := range paint(t, Frame{Square: "sbx · claudecode", Hop: "sbx proxy", Open: 2}, GlyphsOff, 0) {
		for _, r := range row {
			if r >= 0xE000 && r <= 0xF8FF {
				t.Errorf("GlyphsOff painted a private-use rune U+%04X in %q", r, row)
			}
		}
	}
}

// pulses reports every traffic mote as (row, column), rune-indexed.
func pulses(rows []string) [][2]int {
	var out [][2]int
	for y, r := range rows {
		for x, c := range []rune(r) {
			if c == '•' {
				out = append(out, [2]int{y, x})
			}
		}
	}
	return out
}

func TestTheFirstMoteTravelsFromTheHopAlongTheSpine(t *testing.T) {
	t.Parallel()
	fr := base()
	fr.Lane, fr.Open, fr.Refused = LaneScreened, 2, 1

	from := blockCols.hop + 1
	spine := blockCols.lanes - from
	if spine <= 1 {
		t.Fatalf("spine is %d; the travel assertion needs a wire to ride", spine)
	}
	var seen []int
	for tick := 1; tick <= spine; tick++ {
		want := from + (tick - 1)
		found := false
		for _, p := range pulses(paint(t, fr, GlyphsNerd, tick)) {
			if p[0] == 2 && p[1] == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("tick %d: first mote missing at column %d", tick, want)
		}
		seen = append(seen, want)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("the mote went backwards on the wire: %v", seen)
			break
		}
	}
}

// Past the junction a mote takes ONE lane. It never stacks on every row of the
// same column — that was the old fan-out. On a limited hop it may ride a
// refused lane to the ×; that is the dead-end, not a leak.
func TestEachMoteTakesOneLane(t *testing.T) {
	t.Parallel()
	fr := base()
	fr.Lane, fr.Open, fr.Refused = LaneScreened, 2, 1

	reachedLane := false
	junction := blockCols.lanes
	for tick := 1; tick <= 80; tick++ {
		atCol := map[int]int{}
		for _, p := range pulses(paint(t, fr, GlyphsNerd, tick)) {
			if p[1] > junction {
				reachedLane = true
				atCol[p[1]]++
			}
		}
		for x, n := range atCol {
			if n > 1 {
				t.Errorf("tick %d: %d motes stacked at column %d; that is the old fan-out", tick, n, x)
			}
		}
	}
	if !reachedLane {
		t.Error("no mote ever reached the lanes; they stop at the junction")
	}
}

func TestLimitedHopSendsMotesIntoDeadEnds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ open, refused int }{{2, 1}, {1, 2}} {
		fr := base()
		fr.Lane, fr.Open, fr.Refused = LaneScreened, c.open, c.refused
		from := blockCols.hop + 1
		spine := blockCols.lanes - from
		hit := 0
		for tick := 1; tick <= 400 && hit == 0; tick++ {
			for _, m := range trafficMotes(tick, spine, blockCols.runLen, c.open, c.refused) {
				if m.lane >= c.open && m.pos >= spine {
					hit = tick
					break
				}
			}
		}
		if hit == 0 {
			t.Errorf("open=%d refused=%d: schedule never sent a mote to a dead-end", c.open, c.refused)
			continue
		}
		onDead := false
		junction := blockCols.lanes
		for _, p := range pulses(paint(t, fr, GlyphsNerd, hit)) {
			lane := p[0] - 1
			if lane >= c.open && p[1] > junction {
				onDead = true
			}
		}
		if !onDead {
			t.Errorf("open=%d refused=%d tick %d: a dead-end mote was scheduled but not painted", c.open, c.refused, hit)
		}
	}
}

func TestTrafficBurstSchedule(t *testing.T) {
	t.Parallel()
	if n := len(trafficMotes(1, 10, 3, 3, 0)); n != 1 {
		t.Errorf("tick 1: %d motes, want the first one alone", n)
	}
	max := 0
	for tick := 1; tick <= 40; tick++ {
		if n := len(trafficMotes(tick, 10, 3, 3, 0)); n > max {
			max = n
		}
	}
	if max < 2 {
		t.Error("a burst never put two motes on the wire at once")
	}
	var spawn time.Duration
	seenLane := [3]bool{}
	for n := 0; n < 40; n++ {
		if n > 0 {
			gap := trafficStep(n - 1)
			if n%trafficBurst == 0 {
				if gap != trafficCooldown {
					t.Errorf("after mote %d: cooldown %s, want %s", n-1, gap, trafficCooldown)
				}
			} else if gap < trafficGapMin || gap > trafficGapMax {
				t.Errorf("before mote %d: gap %s outside [%s, %s]", n, gap, trafficGapMin, trafficGapMax)
			}
			spawn += gap
		}
		lane := trafficLane(n, 3)
		if lane < 0 || lane > 2 {
			t.Errorf("mote %d: lane %d, want 0..2", n, lane)
		}
		seenLane[lane] = true
	}
	for i, ok := range seenLane {
		if !ok {
			t.Errorf("a 3-lane hop never chose lane %d (top/middle/bottom)", i)
		}
	}
	if spawn == 0 {
		t.Fatal("the schedule did not advance; bursts cannot start")
	}
}

// Bursts continue for as long as the prompt is open: far in the future there
// are still motes, rather than a still picture.
func TestTrafficBurstsKeepComing(t *testing.T) {
	t.Parallel()
	late := int((30*time.Second)/animFrame) + 1
	found := false
	for tick := late; tick < late+int(4*time.Second/animFrame); tick++ {
		if len(trafficMotes(tick, 10, 3, 3, 0)) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no motes in a 4s window starting at tick %d; the bursts have stopped", late)
	}
}

// A review tier asks the operator and opens nothing, so there is no traffic to
// draw at all.
func TestNoOpenLaneMeansNoTraffic(t *testing.T) {
	t.Parallel()
	fr := base()
	fr.Lane, fr.Open, fr.Refused = LaneAsked, 0, 0
	for tick := 1; tick <= 20; tick++ {
		if at := pulses(paint(t, fr, GlyphsNerd, tick)); len(at) != 0 {
			t.Fatalf("tick %d: traffic on a figure with no open lane: %v", tick, at)
		}
	}
}
