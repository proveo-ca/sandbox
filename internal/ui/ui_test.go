package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/google/go-cmp/cmp"
	"github.com/mattn/go-runewidth"
)

// Severity is the mark and role is the colour, in three tiers plus plain. The
// table is the vocabulary: a verb that renders differently from this row is a
// verb that has left the design language.
func TestPrinterVocabulary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		print     func(p *Printer)
		wantNerd  string
		wantASCII string
		wantOff   string
		wantPlain string
	}{
		{
			name:      "ok",
			print:     func(p *Printer) { p.Okf("added %s to PATH", "/bin") },
			wantNerd:  "✓ added /bin to PATH",
			wantASCII: "+ added /bin to PATH",
			wantOff:   "  added /bin to PATH",
			wantPlain: "ok: added /bin to PATH",
		},
		{
			name:      "warn",
			print:     func(p *Printer) { p.Warnf("%s not set", "CURSOR_API_KEY") },
			wantNerd:  "⚠ CURSOR_API_KEY not set",
			wantASCII: "! CURSOR_API_KEY not set",
			wantOff:   "  CURSOR_API_KEY not set",
			wantPlain: "warn: CURSOR_API_KEY not set",
		},
		{
			name:      "fail",
			print:     func(p *Printer) { p.Failf("unknown target %q", "nope") },
			wantNerd:  "× unknown target \"nope\"",
			wantASCII: "x unknown target \"nope\"",
			wantOff:   "  unknown target \"nope\"",
			wantPlain: "error: unknown target \"nope\"",
		},
		{
			name:      "note is a continuation: no marker, indented to the text column",
			print:     func(p *Printer) { p.Notef("restart your shell") },
			wantNerd:  "  restart your shell",
			wantASCII: "  restart your shell",
			wantOff:   "  restart your shell",
			wantPlain: "  restart your shell",
		},
		{
			name:      "host role",
			print:     func(p *Printer) { p.Hostf("proveo home: %s", "/Users/x/.proveo") },
			wantNerd:  "● proveo home: /Users/x/.proveo",
			wantASCII: "o proveo home: /Users/x/.proveo",
			wantOff:   "  proveo home: /Users/x/.proveo",
			wantPlain: "proveo home: /Users/x/.proveo",
		},
		{
			name:      "every role shares the dot; only the colour differs",
			print:     func(p *Printer) { p.Dangerf("removing image %s", "proveo/x:local") },
			wantNerd:  "● removing image proveo/x:local",
			wantASCII: "o removing image proveo/x:local",
			wantOff:   "  removing image proveo/x:local",
			wantPlain: "removing image proveo/x:local",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, tier := range []struct {
				name string
				tier GlyphTier
				want string
			}{
				{"nerd", GlyphsNerd, tc.wantNerd},
				{"ascii", GlyphsASCII, tc.wantASCII},
				{"off", GlyphsOff, tc.wantOff},
			} {
				var buf bytes.Buffer
				tc.print(&Printer{W: &buf, Tier: tier.tier})
				got := string(stripANSI(buf.Bytes()))
				if diff := cmp.Diff(tier.want+"\n", got); diff != "" {
					t.Errorf("%s mismatch (-want +got):\n%s", tier.name, diff)
				}
			}
			var buf bytes.Buffer
			tc.print(&Printer{W: &buf, Plain: true})
			if diff := cmp.Diff(tc.wantPlain+"\n", buf.String()); diff != "" {
				t.Errorf("plain mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// The mark is the whole reason this stream can measure itself, so its runes are
// constrained rather than merely chosen: one column each, and no variation
// selector. U+26A0 followed by U+FE0F is what the warn mark used to be, and
// go-runewidth calls that pair one column while every terminal draws two.
func TestMarksAreOneColumnAndCarryNoVariationSelector(t *testing.T) {
	t.Parallel()
	for _, tier := range []GlyphTier{GlyphsNerd, GlyphsASCII, GlyphsOff} {
		for _, s := range []sev{sevNone, sevOk, sevWarn, sevFail} {
			for _, r := range []Role{RoleNone, RoleHost, RoleApp, RoleAsync, RoleCloud, RoleStore, RoleError} {
				m := mark(tier, s, r)
				if got := runewidth.StringWidth(m); got != textCol {
					t.Errorf("tier %v sev %v role %v: mark %q is %d columns, want %d",
						tier, s, r, m, got, textCol)
				}
				for _, c := range m {
					if c >= 0xFE00 && c <= 0xFE0F {
						t.Errorf("tier %v sev %v role %v: mark %q carries variation selector U+%04X",
							tier, s, r, m, c)
					}
					if unicode.Is(unicode.Mn, c) || unicode.Is(unicode.Me, c) {
						t.Errorf("tier %v sev %v role %v: mark %q carries a combining rune U+%04X",
							tier, s, r, m, c)
					}
				}
			}
		}
	}
}

// Every role must resolve to a palette colour, or a call site can pick a role
// that prints undecorated and nothing says so.
func TestEveryRoleHasAColour(t *testing.T) {
	t.Parallel()
	for _, r := range []Role{RoleHost, RoleApp, RoleAsync, RoleCloud, RoleStore, RoleError} {
		if _, ok := r.color(); !ok {
			t.Errorf("role %v has no colour", r)
		}
	}
	if _, ok := RoleNone.color(); ok {
		t.Error("RoleNone must have no colour: it is a continuation, not a layer")
	}
}

// A role line is coloured; a plain one is not. This is the assertion the old
// package could not make, because line() emitted no ANSI at all.
func TestFancyLinesCarryTheRoleColour(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	(&Printer{W: &buf}).Hostf("relay on %s", "127.0.0.1:60409")
	if !strings.Contains(buf.String(), ANSI(ColorHost)) {
		t.Errorf("host line carries no host colour: %q", buf.String())
	}

	buf.Reset()
	(&Printer{W: &buf, Plain: true}).Hostf("relay on %s", "127.0.0.1:60409")
	if strings.Contains(buf.String(), "\033[") {
		t.Errorf("plain line carries ANSI: %q", buf.String())
	}
}

// The theme's own red and green, painting a text glyph. This is the assertion
// that replaces reaching for U+2705 / U+274C: the mark carries the shape and
// the colour carries the meaning, so nothing needs a rune with a colour in it.
func TestSeverityUsesTheThemesRedAndGreen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		print func(p *Printer)
		want  int
	}{
		{"ok is the identity's success green", func(p *Printer) { p.Okf("resolved") }, ColorSuccess},
		{"warn is the attention lime", func(p *Printer) { p.Warnf("policy is open") }, ColorWarn},
		{"fail is the identity's error red", func(p *Printer) { p.Failf("no such target") }, ColorFailure},
	} {
		var buf bytes.Buffer
		tc.print(&Printer{W: &buf})
		if !strings.Contains(buf.String(), ANSI(tc.want)) {
			t.Errorf("%s: want %#06x in %q", tc.name, tc.want, buf.String())
		}
	}
	// Green and red must be distinguishable from each other AND from the roles,
	// or the colour half of the design carries nothing.
	if ColorSuccess == ColorFailure {
		t.Error("success and failure are the same colour")
	}
	for _, r := range []Role{RoleHost, RoleApp, RoleAsync, RoleCloud, RoleStore} {
		if c, _ := r.color(); c == ColorSuccess {
			t.Errorf("role %v shares the success green, so a fact reads as a pass", r)
		}
	}
}

func TestGlyphTierFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want GlyphTier
	}{
		{"", GlyphsNerd},
		{"ascii", GlyphsASCII},
		{"ASCII", GlyphsASCII},
		{"  ascii  ", GlyphsASCII},
		{"off", GlyphsOff},
		{"none", GlyphsOff},
		{"0", GlyphsOff},
		// A typo degrades to the default rather than silently stripping the run.
		{"nerdfont", GlyphsNerd},
	} {
		got := GlyphTierFrom(func(string) string { return tc.in })
		if got != tc.want {
			t.Errorf("PROVEO_GLYPHS=%q: tier %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The defect that prompted the design pass: long lines broke mid-word, at
// column zero, because nothing measured them.
func TestWrapBreaksOnSpacesAndHangsAtTheTextColumn(t *testing.T) {
	t.Parallel()
	in := "● sbx's global network policy allows every host, so this run's Kit allowlist adds reach rather than limiting it\n"
	got := string(wrapLines([]byte(in), 60))
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("nothing wrapped at width 60:\n%s", got)
	}
	for i, l := range lines {
		if w := displayWidth([]byte(l)); w > 60 {
			t.Errorf("line %d is %d columns, over the 60 it was given: %q", i, w, l)
		}
		if i > 0 && !strings.HasPrefix(l, strings.Repeat(" ", textCol)) {
			t.Errorf("continuation %d does not hang at the text column: %q", i, l)
		}
	}
	// No word may be split: rejoining on whitespace must give the input back.
	if want, got := strings.Fields(in), strings.Fields(got); !cmp.Equal(want, got) {
		t.Errorf("wrapping changed the words (-want +got):\n%s", cmp.Diff(want, got))
	}
}

// A colour run costs bytes and no columns, so it must not be charged against
// the width — or a coloured line wraps earlier than an identical plain one.
func TestWrapDoesNotChargeForColour(t *testing.T) {
	t.Parallel()
	plain := "aaaa bbbb cccc dddd"
	painted := ANSI(ColorHost) + "aaaa" + ANSIReset + " bbbb cccc dddd"
	if displayWidth([]byte(painted)) != displayWidth([]byte(plain)) {
		t.Fatalf("colour counted: %d vs %d",
			displayWidth([]byte(painted)), displayWidth([]byte(plain)))
	}
	gotPlain := strings.Count(string(wrapLines([]byte(plain+"\n"), 12)), "\n")
	gotPainted := strings.Count(string(wrapLines([]byte(painted+"\n"), 12)), "\n")
	if gotPlain != gotPainted {
		t.Errorf("coloured line wrapped into %d rows, plain into %d", gotPainted, gotPlain)
	}
}

// A line that fits is returned untouched, byte for byte — wrapping must not be
// a reformatter that runs on every line whether or not it is needed.
func TestWrapLeavesShortLinesAlone(t *testing.T) {
	t.Parallel()
	in := []byte("● image: proveo/claudecode:local\n  a continuation\n")
	if got := wrapLines(in, 100); !bytes.Equal(in, got) {
		t.Errorf("short lines were rewritten:\nwant %q\ngot  %q", in, got)
	}
}

// The transcript keeps its long lines: wrapping is a terminal concern, so a
// grep over a run log cannot depend on how wide the operator's window was.
func TestTeeDoesNotWrapTheLog(t *testing.T) {
	var term, log bytes.Buffer
	saved, savedLogOnly := Default, logOnly
	t.Cleanup(func() { Default, logOnly = saved, savedLogOnly })

	Default = &Printer{W: &wrapWriter{w: &term, width: func() int { return 40 }}}
	TeeTo(&log)
	long := "sbx's global network policy allows every host, so this run's Kit allowlist adds reach rather than limiting it"
	Warnf("%s", long)

	if n := strings.Count(strings.TrimRight(term.String(), "\n"), "\n"); n == 0 {
		t.Errorf("the terminal copy did not wrap at 40 columns:\n%s", term.String())
	}
	if n := strings.Count(strings.TrimRight(log.String(), "\n"), "\n"); n != 0 {
		t.Errorf("the log copy was wrapped, so a grep over it now depends on window width:\n%s", log.String())
	}
	if !strings.Contains(log.String(), long) {
		t.Errorf("the log lost the unbroken sentence:\n%s", log.String())
	}
}

// New must degrade to plain mode for anything that is not a terminal: pipes,
// buffers, regular files. (The terminal=fancy side needs a real PTY, which unit
// tests don't have — it is exercised by the tmux-driven agent-E2E layer.)
func TestNewDetectsPlain(t *testing.T) {
	t.Run("non-file writer is plain", func(t *testing.T) {
		if p := New(&bytes.Buffer{}); !p.Plain {
			t.Error("New(bytes.Buffer) should be plain: a buffer is not a terminal")
		}
	})
	t.Run("regular file is plain", func(t *testing.T) {
		f, err := os.Create(filepath.Join(t.TempDir(), "out"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if p := New(f); !p.Plain {
			t.Error("New(regular file) should be plain: a file is not a terminal")
		}
	})
}

func TestWriteBrandBanner(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	WriteBrandBanner(&buf)
	got := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("pr●veo")) {
		t.Fatalf("banner missing pr●veo mark:\n%s", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("S O L U T I O N S")) {
		t.Fatalf("banner missing SOLUTIONS:\n%s", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte(BrandTagline)) {
		t.Fatalf("banner missing tagline:\n%s", got)
	}
}

// A heading must be drawn by the line that follows it, never by the call that
// declares it. Nearly every line in a prelude is conditional, so a stage that
// declares a section and finds nothing to say must leave no divider behind.
func TestSectionsAreLazy(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &Printer{W: &buf, Plain: true}

	p.Section(SectionCredentials) // nothing follows: must draw nothing
	if buf.Len() != 0 {
		t.Fatalf("declaring a section drew something: %q", buf.String())
	}

	p.Section(SectionEgress)
	p.Warnf("policy is open")
	got := buf.String()
	if !strings.Contains(got, "------ "+SectionEgress+" ------") {
		t.Errorf("the line did not draw its heading:\n%s", got)
	}
	if strings.Contains(got, SectionCredentials) {
		t.Errorf("a section nothing followed still drew a heading:\n%s", got)
	}
}

// Re-declaring the section on screen must be a no-op, so a stage can announce
// itself without first checking whether the previous one already did. This is
// what lets selectBackend continue the egress block the broker line opened.
func TestReDeclaringTheCurrentSectionDrawsNothing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &Printer{W: &buf, Plain: true}
	p.Section(SectionEgress)
	p.Warnf("broker off")
	p.Section(SectionEgress)
	p.Warnf("baseline is open")
	if n := strings.Count(buf.String(), "------ "+SectionEgress); n != 1 {
		t.Errorf("egress heading drawn %d times, want 1:\n%s", n, buf.String())
	}
}

// A known width runs the rule out to the edge; an unmeasurable writer gets
// choiceui's short form instead of a rule sized from a guess.
func TestSectionRuleFillsOnlyAMeasuredWidth(t *testing.T) {
	t.Parallel()
	wide := sectionHeading(GlyphsNerd, false, SectionRun, 80)
	if got := textWidthOf(wide); got != 80 {
		t.Errorf("a measured heading is %d columns, want 80: %q", got, wide)
	}
	short := sectionHeading(GlyphsNerd, false, SectionRun, 0)
	if textWidthOf(short) >= 80 {
		t.Errorf("an unmeasured heading ran to a guess: %q", short)
	}
	if !strings.HasSuffix(short, strings.Repeat("─", sectionRules)) {
		t.Errorf("the short form dropped choiceui's trailing run: %q", short)
	}
	// ASCII and plain both fall back to the rule a dumb terminal can draw.
	if !strings.Contains(sectionHeading(GlyphsASCII, false, SectionRun, 0), "------") {
		t.Error("the ascii tier drew a box-drawing rule")
	}
	if !strings.Contains(sectionHeading(GlyphsNerd, true, SectionRun, 0), "------") {
		t.Error("plain drew a box-drawing rule")
	}
}

// TeeTo swaps the writer mid-run, after the first lines are already out. The
// section state has to cross with it or the next line redraws a heading the
// operator has just read.
func TestTeeKeepsTheSectionState(t *testing.T) {
	var term, log bytes.Buffer
	saved, savedLogOnly := Default, logOnly
	t.Cleanup(func() { Default, logOnly = saved, savedLogOnly })

	Default = &Printer{W: &term, Plain: true}
	Section(SectionRun)
	Appf("image: proveo/claudecode:local")
	TeeTo(&log)
	Section(SectionRun) // the same section: must not redraw
	Storef("run log: /tmp/x.log")

	if n := strings.Count(term.String(), "------ "+SectionRun); n != 1 {
		t.Errorf("the run heading was drawn %d times across the tee, want 1:\n%s", n, term.String())
	}
}
