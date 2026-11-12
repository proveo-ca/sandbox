// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestPushedBranchesKeepsOneLinePerRefAfterSince(t *testing.T) {
	t.Parallel()
	reflog := strings.Join([]string{
		"origin/main@{100}\tupdate by push",
		"origin/main@{200}\tupdate by push",
		"origin/other@{200}\tupdate by push",
		"main@{200}\tcommit: other",
		"worktrees/wt/HEAD@{200}\tcommit: other",
		"origin/old@{50}\tupdate by push",
	}, "\n")
	got := pushedBranches(reflog, time.Unix(100, 0))
	want := []string{"origin/main", "origin/other"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q", got)
	}
}

func TestGitReflogSeesPushesFromEveryWorktree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	wt := filepath.Join(dir, "wt")
	remote := filepath.Join(dir, "remote")
	git := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "main", src)
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(src, "add", "a")
	git(src, "commit", "-qm", "init")
	git(dir, "init", "-q", "--bare", remote)
	git(src, "remote", "add", "origin", remote)
	before := time.Now().Add(-time.Second)
	git(src, "push", "-q", "-u", "origin", "HEAD")
	git(src, "worktree", "add", "-q", "-b", "other", wt, "main")
	if err := os.WriteFile(filepath.Join(wt, "b"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "add", "b")
	git(wt, "commit", "-qm", "other")
	git(wt, "push", "-q", "-u", "origin", "other")
	raw, err := gitReflog(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	got := pushedBranches(raw, before)
	if strings.Join(got, "\n") != "origin/main\norigin/other" {
		t.Fatalf("reflog %q\nbranches %q", raw, got)
	}
}

func TestPushedFetchSkipsAnAssistant(t *testing.T) {
	t.Parallel()
	coding := []string{"cursor", "opencode", "codex", "claudecode"}
	for _, name := range coding {
		if !offerPushedFetch(Input{Target: name}) {
			t.Errorf("%s is a coding harness and must be asked", name)
		}
	}
	hermes := Input{Target: "hermes", Man: manifest.Manifest{Kind: manifest.KindAssistant}}
	if offerPushedFetch(hermes) {
		t.Fatal("hermes is an assistant and must not be asked")
	}
}

func TestPushedFetchArgsDropsUnsafeNames(t *testing.T) {
	t.Parallel()
	got := pushedFetchArgs("/repo", []string{
		"origin/other",
		"fork/feat",
		"origin/feat",
		"origin/../../etc",
		"origin/branch:refs/heads/main",
		"-o/evil",
	})
	want := [][]string{
		{"-C", "/repo", "fetch", "--no-tags", "fork", "feat"},
		{"-C", "/repo", "fetch", "--no-tags", "origin", "feat", "other"},
	}
	if len(got) != len(want) {
		t.Fatalf("cmds %#v", got)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Fatalf("cmd %d = %q", i, got[i])
		}
	}
	lines := fetchPushedLines("/repo", []string{"origin/feat"})
	if len(lines) != 1 || !strings.Contains(lines[0], "git -C /repo fetch --no-tags origin feat") {
		t.Fatalf("lines %q", lines)
	}
}

func TestAPushedBranchIsAFactAndTheFetchRecipeGoesToTheLog(t *testing.T) {
	dir := t.TempDir()
	src, remote := filepath.Join(dir, "src"), filepath.Join(dir, "remote")
	git := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "main", src)
	git(src, "commit", "-q", "--allow-empty", "-m", "init")
	git(dir, "init", "-q", "--bare", remote)
	git(src, "remote", "add", "origin", remote)
	before := time.Now().Add(-time.Second)
	git(src, "push", "-q", "-u", "origin", "HEAD:refs/heads/feat")
	prev := confirmFetchPushed
	t.Cleanup(func() { confirmFetchPushed = prev })
	for _, fetch := range []bool{false, true} {
		confirmFetchPushed = func([]string) bool { return fetch }
		screen, log := captured(t)
		out := NewBlock(ResultsHeading)
		reportPushedBranches(Input{Target: "claudecode", RepoRoot: src}, sbx.RunConfig{Name: "sb"}, before, out)
		out.Print()
		got := screen.String()
		want := "pushed: origin/feat\n"
		if fetch {
			want += "        fetched\n"
		}
		if !strings.HasSuffix(got, want) {
			t.Errorf("fetch %v: the pushed fact:\n%s\nwant:\n%s", fetch, got, want)
		}
		if recipe := "git -C " + src + " fetch --no-tags origin feat"; strings.Contains(got, recipe) || fetch == strings.Contains(log.String(), recipe) {
			t.Errorf("fetch %v: the fetch recipe goes to the run log only when the operator left it:\nscreen:\n%s\nlog:\n%s", fetch, got, log.String())
		}
	}
}
