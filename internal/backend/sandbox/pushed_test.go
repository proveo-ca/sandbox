// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	raw, err := gitReflog(src)
	if err != nil {
		t.Fatal(err)
	}
	got := pushedBranches(raw, before)
	if strings.Join(got, "\n") != "origin/main\norigin/other" {
		t.Fatalf("reflog %q\nbranches %q", raw, got)
	}
}
