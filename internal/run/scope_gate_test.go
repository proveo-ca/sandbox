// SPEC: _spec/internal/run/run-spec.puml
package run

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestTheScopeGateAsksOnlyAHeadedRunThatWasNotToldYes(t *testing.T) {
	for _, c := range []struct {
		name                   string
		p                      Params
		tty, wizard, scheduled bool
		want                   scopeChoice
	}{
		{"a headed run", Params{}, true, true, false, scopeAsk},
		{"--yes", Params{Yes: true}, true, true, false, scopeRoot},
		{"a scheduled run on its tmux terminal", Params{}, true, true, true, scopeRoot},
		{"--yes without a terminal", Params{Yes: true}, false, true, false, scopeRoot},
		{"--print", Params{PrintOnly: true}, true, true, false, scopeSilent},
		{"--print --yes", Params{PrintOnly: true, Yes: true}, true, true, true, scopeSilent},
		{"no terminal", Params{}, false, true, false, scopeSilent},
		{"the wizard switched off", Params{}, true, false, false, scopeSilent},
	} {
		if got := scopeGate(&c.p, c.tty, c.wizard, c.scheduled); got != c.want {
			t.Errorf("%s: scopeGate = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestASkippedScopeQuestionTakesTheRepoRootAndSaysSo(t *testing.T) {
	rs, p, term, projs := pagesFixture(t)
	asked := false
	err := walkPages(rs, p, scopeRoot, projs,
		func(ScopeAsk) (string, bool) { asked = true; return "apps/web", true },
		func(*formVisit) error { return nil })
	if err != nil || rs.Workspace.SubScope != "" {
		t.Errorf("walkPages = %v, scope %q; want the repo root", err, rs.Workspace.SubScope)
	}
	if asked {
		t.Error("the picker opened for a run that skips the question")
	}
	if term.Len() != 0 {
		t.Errorf("the pages printed before the run block:\n%s", term.String())
	}
	reportRun(rs, false)
	if !strings.Contains(term.String(), "scope: repo root") {
		t.Errorf("the run block did not say which scope the run took:\n%s", term.String())
	}
}

func TestTheRunBlockAlignsTheLogAndTheScope(t *testing.T) {
	rs, _, term, _ := pagesFixture(t)
	home := t.TempDir()
	t.Setenv("PROVEO_HOME", home)
	log, err := runlog.Open("proveo-1-2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	rs.Log = log
	rs.Workspace.ScopeLabel = "apps/web"
	reportRun(rs, false)
	want := "------ run ------\nlog:   " + filepath.Join(home, "logs", "proveo-1-2.log") + "\nscope: apps/web\n"
	if got := term.String(); !strings.Contains(got, want) {
		t.Errorf("the run block = %q, want the log and the scope aligned in one block:\n%s", got, want)
	}
	term.Reset()
	rs.Log, rs.Workspace.ScopeLabel = nil, ""
	reportRun(rs, false)
	if got := term.String(); got != "" {
		t.Errorf("a run with no transcript and no scope prints nothing:\n%q", got)
	}
}

func TestARunBlockWithNothingToSayDrawsNoHeading(t *testing.T) {
	rs, _, term, _ := pagesFixture(t)
	rs.Log, rs.Workspace.ScopeLabel = nil, ""
	reportRun(rs, false)
	if got := term.String(); got != "" {
		t.Errorf("a run with no transcript and no scope drew %q; want nothing, not even the run heading", got)
	}
}

func TestClosingTheScopePickerCancelsTheRun(t *testing.T) {
	projs := []workspace.Project{{Name: "web", Path: "apps/web"}}
	for _, c := range []struct {
		name, picked, want string
		closed, wantErr    bool
	}{
		{"a project", "apps/web", "apps/web", false, false},
		{"the repo root row", "", "", false, false},
		{"the page closed", "", "", true, true},
		{"the page closed on a project", "apps/web", "", true, true},
	} {
		scope, err := chooseScope(scopeAsk, ScopeAsk{Projects: projs}, func(ScopeAsk) (string, bool) { return c.picked, !c.closed })
		if scope != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: chooseScope = %q, %v; want %q, error %v", c.name, scope, err, c.want, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "cancelled at the scope prompt") {
			t.Errorf("%s: error %q does not say where the run was cancelled", c.name, err)
		}
	}
}

func TestARunThatEndsBeforeItsBlocksStillPrintsWhatItDecided(t *testing.T) {
	rs, _, term, _ := pagesFixture(t)
	rs.Workspace.Items = []sandbox.Item{{Warn: true, Text: "dependency trees — 2 cross into the sandbox as the host built them"}}
	rs.Interface = []sandbox.Item{{Warn: true, Text: "port 3000 — not published", Detail: []string{"/w/apps/web"}}}
	reportUnhanded(rs, true)
	got := term.String()
	for _, want := range []string{"------ workspace ------", "dependency trees — 2 cross", "------ interface ------", "port 3000 — not published", "/w/apps/web"} {
		if !strings.Contains(got, want) {
			t.Errorf("a run that failed before sandbox.Run lost %q:\n%s", want, got)
		}
	}
	term.Reset()
	reportUnhanded(&Spec{}, false)
	if got := term.String(); got != "" {
		t.Errorf("a run that decided nothing printed %q", got)
	}
}
