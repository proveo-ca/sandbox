// SPEC: _spec/internal/ui/output-vocabulary.puml (THE CLOSED CHECKLIST)
package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/termtext"
)

// THE CLOSED CHECKLIST: a row's outcome starts at 18 and wraps at " · " back to 18; a word wider
// than its room keeps its head and tail; plain output is one line per row with no escapes.
func TestACheckRowLaysItsOutcomeAt18AndWrapsAtItsSeparators(t *testing.T) {
	t.Parallel()
	var term bytes.Buffer
	p := &Printer{W: &wrapWriter{w: &term, width: func() int { return 50 }}, Tier: GlyphsSafe, term: &term}
	p.CheckRow(Check{Mark: "–", Ink: ColorMuted, Label: "not held", Outcome: "claudecode, cursor sign-ins · openai, cursor keys"})
	p.CheckRow(Check{Mark: "✓", Ink: ColorSuccess, Label: "sbx install", Outcome: "kept 0.50.0 · /very/long/path/that/never/fits/in/thirty/columns/bin/sbx"})
	p.CheckRow(Check{Mark: "!", Ink: ColorWarn, Label: "git identity", Outcome: "not set", Detail: []string{"git config --global user.name and user.email, from a terminal"}})
	p.CheckItem(Check{Mark: "✓", Ink: ColorSuccess, Label: "host — ready to run"}, true)
	want := "  – not held      claudecode, cursor sign-ins\n" +
		"                  openai, cursor keys\n" +
		"  ✓ sbx install   kept 0.50.0\n" +
		"                  /very/long/path…olumns/bin/sbx\n" +
		"  ! git identity  not set\n" +
		"      git config --global user.name and\n" +
		"      user.email, from a terminal\n" +
		"  ✓ host — ready to run\n"
	if got := termtext.StripANSI(term.String()); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(term.String(), ANSIBold+"host — ready to run") {
		t.Error("the verdict is not bold")
	}

	var plain bytes.Buffer
	q := &Printer{W: &plain, Plain: true, Tier: GlyphsSafe}
	q.CheckRow(Check{Mark: "!", Tag: "warn: ", Label: "Docker login", Outcome: "not signed in", Detail: []string{"`sbx login` on a terminal"}})
	q.CheckRow(Check{Mark: "–", Label: "GitHub", Outcome: "later · --skip-gh"})
	q.CheckItem(Check{Mark: "×", Tag: "error: ", Label: "Daemon — not running", Detail: []string{"Start Docker Sandboxes"}}, true)
	if got, want := plain.String(), "warn: Docker login — not signed in\n    `sbx login` on a terminal\nGitHub — later · --skip-gh\n"+
		"error: Daemon — not running\n    Start Docker Sandboxes\n"; got != want {
		t.Errorf("plain got\n%q\nwant\n%q", got, want)
	}
}
