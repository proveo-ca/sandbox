// SPEC: _spec/internal/run/run-spec.puml
package run

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// pagesFixture is a monorepo run about to walk its pages, with the terminal captured.
func pagesFixture(t *testing.T) (*Spec, *Params, *bytes.Buffer, []workspace.Project) {
	t.Helper()
	term := &bytes.Buffer{}
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(term)
	t.Setenv("PROVEO_ENV_FILE", "")
	t.Setenv("PROVEO_SBX_MCP", "")
	t.Setenv("PROVEO_MISE_TRUST", "")
	root := t.TempDir()
	rs := &Spec{Start: root}
	rs.Workspace.RepoRoot = root
	rs.Workspace.Scope.IsRepo = true
	rs.Workspace.Scope.Root = root
	return rs, &Params{Target: "claudecode"}, term,
		[]workspace.Project{{Name: "web", Path: "apps/web"}, {Name: "api", Path: "apps/api"}}
}

func TestAStepBackReturnsToTheScopePageWhereItWasLeft(t *testing.T) {
	rs, p, term, projs := pagesFixture(t)
	var asks []ScopeAsk
	pick := func(ask ScopeAsk) (string, bool) {
		asks = append(asks, ask)
		return []string{"apps/web", "apps/api"}[len(asks)-1], true
	}
	left := &choiceui.Form{Title: "the form the first visit left"}
	var visits []formVisit
	var seen [][]string
	form := func(v *formVisit) error {
		visits = append(visits, *v)
		seen = append(seen, slices.Clone(p.Addons))
		p.Addons = append(p.Addons, "browser")
		if len(visits) == 1 {
			v.left = left
			return errStepBack
		}
		return nil
	}
	if err := walkPages(rs, p, scopeAsk, projs, pick, form); err != nil {
		t.Fatal(err)
	}
	if len(asks) != 2 || asks[0].Picked != "" || asks[1].Picked != "apps/web" {
		t.Fatalf("the scope page was asked %+v; the second visit must open on apps/web", asks)
	}
	if asks[0].Step != (choiceui.Step{At: 1, Of: 2}) || visits[0].step != (choiceui.Step{At: 2, Of: 2}) {
		t.Errorf("pages are numbered %+v then %+v, want 1/2 then 2/2", asks[0].Step, visits[0].step)
	}
	if visits[1].left != left {
		t.Error("the second visit to the form did not get the form the first one left")
	}
	if len(seen[1]) != 0 {
		t.Errorf("the second visit started from what the first one changed: %v", seen[1])
	}
	if rs.Workspace.SubScope != "apps/api" || !strings.HasSuffix(rs.Workspace.WS.InputDir, "apps/api") {
		t.Errorf("scope = %q, input %q; want apps/api", rs.Workspace.SubScope, rs.Workspace.WS.InputDir)
	}
	if out := term.String(); strings.Count(out, "scope: ") != 1 || !strings.Contains(out, "scope: apps/api") {
		t.Errorf("the scope line must print once, for the scope the run took:\n%s", out)
	}
}

func TestClosingTheScopePageEndsThePagesBeforeTheForm(t *testing.T) {
	rs, p, _, projs := pagesFixture(t)
	formRan := false
	err := walkPages(rs, p, scopeAsk, projs,
		func(ScopeAsk) (string, bool) { return "", false },
		func(*formVisit) error { formRan = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "cancelled at the scope prompt") || formRan {
		t.Errorf("err = %v, form ran = %v; want a cancel before the form", err, formRan)
	}
}

func TestARunWithNoScopeQuestionShowsTheFormAloneAndCannotStepBack(t *testing.T) {
	for _, c := range []struct {
		name  string
		scope string
		gate  scopeChoice
		projs bool
		want  string
		says  string
	}{
		{"a --scope flag", "/apps/web/", scopeSilent, false, "apps/web", "scope: apps/web"},
		{"--yes in a monorepo", "", scopeRoot, true, "", "scope: repo root"},
		{"a repository with one project", "", scopeAsk, false, "", ""},
	} {
		rs, p, term, projs := pagesFixture(t)
		p.Scope = c.scope
		if !c.projs {
			projs = nil
		}
		var step choiceui.Step
		err := walkPages(rs, p, c.gate, projs,
			func(ScopeAsk) (string, bool) { t.Errorf("%s: the scope page opened", c.name); return "", true },
			func(v *formVisit) error { step = v.step; return nil })
		if err != nil || rs.Workspace.SubScope != c.want || step != (choiceui.Step{}) {
			t.Errorf("%s: err %v, scope %q, form step %+v; want %q and a lone form", c.name, err, rs.Workspace.SubScope, step, c.want)
		}
		if out := term.String(); (c.says == "") != !strings.Contains(out, "scope: ") || !strings.Contains(out, c.says) {
			t.Errorf("%s: terminal = %q, want %q", c.name, out, c.says)
		}
	}
}

func TestAFormErrorEndsThePages(t *testing.T) {
	rs, p, term, projs := pagesFixture(t)
	boom := errors.New("cancelled at the choice prompt")
	err := walkPages(rs, p, scopeAsk, projs,
		func(ScopeAsk) (string, bool) { return "apps/web", true },
		func(*formVisit) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the form's", err)
	}
	if strings.Contains(term.String(), "scope: ") {
		t.Errorf("a run that ended at the form announced a scope:\n%s", term.String())
	}
}

func TestACloneSharesNoSliceOrMapWithItsOriginal(t *testing.T) {
	t.Parallel()
	p := Params{
		Addons: []string{"browser"}, Extra: []string{"--x"},
		Ports: []devports.Candidate{{Port: 3000}}, discovered: []devports.Candidate{{Port: 3000}},
		Apps: []devports.AndroidApp{{AppID: "a"}}, discoveredApps: []devports.AndroidApp{{AppID: "a"}},
		Roles: provider.Roles{"main": "m"}, RolesRemembered: provider.Roles{"main": "m"},
	}
	c := p.clone()
	c.Addons[0], c.Extra[0], c.Ports[0].Port, c.discovered[0].Port = "tui", "--y", 1, 1
	c.Apps[0].AppID, c.discoveredApps[0].AppID = "b", "b"
	c.Roles["main"], c.RolesRemembered["main"] = "n", "n"
	if p.Addons[0] != "browser" || p.Extra[0] != "--x" || p.Ports[0].Port != 3000 || p.discovered[0].Port != 3000 ||
		p.Apps[0].AppID != "a" || p.discoveredApps[0].AppID != "a" || p.Roles["main"] != "m" || p.RolesRemembered["main"] != "m" {
		t.Errorf("a change to the clone reached the original: %+v", p)
	}
	if empty := (Params{}).clone(); empty.Roles != nil || empty.Addons != nil {
		t.Errorf("cloning nothing made something: %+v", empty)
	}
}

func TestTheRunFormNamesWhatEnterStarts(t *testing.T) {
	t.Parallel()
	repo := workspace.MountSpec{InputDir: "/work/repo", RepoRoot: "/work/repo"}
	for _, c := range []struct {
		name  string
		p     Params
		ws    workspace.MountSpec
		first string
		n     int
	}{
		{"a repository clones", Params{Target: "claudecode", Clone: true}, repo, "clone the repository", 3},
		{"--clone=false mounts", Params{Target: "claudecode"}, repo, "mount the checkout — the agent edits it directly (--clone=false)", 3},
		{"no repository mounts and says why", Params{Target: "claudecode", Clone: true},
			workspace.MountSpec{InputDir: "/work/dir"}, "mount the checkout — the agent edits it directly (not a git repository", 3},
		{"a --clone that cannot work names no workspace step", Params{Target: "claudecode", Clone: true, CloneSet: true},
			workspace.MountSpec{InputDir: "/work/dir"}, "create the sandbox", 2},
	} {
		got := runOnEnter(&c.p, c.ws)
		if len(got) != c.n || !strings.HasPrefix(got[0], c.first) || got[len(got)-1] != "start claudecode" {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
