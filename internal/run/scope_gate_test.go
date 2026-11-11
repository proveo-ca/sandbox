// SPEC: _spec/internal/run/run-spec.puml
package run

import (
	"bytes"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
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
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	projs := []workspace.Project{{Name: "web", Path: "apps/web"}}
	asked := false
	scope, err := chooseScope(scopeRoot, ScopeAsk{Projects: projs}, func(ScopeAsk) (string, bool) { asked = true; return "apps/web", true })
	if err != nil || scope != "" {
		t.Errorf("chooseScope = %q, %v; want the repo root", scope, err)
	}
	if asked {
		t.Error("the picker opened for a run that skips the question")
	}
	if !strings.Contains(term.String(), "scope: repo root") {
		t.Errorf("the run did not say which scope it took:\n%s", term.String())
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
