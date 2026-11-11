// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/ui"
)

// carriedCheckout is a repository on main with one carried ref a commit ahead, and the
// test's working directory and state root pointed at it.
func carriedCheckout(t *testing.T) (repo, store string, git func(args ...string) string) {
	t.Helper()
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
	if !strings.Contains(out.String(), "proveo/proveo-1-2/feat — 1 commit no branch holds") {
		t.Errorf("the list does not name the waiting ref:\n%s", out.String())
	}

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
	if !strings.Contains(out.String(), "git switch feat") {
		t.Errorf("adopt does not say how to reach the branch:\n%s", out.String())
	}

	again := adoptCmd()
	if err := again.RunE(again, []string{"proveo-1-2"}); err == nil || !strings.Contains(err.Error(), "--as") {
		t.Errorf("a second adopt = %v, want the refusal that names --as", err)
	}
}

func TestAdoptOutsideARepositoryRefuses(t *testing.T) {
	t.Setenv("PROVEO_STATE_ROOT", t.TempDir())
	t.Chdir(t.TempDir())
	cmd := adoptCmd()
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Error("adopt ran outside a repository")
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
	// A PATH that holds git alone: sbx's listing fails, so the reconcile clears nothing.
	bin := t.TempDir()
	if err := os.Symlink(mustLookPath(t, "git"), filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cmd := rmCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	mine, other := strings.Index(got, "proveo-claudecode-abc — the run failed"), strings.Index(got, "proveo-hermes-def — its removal failed")
	if mine < 0 || other < mine {
		t.Errorf("want this repository's sandbox first, then the other:\n%s", got)
	}
	for _, want := range []string{"in /somewhere/else", "proveo rm <sandbox>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
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
