// SPEC: _spec/internal/choiceui/flock.puml
package choiceui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestFlockSameTickSameSky(t *testing.T) {
	t.Parallel()
	a, b := newFlockSim(72), newFlockSim(72)
	a.advance(30)
	b.advance(30)
	fa, fb := a.frame(GlyphsNerd), b.frame(GlyphsNerd)
	if len(fa) != flockRows || len(fb) != flockRows {
		t.Fatalf("frame rows = %d/%d, want %d", len(fa), len(fb), flockRows)
	}
	for i := range fa {
		if fa[i] != fb[i] {
			t.Errorf("row %d differs between identical sims:\n%q\n%q", i, fa[i], fb[i])
		}
	}
	if !flockPainted(fa) {
		t.Errorf("frame paints no braille dots: %q", fa)
	}
}

func flockPainted(rows []string) bool {
	for _, row := range rows {
		for _, r := range row {
			if r > 0x2800 && r <= 0x28FF {
				return true
			}
		}
	}
	return false
}

func TestFlockMoves(t *testing.T) {
	t.Parallel()
	s := newFlockSim(72)
	s.advance(1)
	before := s.frame(GlyphsNerd)
	s.advance(31)
	after := s.frame(GlyphsNerd)
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		t.Error("flock did not move between tick 1 and tick 31")
	}
}

func TestFlockAdvanceIsCapped(t *testing.T) {
	t.Parallel()
	capped, exact := newFlockSim(72), newFlockSim(72)
	capped.advance(1000)
	exact.advance(flockMaxDelta)
	cf, ef := capped.frame(GlyphsNerd), exact.frame(GlyphsNerd)
	for i := range cf {
		if cf[i] != ef[i] {
			t.Errorf("row %d: a stalled wake fast-forwarded the flock", i)
			break
		}
	}
}

func TestFlockStaysInBoundsAtBandSpeed(t *testing.T) {
	t.Parallel()
	s := newFlockSim(72)
	horizon := int(math.Round(500 / flockStep))
	for tick := 1; tick <= horizon; tick++ {
		s.advance(tick)
	}
	for i, b := range append(append([]bird{}, s.birds...), s.hawk) {
		if b.x < 0 || b.x > float64(s.dotsW-1) || b.y < 0 || b.y > float64(s.dotsH-1) {
			t.Errorf("bird %d out of bounds at (%v,%v) in %dx%d", i, b.x, b.y, s.dotsW, s.dotsH)
		}
		if b.speed < flockMinSpeed-1e-9 || b.speed > flockMaxSpeed+1e-9 {
			t.Errorf("bird %d speed %v outside [%v,%v]", i, b.speed, flockMinSpeed, flockMaxSpeed)
		}
	}
}

func TestFlockPictureChangesMostFrames(t *testing.T) {
	t.Parallel()
	s := newFlockSim(80)
	prev := ""
	changes := 0
	const frames = 60
	for tick := 1; tick <= frames; tick++ {
		s.advance(tick)
		got := strings.Join(s.frame(GlyphsNerd), "\n")
		if got != prev {
			changes++
			prev = got
		}
	}
	if changes < frames*2/3 {
		t.Errorf("picture changed on %d of %d frames", changes, frames)
	}
}

func TestFlockPaintKeepsTheSixtyHertzBudget(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 60)
	start := time.Now()
	for i := 1; i <= 60; i++ {
		f.flockTick = i
		f.draw(s, 0, 1)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Fatalf("60 paints took %s", d)
	}
}

func TestFlockFrameMatchesTheBrailleClip(t *testing.T) {
	t.Parallel()
	if time.Second/flockFrame != 30 {
		t.Fatalf("flock frame %s, want 1/30s", flockFrame)
	}
	if flockMaxDelta != 1 {
		t.Fatalf("catch-up is %d steps, want one step per paint", flockMaxDelta)
	}
}

func TestFlockEightFacings(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for bin := 0; bin < flockFacings; bin++ {
		s := newFlockSim(72)
		s.hawk.x, s.hawk.y = -30, -30
		s.birds = []bird{{x: 40, y: 8, heading: float64(bin) * math.Pi / 4, speed: flockMinSpeed}}
		sky := strings.Join(s.frame(GlyphsNerd), "\n")
		if seen[sky] {
			t.Errorf("facing %d repeats an earlier stamp", bin)
		}
		seen[sky] = true
		if n := flockDots(s.frame(GlyphsNerd)); n != 5 {
			t.Errorf("facing %d lights %d dots, want 5", bin, n)
		}
	}
}

func TestFlockBodySlidesBetweenDots(t *testing.T) {
	t.Parallel()
	s := newFlockSim(72)
	s.hawk.x, s.hawk.y = -30, -30
	s.birds = []bird{{x: 40, y: 8, heading: 0, speed: flockMinSpeed}}
	before := strings.Join(s.frame(GlyphsNerd), "\n")
	s.birds[0].x = 40.7
	after := strings.Join(s.frame(GlyphsNerd), "\n")
	if before == after {
		t.Error("a fraction of a dot left the stamp unchanged")
	}
}

func TestFlockPartsAroundTheHawk(t *testing.T) {
	t.Parallel()
	s := newFlockSim(40)
	s.birds = []bird{{x: 20, y: 8, heading: 0, speed: flockMinSpeed}}
	s.hawk = bird{x: 24, y: 8, heading: math.Pi, speed: flockMaxSpeed}
	s.step()
	if s.birds[0].heading <= 0 {
		t.Errorf("heading %v still points at the hawk", s.birds[0].heading)
	}
	if flockAngleBetween(s.birds[0].heading, 0) > flockTurn*flockStep+1e-9 {
		t.Errorf("turned %v, limit %v", s.birds[0].heading, flockTurn*flockStep)
	}
}

func TestFlockStaysWithinFortyColumns(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	rows := renderAt(t, f, 0, 140, 60)
	for y, row := range rows {
		for x, r := range []rune(row) {
			if r > 0x2800 && r <= 0x28FF && x >= 2+flockCols {
				t.Errorf("braille at row %d col %d, past the 40-column flock", y, x)
			}
		}
	}
}

func TestFlockFarPlaneLags(t *testing.T) {
	t.Parallel()
	s := newFlockSim(40)
	s.hawk.x, s.hawk.y = -30, -30
	s.birds = []bird{
		{x: 20, y: 8, heading: 0, speed: flockMinSpeed},
		{x: 20, y: 8, heading: 0, speed: flockMinSpeed, far: true},
	}
	s.step()
	if s.birds[1].x >= s.birds[0].x {
		t.Errorf("far bird at %v, near bird at %v", s.birds[1].x, s.birds[0].x)
	}
}

func TestFlockFarPlaneIgnoresTheHawk(t *testing.T) {
	t.Parallel()
	s := newFlockSim(40)
	s.birds = []bird{{x: 20, y: 8, heading: 0, speed: flockMinSpeed, far: true}}
	s.hawk = bird{x: 24, y: 8, heading: math.Pi, speed: flockMaxSpeed}
	s.step()
	if flockAngleBetween(s.birds[0].heading, 0) > 1e-9 {
		t.Errorf("far heading %v turned for the hawk", s.birds[0].heading)
	}
}

func TestFlockPlanesKeepToThemselves(t *testing.T) {
	t.Parallel()
	s := newFlockSim(40)
	s.hawk.x, s.hawk.y = -30, -30
	s.birds = []bird{
		{x: 20, y: 8, heading: 0, speed: flockMinSpeed, far: true},
		{x: 22, y: 8, heading: math.Pi, speed: flockMinSpeed},
	}
	s.step()
	if flockAngleBetween(s.birds[0].heading, 0) > 1e-9 {
		t.Errorf("far heading %v followed the near bird", s.birds[0].heading)
	}
}

func TestFlockNearPlaneCoversTheFar(t *testing.T) {
	t.Parallel()
	s := newFlockSim(72)
	s.hawk.x, s.hawk.y = -30, -30
	s.birds = []bird{
		{x: 20, y: 8, heading: 0, speed: flockMinSpeed, far: true},
		{x: 40, y: 8, heading: 0, speed: flockMinSpeed, far: true},
		{x: 40, y: 8, heading: 0, speed: flockMinSpeed},
	}
	_, who, _ := s.raster(GlyphsNerd)
	if who[2][10] != flockPlaneFar {
		t.Errorf("far bird cell is plane %d", who[2][10])
	}
	if who[2][20] != flockPlaneNear {
		t.Errorf("shared cell is plane %d", who[2][20])
	}
}

func TestFlockHawkHasItsOwnColor(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 60)
	f.flockTick = 1
	f.draw(s, 0, 1)
	colors := map[tcell.Color]bool{}
	w, h := s.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, style, _ := s.GetContent(x, y)
			if r <= 0x2800 || r > 0x28FF {
				continue
			}
			fg, _, _ := style.Decompose()
			colors[fg] = true
		}
	}
	for _, want := range []tcell.Color{
		tcell.NewHexColor(int32(ui.ColorSecondary)),
		tcell.NewHexColor(int32(ui.ColorAccent)),
		tcell.NewHexColor(int32(ui.ColorWarn)),
	} {
		if !colors[want] {
			t.Errorf("braille missing %v; have %v", want, colors)
		}
	}
}

func TestFlockEdgeBanksTheHeading(t *testing.T) {
	t.Parallel()
	s := newFlockSim(40)
	s.birds = []bird{{x: 1, y: 8, heading: math.Pi, speed: flockMinSpeed}}
	before := s.birds[0].heading
	s.step()
	turned := flockAngleBetween(before, s.birds[0].heading)
	limit := flockTurn * flockStep
	if turned > limit+1e-9 {
		t.Errorf("turned %v, limit %v", turned, limit)
	}
	if turned < 1e-6 {
		t.Error("the edge left the heading unchanged")
	}
	if flockAngleBetween(s.birds[0].heading, 0) < math.Pi/2 {
		t.Error("the heading snapped inward")
	}
}

func flockDots(rows []string) int {
	n := 0
	for _, row := range rows {
		for _, r := range row {
			if r <= 0x2800 || r > 0x28FF {
				continue
			}
			for m := uint(r - 0x2800); m > 0; m >>= 1 {
				n += int(m & 1)
			}
		}
	}
	return n
}

func flockAngleBetween(a, b float64) float64 {
	d := math.Mod(b-a, 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	} else if d < -math.Pi {
		d += 2 * math.Pi
	}
	if d < 0 {
		return -d
	}
	return d
}

func TestFlockTiers(t *testing.T) {
	t.Parallel()
	s := newFlockSim(72)
	s.advance(10)
	if got := s.frame(GlyphsOff); got != nil {
		t.Errorf("GlyphsOff frame = %q, want nil", got)
	}
	for _, row := range s.frame(GlyphsASCII) {
		for _, r := range row {
			if r != ' ' && r != '.' && r != '*' {
				t.Errorf("ASCII frame carries %q, want only space, '.' and '*'", r)
			}
		}
	}
	if !flockPainted(s.frame(GlyphsNerd)) {
		t.Error("GlyphsNerd frame paints no braille")
	}
}

func TestFlockTakesTheBannersPlace(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	seen := false
	for h := 8; h <= 60; h++ {
		lay := f.layout(120, h)
		if !lay.flock {
			continue
		}
		seen = true
		if lay.banner {
			t.Errorf("h=%d: flock and banner both afforded; the flock replaces the banner", h)
		}
		if lay.strip == stripNone {
			t.Errorf("h=%d: flock afforded while the figure was dropped", h)
		}
		if lay.body < f.rowsHeight() {
			t.Errorf("h=%d: flock bought with a body line (%d of %d)", h, lay.body, f.rowsHeight())
		}
	}
	if !seen {
		t.Fatal("no height afforded the flock, so nothing above was checked")
	}
	plain := budgetForm()
	for h := 8; h <= 60; h++ {
		a, b := f.layout(120, h), plain.layout(120, h)
		if a.flock && b.banner {
			return
		}
	}
	t.Error("no height shows the flock where the plain form shows its banner")
}

func TestFlockOffTierCostsNoRows(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	f.Glyphs = GlyphsOff
	if f.layout(120, 60).flock {
		t.Error("GlyphsOff afforded a flock region")
	}
}

func TestFlockRendersInsideTheForm(t *testing.T) {
	t.Parallel()
	f := budgetForm()
	f.Flock = true
	rows := renderAt(t, f, 0, 120, 60)
	if !flockPainted(rows) {
		t.Error("a tall terminal with Form.Flock paints no braille flock")
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "proveo/sandbox: portable, safe AI coding agents") {
		t.Error("the flock header lost the product tagline")
	}
	if strings.Contains(joined, "pr●veo") {
		t.Error("the flock header still carries the wordmark")
	}
}
