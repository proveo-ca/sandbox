// SPEC: _spec/internal/choiceui/flock.puml
package choiceui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
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
	for i, b := range s.birds {
		if b.x < 0 || b.x > float64(s.dotsW-1) || b.y < 0 || b.y > float64(s.dotsH-1) {
			t.Errorf("bird %d out of bounds at (%v,%v) in %dx%d", i, b.x, b.y, s.dotsW, s.dotsH)
		}
		if v := math.Hypot(b.vx, b.vy); v < flockMinSpeed-1e-9 || v > flockMaxSpeed+1e-9 {
			t.Errorf("bird %d speed %v outside [%v,%v]", i, v, flockMinSpeed, flockMaxSpeed)
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

func TestFlockFrameIsSixtyHertz(t *testing.T) {
	t.Parallel()
	if time.Second/flockFrame != 60 {
		t.Fatalf("flock frame %s, want 1/60s", flockFrame)
	}
	got := float64(60) * flockStep
	want := float64(time.Second) / float64(animFrame)
	if math.Abs(got-want) > 0.02 {
		t.Fatalf("60 flock steps cover %v strip frames, want %v", got, want)
	}
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
