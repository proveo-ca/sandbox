// SPEC: _spec/internal/ui/output-vocabulary.puml (HOLD)
package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/termtext"
)

func TestHeldOutputNeverReachesTheTerminal(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: &wrapWriter{w: term, width: func() int { return 60 }}, Tier: GlyphsSafe, term: term}, nil)
	TeeTo(log)
	Section(SectionSbx)
	Notef("before the card")
	term.Reset()
	teed := Default

	release := Hold()
	Section(SectionGit)
	Warnf("Docker login — not signed in")
	Asyncf("step 1/2 · install · 3s")
	Live(RoleAsync, "a live frame")
	Frame("sbx login", "sbx's screen", "page 1/4 returns when it exits")
	EndLive()
	if got := term.String(); got != "" {
		t.Fatalf("a held line reached the terminal: %q", got)
	}
	if got := log.String(); strings.Contains(got, "not signed in") {
		t.Errorf("a held line reached the run log before the replay: %q", got)
	}

	held := release()
	if Default != teed {
		t.Fatalf("release left Default=%p, want the printer before the hold %p", Default, teed)
	}
	if again := release(); !bytes.Equal(again, held) {
		t.Errorf("a second release returned %q, want the same %q", again, held)
	}
	text := termtext.StripANSI(string(held))
	for _, want := range []string{" " + SectionGit + " ", "Docker login — not signed in", "step 1/2 · install", "── sbx login ──"} {
		if !strings.Contains(text, want) {
			t.Errorf("the hold lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(string(held), "a live frame") {
		t.Errorf("a live frame was kept; it belongs to a terminal the hold stood in for:\n%s", held)
	}

	Replay(held)
	got := term.String()
	if !strings.Contains(got, "Docker login") || !strings.Contains(got, " "+SectionGit+" ") {
		t.Errorf("the replay did not reach the terminal:\n%s", got)
	}
	if !strings.Contains(log.String(), "Docker login — not signed in") {
		t.Errorf("the replay did not reach the run log:\n%s", log.String())
	}
	term.Reset()
	Section(SectionGit)
	Notef("after the card")
	if got := term.String(); strings.Contains(got, " "+SectionGit+" ") {
		t.Errorf("the git heading drew twice: the hold's section state did not carry back:\n%s", got)
	}
}
