// SPEC: _spec/internal/ui/output-vocabulary.puml
package ui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/termtext"
)

// col is the display column where needle starts in line, or -1.
func col(line, needle string) int {
	i := strings.Index(line, needle)
	if i < 0 {
		return -1
	}
	return textWidthOf(line[:i])
}

func plainLines(s string) []string {
	return strings.Split(strings.TrimRight(termtext.StripANSI(s), "\n"), "\n")
}

func TestFactsAlignValuesInOneColumnAndKeepTheRoleDot(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &Printer{W: &buf, Tier: GlyphsSafe}
	p.Facts(
		Fact{Role: RoleStore, Key: "checkout", Value: "~/w/hotfix (linked worktree)", Detail: []string{"cloned from ~/w/main"}},
		Fact{Role: RoleStore, Key: ".env", Value: "~/w/main/.env (copy, rw)"},
		Fact{Value: "~/w/main/.env.local (copy, rw)"},
		Fact{Role: RoleHost, Key: "links", Value: "_spec → ~/Syncd/_spec"},
		Fact{Role: RoleHost, Key: "empty"},
	)
	lines := plainLines(buf.String())
	want := []string{
		"● checkout: ~/w/hotfix (linked worktree)",
		"            cloned from ~/w/main",
		"● .env:     ~/w/main/.env (copy, rw)",
		"            ~/w/main/.env.local (copy, rw)",
		"● links:    _spec → ~/Syncd/_spec",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Facts drew:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(buf.String(), ANSI(ColorDB)+"● ") || !strings.Contains(buf.String(), ANSI(ColorHost)+"● ") {
		t.Errorf("each keyed row must keep its role's dot colour:\n%q", buf.String())
	}
	if !strings.Contains(buf.String(), ANSI(ColorRule)+"cloned from") {
		t.Errorf("a detail line must be dim:\n%q", buf.String())
	}
}

func TestARowThatPrintsNothingWidensNoKeyColumn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	(&Printer{W: &buf, Plain: true}).Facts(
		Fact{Role: RoleStore, Key: "log", Value: "/var/log/run.log"},
		Fact{Role: RoleStore, Key: "scope"},
	)
	if got := buf.String(); got != "log: /var/log/run.log\n" {
		t.Errorf("Facts = %q, want the empty scope row to leave the log row unpadded", got)
	}
}

func TestPlainFactsAlignWithoutMarks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	(&Printer{W: &buf, Plain: true}).Facts(
		Fact{Role: RoleStore, Key: "log", Value: "/var/log/run.log"},
		Fact{Role: RoleApp, Key: "agent", Value: "claudecode 2.1.312 (pinned)", Detail: []string{"pinned at build"}},
	)
	want := "log:   /var/log/run.log\nagent: claudecode 2.1.312 (pinned)\n       pinned at build\n"
	if buf.String() != want {
		t.Errorf("plain Facts = %q, want %q", buf.String(), want)
	}
}

func TestAStackedValuesDetailSitsPastTheList(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	(&Printer{W: &buf, Plain: true}).Facts(
		Fact{Role: RoleHost, Key: "later", Value: "prompt"},
		Fact{Role: RoleHost, Value: "credentials", Detail: []string{"`proveo init` from a terminal provisions them"}},
		Fact{Role: RoleHost, Key: "name", Value: "ada", Detail: []string{"the default"}},
	)
	want := "later: prompt\n       credentials\n         `proveo init` from a terminal provisions them\nname:  ada\n       the default\n"
	if buf.String() != want {
		t.Errorf("plain Facts = %q, want %q", buf.String(), want)
	}
}

func TestAFactValueWrapsAtItsOwnColumnAndTheLogStaysOneLine(t *testing.T) {
	var term, log bytes.Buffer
	saved, savedLogOnly := Default, logOnly
	t.Cleanup(func() { Default, logOnly = saved, savedLogOnly })
	Default = &Printer{W: &wrapWriter{w: &term, width: func() int { return 40 }}, Tier: GlyphsSafe}
	TeeTo(&log)
	long := "one two three four five six seven eight nine ten eleven twelve"
	Facts(Fact{Role: RoleStore, Key: "checkout", Value: long})

	lines := plainLines(term.String())
	if len(lines) < 2 {
		t.Fatalf("a 62-column value did not wrap at 40 columns:\n%s", term.String())
	}
	valCol := col(lines[0], "one")
	for i, l := range lines[1:] {
		if lead := len(l) - len(strings.TrimLeft(l, " ")); lead != valCol {
			t.Errorf("continuation %d starts at column %d, want the value column %d: %q", i+1, lead, valCol, l)
		}
	}
	if n := strings.Count(strings.TrimRight(log.String(), "\n"), "\n"); n != 0 || !strings.Contains(log.String(), long) {
		t.Errorf("the log copy must keep the fact on one line:\n%s", log.String())
	}
}

func TestDetailfIndentsTwoPastTheTextColumn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &Printer{W: &buf, Tier: GlyphsSafe}
	p.Warnf(".idea/ — 20 links into the main worktree, not mounted")
	p.Detailf("read .idea/ from the clone instead")
	lines := plainLines(buf.String())
	if len(lines) != 2 || lines[1] != "    read .idea/ from the clone instead" {
		t.Errorf("Detailf drew %q", lines)
	}
	if !strings.Contains(buf.String(), ANSI(ColorRule)+"read .idea/") {
		t.Errorf("a detail line must be dim:\n%q", buf.String())
	}
	var plain bytes.Buffer
	(&Printer{W: &plain, Plain: true}).Detailf("read %s", ".idea/")
	if plain.String() != "    read .idea/\n" {
		t.Errorf("plain Detailf = %q", plain.String())
	}
}

func TestTildeAbbreviatesOnlyUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		home:                                   "~",
		filepath.Join(home, ".proveo", "logs"): "~" + string(filepath.Separator) + filepath.Join(".proveo", "logs"),
		home + "-other/x":                      home + "-other/x",
		"/etc/hosts":                           "/etc/hosts",
		"relative/path":                        "relative/path",
	} {
		if got := Tilde(in); got != want {
			t.Errorf("Tilde(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTildeInAbbreviatesEachHomePathInsideText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		home: "~",
		"open " + home + sep + "w" + sep + ".env: permission denied": "open ~" + sep + "w" + sep + ".env: permission denied",
		home + sep + "a → " + home + sep + "b":                       "~" + sep + "a → ~" + sep + "b",
		`"` + home + sep + "q" + `"`:                                 `"~` + sep + `q"`,
		"/mnt" + home + sep + "x":                                    "/mnt" + home + sep + "x",
		home + "-other" + sep + "x":                                  home + "-other" + sep + "x",
	} {
		if got := TildeIn(in); got != want {
			t.Errorf("TildeIn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFactsWithNothingToPrintDrawNoHeading(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &Printer{W: &buf, Plain: true}
	p.Section("run")
	p.Facts(Fact{Role: RoleStore, Key: "log"}, Fact{Role: RoleStore, Key: "scope"})
	if buf.Len() != 0 {
		t.Errorf("Facts with no value drew %q; want nothing, not even the pending heading", buf.String())
	}
	p.Facts(Fact{Role: RoleStore, Key: "log", Value: "/l"})
	if !strings.Contains(buf.String(), " run ") {
		t.Errorf("the first row that prints must draw the pending heading:\n%s", buf.String())
	}
}

func TestAWrappedFactKeepsItsAlignedGapAndBreaksALongWordAtItsOwnColumn(t *testing.T) {
	var term bytes.Buffer
	saved := Default
	t.Cleanup(func() { Default = saved })
	Default = &Printer{W: &wrapWriter{w: &term, width: func() int { return 40 }}, Tier: GlyphsSafe}
	Facts(Fact{Role: RoleHost, Key: "toolbox", Value: "jetbrains://gateway/ssh/environment?h=sb.sbx&launchIde=true"},
		Fact{Role: RoleHost, Key: "vs code", Value: "code --remote ssh-remote+sb.sbx /a/very/long/project/path"},
		Fact{Role: RoleHost, Key: "jetbrains", Value: "File"})
	lines := plainLines(term.String())
	want := []string{"● toolbox:   jetbrains://gateway/ssh/en", "● vs code:   code --remote"}
	for _, w := range want {
		found := false
		for _, l := range lines {
			found = found || strings.HasPrefix(l, w)
		}
		if !found {
			t.Errorf("no row opens %q; a wrapped row keeps its value column:\n%s", w, strings.Join(lines, "\n"))
		}
	}
	for _, l := range lines {
		if w := len([]rune(l)); w > 40 {
			t.Errorf("a row is %d columns on a 40-column terminal: %q", w, l)
		}
	}
	valCol := col(lines[0], "jetbrains")
	if lead := len(lines[1]) - len(strings.TrimLeft(lines[1], " ")); lead != valCol {
		t.Errorf("the link's continuation starts at column %d, want the value column %d:\n%s", lead, valCol, strings.Join(lines, "\n"))
	}
}

// A wait and an age read alike on every screen: the two largest units, spaced.
func TestSpanWritesAWaitOrAnAgeToTheMinute(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Minute:                              "45m",
		19*time.Hour + 7*time.Minute:                  "19h 7m",
		46*time.Hour + 10*time.Minute:                 "1d 22h",
		19*time.Hour + 6*time.Minute + 31*time.Second: "19h 7m",
	} {
		if got := Span(d); got != want {
			t.Errorf("Span(%s) = %q, want %q", d, got, want)
		}
	}
}
