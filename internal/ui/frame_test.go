// SPEC: _spec/internal/ui/step-lines.puml (THE HAND-OFF FRAME)
package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/termtext"
)

// closed is a frame line that at least two rules close.
var closed = regexp.MustCompile(` ─{2,}$`)

// THE HAND-OFF FRAME: the line shortens in a fixed order and never wraps.
func TestAFrameLineShortensBeforeItWraps(t *testing.T) {
	t.Parallel()
	login := []string{"Docker not signed in", "sbx's screen", "page 1/4 returns when it exits"}
	for _, c := range []struct {
		w    int
		head string
	}{
		{160, "── sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits ─"},
		{120, "── sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits ─"},
		{81, "── sbx login ── Docker not signed in · sbx's screen · page 1/4 returns on exit ──"},
		{80, "── sbx login ── sbx's screen · page 1/4 returns on exit ─"},
		{60, "── sbx login ── sbx's screen · page 1/4 returns on exit ─"},
		{55, "── sbx l… ── sbx's screen · page 1/4 returns on exit ──"},
		{50, "── s… ── sbx's screen · page 1/4 returns on ex… ──"},
		{40, "── s… ── sbx's screen · page 1/4 ret… ──"},
		{0, "── sbx login ── sbx's screen · page 1/4 returns on exit ─"},
	} {
		got := FrameLine(c.w, "sbx login", login...)
		want := min(c.w, frameMax)
		if c.w <= 0 {
			want = frameFallback
		}
		if n := textWidthOf(got); n != want {
			t.Errorf("w=%d: the line is %d columns, want %d:\n%s", c.w, n, want, got)
		}
		if !strings.HasPrefix(got, c.head) {
			t.Errorf("w=%d:\n got %s\nwant %s…", c.w, got, c.head)
		}
		if !closed.MatchString(got) {
			t.Errorf("w=%d: fewer than 2 rules close the line: %q", c.w, got)
		}
	}
	if got := FrameLine(120, "sbx setup", "2 env keys sbx's store lacks", "sbx's screen", "init ends when it exits"); !strings.HasPrefix(got,
		"── sbx setup ── 2 env keys sbx's store lacks · sbx's screen · init ends when it exits ──") {
		t.Errorf("the setup frame at 120 = %q", got)
	}
	if got := FrameLine(80, "sbx setup", "2 env keys sbx's store lacks", "sbx's screen", "init ends when it exits"); got !=
		"── sbx setup ── sbx's screen · init ends on exit "+strings.Repeat("─", 31) {
		t.Errorf("the setup frame at 80 = %q", got)
	}
	if got := FrameLine(80, "sbx login"); got != "── sbx login ── "+strings.Repeat("─", 64) {
		t.Errorf("a frame with no parts = %q", got)
	}
	for w := 36; w <= 140; w++ {
		for _, got := range []string{
			FrameLine(w, "sbx login", "Docker not signed in", "sbx's screen", "init resumes when it exits"),
			FrameLine(w, "sbx setup", "2 env keys sbx's store lacks", "sbx's screen", "init ends when it exits"),
		} {
			if strings.Contains(got, " …") || textWidthOf(got) != min(w, frameMax) {
				t.Errorf("w=%d: a cut keeps its trailing space or the width drifts: %q", w, got)
			}
		}
	}
}

// Plain output prints the frame's characters with no escapes, after one blank row.
func TestAPlainFrameCarriesNoEscapes(t *testing.T) {
	var out bytes.Buffer
	p := New(&out)
	p.Tier = GlyphsSafe
	p.Section(SectionResults)
	p.Frame("sbx setup", "sbx's screen", "init ends when it exits")
	p.Blank()
	got := out.String()
	if strings.Contains(got, "\x1b") {
		t.Errorf("plain frame carries an escape: %q", got)
	}
	want := "\n  ------ results ------\n\n" + FrameLine(frameFallback, "sbx setup", "sbx's screen", "init ends when it exits") + "\n\n"
	if got != want {
		t.Errorf("plain frame:\n got %q\nwant %q", got, want)
	}
	var fancy bytes.Buffer
	term := NewTerminal(&fancy, 80)
	term.Tier = GlyphsSafe
	term.Frame("sbx login", "sbx's screen", "init resumes when it exits")
	if line := strings.TrimSpace(termtext.StripANSI(fancy.String())); line != FrameLine(80, "sbx login", "sbx's screen", "init resumes when it exits") {
		t.Errorf("the painted frame reads %q", line)
	}
}

// Lead heads an output: no blank row above it as the printer's first line, one under a line it
// printed before; a live line, which the terminal loses, does not count.
func TestALeadFrameTakesABlankRowOnlyAfterALine(t *testing.T) {
	var out bytes.Buffer
	p := New(&out)
	p.Lead("proveo init", "darwin/arm64")
	if got, want := out.String(), FrameLine(frameFallback, "proveo init", "darwin/arm64")+"\n"; got != want {
		t.Errorf("a first Lead = %q, want %q", got, want)
	}
	var term bytes.Buffer
	tp := NewTerminal(&term, 80)
	tp.Live(RoleAsync, "step 1/2 · install sbx")
	tp.EndLive()
	tp.Lead("proveo init")
	if got := termtext.StripANSI(term.String()); strings.Contains(got, "\n─") || strings.HasPrefix(got, "\n") {
		t.Errorf("a Lead after a live line alone took a blank row: %q", got)
	}
	out.Reset()
	p.Asyncf("step 1/2 · install sbx")
	p.Lead("proveo init")
	if got := out.String(); !strings.HasSuffix(got, "\n\n"+FrameLine(frameFallback, "proveo init")+"\n") {
		t.Errorf("a Lead under a line took no blank row: %q", got)
	}
}
