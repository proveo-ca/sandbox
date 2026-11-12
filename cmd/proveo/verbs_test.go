// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/ui"
)

// carriedCheckout is a repository on main with one carried ref a commit ahead, the working directory
// and state root pointed at it, and a PATH that holds git alone.
func carriedCheckout(t *testing.T) (repo, store string, git func(args ...string) string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the PATH holds a symlink named git")
	}
	bin := t.TempDir()
	if err := os.Symlink(mustLookPath(t, "git"), filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	repo = results.Canonical(t.TempDir())
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	git("commit", "-q", "--allow-empty", "-m", "the agent's work")
	git("update-ref", "refs/proveo/proveo-1-2/feat", "HEAD")
	git("reset", "-q", "--hard", "HEAD~1")
	state := t.TempDir()
	store = results.Dir(state)
	if err := results.Put(store, results.Entry{Kind: results.Carried, Key: "proveo-1-2", Repo: repo,
		Refs: []results.Ref{{Name: "proveo/proveo-1-2/feat", Lead: 1}}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_STATE_ROOT", state)
	t.Chdir(repo)
	return repo, store, git
}

func captureUI(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&out)
	return &out
}

func TestAdoptCreatesTheBranchLeavesTheCheckoutAloneAndLowersThePromptCount(t *testing.T) {
	repo, store, git := carriedCheckout(t)
	out := captureUI(t)
	if err := os.WriteFile(filepath.Join(repo, "mine.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, status := git("rev-parse", "HEAD"), git("status", "--porcelain")
	if got := leftLine(filepath.Dir(store), repo); got != "1 to adopt" {
		t.Fatalf("before: prompt line = %q", got)
	}

	list := adoptCmd()
	if err := list.RunE(list, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "to adopt: proveo/proveo-1-2/feat (1 new commit)\n" {
		t.Errorf("the list is one fact under no heading, counted as a run's branches fact counts:\n%s", out.String())
	}

	out.Reset()
	cmd := adoptCmd()
	if err := cmd.RunE(cmd, []string{"proveo-1-2"}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if git("rev-parse", "refs/heads/feat") != git("rev-parse", "refs/proveo/proveo-1-2/feat") {
		t.Error("the branch is not at the carried ref")
	}
	if git("rev-parse", "HEAD") != head || git("status", "--porcelain") != status {
		t.Error("adopt moved HEAD or touched the working tree")
	}
	if got := leftLine(filepath.Dir(store), repo); got != "" {
		t.Errorf("after: prompt line = %q, want nothing left to adopt", got)
	}
	if out.String() != "ok: branch feat — created at proveo/proveo-1-2/feat\n" {
		t.Errorf("adopt closes on the branch it created, the how-to in its help:\n%s", out.String())
	}

	out.Reset()
	again := adoptCmd()
	err := again.RunE(again, []string{"proveo-1-2"})
	if err == nil || !strings.Contains(err.Error(), "--as") || !errors.As(err, new(backend.Reported)) {
		t.Errorf("a second adopt = %v, want the printed refusal that names --as", err)
	}
	if want := "error: feat — a branch of that name exists\n    `--as <name>` names the new branch\n"; out.String() != want {
		t.Errorf("the refusal =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestAdoptRefusalsAreItemsAndMainAddsNoSecondCross(t *testing.T) {
	_, _, git := carriedCheckout(t)
	git("update-ref", "refs/proveo/proveo-3-4/feat", "refs/proveo/proveo-1-2/feat")
	git("branch", "taken")
	for _, c := range []struct {
		args []string
		as   string
		want string
	}{
		{[]string{"nope"}, "", "error: nope — no carried ref matches\n"},
		{[]string{"feat"}, "", "error: feat — fits 2 carried refs\n    proveo/proveo-1-2/feat\n    proveo/proveo-3-4/feat\n"},
		{[]string{"proveo-1-2"}, "bad..name", "error: bad..name — not a valid branch name\n"},
		{[]string{"proveo-1-2"}, "taken", "error: taken — a branch of that name exists\n"},
	} {
		out := captureUI(t)
		cmd := adoptCmd()
		if c.as != "" {
			if err := cmd.Flags().Set("as", c.as); err != nil {
				t.Fatal(err)
			}
		}
		err := cmd.RunE(cmd, c.args)
		if code, ok := exitCode(err); !ok || code != 1 {
			t.Errorf("adopt %v --as %q = %v: main would print it again", c.args, c.as, err)
		}
		if out.String() != c.want {
			t.Errorf("adopt %v --as %q =\n%s\nwant\n%s", c.args, c.as, out.String(), c.want)
		}
	}
}

func TestAdoptWithNothingCarriedSaysNone(t *testing.T) {
	_, _, git := carriedCheckout(t)
	git("branch", "feat", "refs/proveo/proveo-1-2/feat")
	out := captureUI(t)
	cmd := adoptCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "to adopt: none\n" {
		t.Errorf("adopt with every ref on a branch = %q, want the fact valued none", out.String())
	}
}

func TestAdoptOutsideARepositoryRefuses(t *testing.T) {
	t.Setenv("PROVEO_STATE_ROOT", t.TempDir())
	t.Chdir(t.TempDir())
	out := captureUI(t)
	cmd := adoptCmd()
	err := cmd.RunE(cmd, nil)
	if !errors.As(err, new(backend.Reported)) || out.String() != "error: adopt — not inside a git repository\n" {
		t.Errorf("adopt outside a repository = %v\n%s", err, out.String())
	}
}

func TestRmWithoutAnArgumentListsWhatWasKept(t *testing.T) {
	repo, store, _ := carriedCheckout(t)
	out := captureUI(t)
	for _, e := range []results.Entry{
		{Kind: results.Kept, Key: "proveo-claudecode-abc", Repo: repo, Why: "the run failed"},
		{Kind: results.Kept, Key: "proveo-hermes-def", Repo: "/somewhere/else", Why: "its removal failed"},
	} {
		if err := results.Put(store, e); err != nil {
			t.Fatal(err)
		}
	}
	cmd := rmCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	mine, other := strings.Index(got, "proveo-claudecode-abc — the run failed"), strings.Index(got, "proveo-hermes-def — its removal failed")
	if mine < 0 || other < mine {
		t.Errorf("want this repository's sandbox first, then the other:\n%s", got)
	}
	if !strings.Contains(got, " · in /somewhere/else\n") {
		t.Errorf("the other repository's sandbox does not name where it ran:\n%s", got)
	}
	for _, gone := range []string{"proveo rm <sandbox>", "results", "------"} {
		if strings.Contains(got, gone) {
			t.Errorf("the list holds %q: its how-to is in --help and it draws no heading:\n%s", gone, got)
		}
	}
}

func TestTheKeptListWritesItsAgeOnADimLine(t *testing.T) {
	repo, store, _ := carriedCheckout(t)
	out := captureUI(t)
	at := time.Now().Add(-(19*time.Hour + 7*time.Minute))
	if err := results.Put(store, results.Entry{Kind: results.Kept, Key: "proveo-claudecode-abc", Repo: repo, Why: "the run failed", At: at}); err != nil {
		t.Fatal(err)
	}
	cmd := rmCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if want := "proveo-claudecode-abc — the run failed\n    19h 7m ago\n"; out.String() != want {
		t.Errorf("the kept list =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestRmWithNothingKeptSaysSoOnce(t *testing.T) {
	carriedCheckout(t)
	out := captureUI(t)
	cmd := rmCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no sandbox kept\n" {
		t.Errorf("rm with nothing kept = %q", out.String())
	}
}

func TestRmOfASandboxSbxNoLongerListsSaysAlreadyGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub sbx is a shell script")
	}
	_, store, _ := carriedCheckout(t)
	bin := filepath.SplitList(os.Getenv("PATH"))[0]
	stub := "#!/bin/sh\ncase \"$*\" in\n" +
		"  'ls --json') echo '[{\"name\":\"bystander\",\"status\":\"running\"}]' ;;\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nbystander claude running\\n' ;;\n" +
		"  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := results.Put(store, results.Entry{Kind: results.Kept, Key: "proveo-claudecode-gone", Why: "the run failed"}); err != nil {
		t.Fatal(err)
	}
	out := captureUI(t)
	cmd := rmCmd()
	if err := cmd.RunE(cmd, []string{"proveo-claudecode-gone"}); err != nil {
		t.Fatalf("rm of a gone sandbox = %v\n%s", err, out.String())
	}
	if out.String() != "ok: proveo-claudecode-gone — already gone\n" {
		t.Errorf("rm of a gone sandbox = %q", out.String())
	}
}

func mustLookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s unavailable", name)
	}
	return p
}
