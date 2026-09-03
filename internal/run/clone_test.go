// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/workspace"
)

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkedWorktree lays out a main checkout and a linked worktree the way git does:
// the worktree's .git is a FILE pointing into <main>/.git/worktrees/<name>, whose
// commondir points back at <main>/.git.
func linkedWorktree(t *testing.T) (mainRepo, wt string) {
	t.Helper()
	root := t.TempDir()
	mainRepo = filepath.Join(root, "main")
	wt = filepath.Join(root, "wt")
	mkdir(t, filepath.Join(mainRepo, ".git", "worktrees", "wt"))
	write(t, filepath.Join(mainRepo, ".git", "worktrees", "wt", "commondir"), "../..\n")
	mkdir(t, wt)
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "wt")+"\n")
	return mainRepo, wt
}

func TestCloneDefaultIsOnUnlessTheEnvironmentSaysOff(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]bool{"": true, "on": true, "1": true, "off": false, "0": false, "false": false, "NO": false} {
		if got := CloneDefault(func(string) string { return v }); got != want {
			t.Errorf("PROVEO_CLONE=%q → %v, want %v", v, got, want)
		}
	}
}

func TestDecideCloneDefaultsToACloneOnlyWhereSbxCanMakeOne(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	mkdir(t, filepath.Join(repo, ".git"))
	mainRepo, wt := linkedWorktree(t)

	cases := []struct {
		name    string
		p       Params
		sbx     bool
		ws      workspace.MountSpec
		on      bool
		whyHas  string
		errHas  string
		explain string
	}{
		{name: "repo root on sbx clones", p: Params{Clone: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: repo}, on: true},
		{name: "opted out stays mounted", p: Params{Clone: false, CloneSet: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: repo}, on: false},
		{name: "docker backend: default is silently off", p: Params{Clone: true}, sbx: false,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: repo}, on: false},
		{name: "docker backend: explicit --clone is an error", p: Params{Clone: true, CloneSet: true}, sbx: false,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: repo}, errHas: "sbx-backend feature"},
		{name: "no repository: default falls back and says so", p: Params{Clone: true}, sbx: true,
			ws: workspace.MountSpec{InputDir: t.TempDir()}, on: false, whyHas: "not a git repository"},
		{name: "no repository: explicit --clone is an error", p: Params{Clone: true, CloneSet: true}, sbx: true,
			ws: workspace.MountSpec{InputDir: t.TempDir()}, errHas: "needs a git repository"},
		{name: "linked worktree: default falls back", p: Params{Clone: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: wt, InputDir: wt}, on: false, whyHas: "linked git worktree",
			explain: "sbx documents clone mode for the main worktree only — " + mainRepo},
		{name: "linked worktree: explicit --clone is an error", p: Params{Clone: true, CloneSet: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: wt, InputDir: wt}, errHas: "linked git worktree"},
		{name: "monorepo sub-scope: default falls back", p: Params{Clone: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: filepath.Join(repo, "apps", "web")}, on: false, whyHas: "sub-scope"},
		{name: "monorepo sub-scope: explicit --clone is honoured", p: Params{Clone: true, CloneSet: true}, sbx: true,
			ws: workspace.MountSpec{RepoRoot: repo, InputDir: filepath.Join(repo, "apps", "web")}, on: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on, why, err := decideClone(&tc.p, tc.sbx, tc.ws)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if on != tc.on {
				t.Errorf("clone = %v, want %v (why=%q) %s", on, tc.on, why, tc.explain)
			}
			if tc.whyHas != "" && !strings.Contains(why, tc.whyHas) {
				t.Errorf("fallback reason %q does not mention %q — the operator has to see why the default did not apply", why, tc.whyHas)
			}
			if tc.on && why != "" {
				t.Errorf("a clone that applies must not carry a fallback reason: %q", why)
			}
		})
	}
}
