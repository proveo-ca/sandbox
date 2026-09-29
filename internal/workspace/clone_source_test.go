// SPEC: _spec/internal/sbx/clone-workspace.puml
package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoWithWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, wt = filepath.Join(root, "main"), filepath.Join(root, "wt")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "a")
	git(t, main, "worktree", "add", "-q", "-b", "feat", wt)
	return main, wt
}

func TestCloneSourceNamesTheMainWorktreeAndTheBranch(t *testing.T) {
	t.Parallel()
	main, wt := repoWithWorktree(t)
	src, err := CloneSource(wt)
	if err != nil {
		t.Fatal(err)
	}
	if src.Main != main || src.Ref != "feat" {
		t.Errorf("= %+v, want {%s feat}", src, main)
	}
}

func TestCloneSourceAcceptsADetachedHeadOnlyWhenABranchHoldsIt(t *testing.T) {
	t.Parallel()
	_, wt := repoWithWorktree(t)
	tip := git(t, wt, "rev-parse", "HEAD")
	git(t, wt, "checkout", "-q", "--detach")
	if src, err := CloneSource(wt); err != nil || src.Ref != tip {
		t.Errorf("detached on a branch tip: %+v, %v — the clone holds that commit", src, err)
	}
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "orphan")
	if _, err := CloneSource(wt); err == nil || !strings.Contains(err.Error(), "on no branch") {
		t.Errorf("err = %v — sbx clones branches, so a commit no branch holds never reaches the clone", err)
	}
}

func TestCloneSourceRejectsWhatIsNotALinkedWorktree(t *testing.T) {
	t.Parallel()
	main, _ := repoWithWorktree(t)
	if _, err := CloneSource(main); err == nil {
		t.Error("the main worktree needs no clone source")
	}
}
