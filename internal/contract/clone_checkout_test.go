// SPEC: _spec/internal/sbx/clone-workspace.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// cloneWithBranch builds what sbx hands the seed: a clone whose origin carries
// the linked worktree's branch only as origin/<branch>.
func cloneWithBranch(t *testing.T) (clone, featTip string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "init", "-q")
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", "a")
	gitIn(t, src, "commit", "-qm", "a")
	gitIn(t, src, "worktree", "add", "-q", "-b", "feat", filepath.Join(root, "wt"))
	if err := os.WriteFile(filepath.Join(root, "wt", "b"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, filepath.Join(root, "wt"), "add", "b")
	gitIn(t, filepath.Join(root, "wt"), "commit", "-qm", "b")
	featTip = gitIn(t, src, "rev-parse", "feat")
	clone = filepath.Join(root, "clone")
	gitIn(t, root, "clone", "-q", src, clone)
	return clone, featTip
}

func runCloneCheckout(t *testing.T, main, workdir, ref string) string {
	t.Helper()
	bash := bashOrSkip(t)
	script := `source "$1/packages/lib/entrypoint-lib.sh"; proveo_clone_checkout; echo DONE`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_WORKDIR="+workdir, "PROVEO_CLONE_MAIN="+main, "PROVEO_CLONE_REF="+ref)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("proveo_clone_checkout did not complete: %v\n%s", err, out)
	}
	return string(out)
}

// wtPath is where the host's linked worktree lives; inside the VM nothing is
// there until the seed adds it.
func wtPath(t *testing.T) string { return filepath.Join(t.TempDir(), "worktrees", "dev") }

func TestCloneCheckoutAddsTheWorktreeAtItsHostPathOnItsBranch(t *testing.T) {
	t.Parallel()
	clone, tip := cloneWithBranch(t)
	wt := wtPath(t)
	out := runCloneCheckout(t, clone, wt, "feat")
	if got := gitIn(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Fatalf("worktree HEAD = %s, want feat — the agent works where the operator launched, on that branch:\n%s", got, out)
	}
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("HEAD = %s, want the worktree's tip %s", got, tip)
	}
	if up := gitIn(t, wt, "rev-parse", "--abbrev-ref", "feat@{upstream}"); up != "origin/feat" {
		t.Errorf("upstream = %s, want origin/feat", up)
	}
	if got := gitIn(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got == "feat" {
		t.Error("the main clone moved onto the worktree's branch; git refuses one branch in two worktrees")
	}
	common := gitIn(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
	want, _ := filepath.EvalSymlinks(filepath.Join(clone, ".git"))
	if got, _ := filepath.EvalSymlinks(common); got != want {
		t.Errorf("common dir = %s, want the clone's %s — teardown fetches the branch from there", common, want)
	}
}

func TestCloneCheckoutLeavesAnExistingWorktreeAlone(t *testing.T) {
	t.Parallel()
	clone, _ := cloneWithBranch(t)
	wt := wtPath(t)
	runCloneCheckout(t, clone, wt, "feat")
	gitIn(t, wt, "checkout", "-q", "-b", "agent-choice")
	runCloneCheckout(t, clone, wt, "feat")
	if got := gitIn(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "agent-choice" {
		t.Errorf("HEAD = %s — a reattached sandbox re-seeds, and must not move the agent off the branch it chose", got)
	}
}

func TestCloneCheckoutAcceptsADetachedCommit(t *testing.T) {
	t.Parallel()
	clone, tip := cloneWithBranch(t)
	wt := wtPath(t)
	runCloneCheckout(t, clone, wt, tip)
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("HEAD = %s, want detached at %s", got, tip)
	}
}

func TestCloneCheckoutNeverFailsTheSeed(t *testing.T) {
	t.Parallel()
	clone, _ := cloneWithBranch(t)
	out := runCloneCheckout(t, clone, wtPath(t), "no-such-branch")
	if !strings.Contains(out, "could not add the worktree") {
		t.Errorf("a missing ref must be reported, not swallowed:\n%s", out)
	}
	out = runCloneCheckout(t, t.TempDir(), wtPath(t), "feat")
	if !strings.Contains(out, "is not a git clone") {
		t.Errorf("a main path without a clone must be reported:\n%s", out)
	}
}

func awaitSeedCwd(t *testing.T, pwd string, env ...string) string {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "packages", "lib", "proveo-await-seed"), "--cwd")
	cmd.Dir = pwd
	cmd.Env = append(append(os.Environ(), "PWD="+pwd, "SANDBOX_VM_ID="), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("proveo-await-seed --cwd: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestAwaitSeedMovesOnlyAWorktreeCloneLaunch(t *testing.T) {
	t.Parallel()
	main, wt, other := t.TempDir(), t.TempDir(), t.TempDir()
	vars := []string{"PROVEO_CLONE_MAIN=" + main, "PROVEO_WORKDIR=" + wt}
	if got := awaitSeedCwd(t, main, vars...); got != wt {
		t.Errorf("launch in the clone went to %q, want the worktree %s", got, wt)
	}
	if got := awaitSeedCwd(t, other, vars...); got != other {
		t.Errorf("a claude started by hand in %s was moved to %q", other, got)
	}
	if got := awaitSeedCwd(t, main, "PROVEO_WORKDIR="+wt); got != main {
		t.Errorf("without a worktree clone the launch dir must stay put, got %q", got)
	}
	if got := awaitSeedCwd(t, main, "PROVEO_CLONE_MAIN="+main, "PROVEO_WORKDIR="+filepath.Join(wt, "absent")); got != main {
		t.Errorf("a worktree the seed failed to add must not strand the agent, got %q", got)
	}
}

func runCloneEnv(t *testing.T, workdir, staged string) string {
	t.Helper()
	bash := bashOrSkip(t)
	cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_clone_env; echo DONE`, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_WORKDIR="+workdir, "PROVEO_CLONE_ENV="+staged)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("proveo_clone_env did not complete: %v\n%s", err, out)
	}
	return string(out)
}

func TestCloneEnvLinksTheStagedFileAndKeepsItOutOfCommits(t *testing.T) {
	t.Parallel()
	clone, _ := cloneWithBranch(t)
	staged := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(staged, []byte("DB_URL=postgres://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCloneEnv(t, clone, staged)
	if got, err := os.Readlink(filepath.Join(clone, ".env")); err != nil || got != staged {
		t.Fatalf(".env = %q, %v; want a symlink to the read-only staged copy", got, err)
	}
	if st := gitIn(t, clone, "status", "--porcelain"); st != "" {
		t.Errorf("the linked .env shows in git status, so the teardown snapshot (`git add -A`) would commit it:\n%s", st)
	}
	runCloneEnv(t, clone, staged)
	if b, _ := os.ReadFile(filepath.Join(gitIn(t, clone, "rev-parse", "--absolute-git-dir"), "info", "exclude")); strings.Count(string(b), "/.env") != 1 {
		t.Errorf("a reattached boot must not append the exclude twice:\n%s", b)
	}
}

func TestCloneEnvNeverReplacesAnEnvTheCloneAlreadyHas(t *testing.T) {
	t.Parallel()
	clone, _ := cloneWithBranch(t)
	own := filepath.Join(clone, ".env")
	if err := os.WriteFile(own, []byte("MINE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(staged, []byte("HOST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runCloneEnv(t, clone, staged)
	if b, _ := os.ReadFile(own); string(b) != "MINE=1\n" || !strings.Contains(out, "already exists") {
		t.Errorf("an existing .env was replaced or silently kept: %q\n%s", b, out)
	}
}

func TestCloneLinksRecreatesIgnoredHostSymlinks(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	clone, _ := cloneWithBranch(t)
	target := t.TempDir()
	pairs := "_spec=" + target + "|docs/shared=" + target + "|a=" + target + "|../escape=" + target
	run := func() string {
		cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_clone_links; echo DONE`, "bash", repoRoot(t))
		cmd.Env = append(os.Environ(), "PROVEO_WORKDIR="+clone, "PROVEO_CLONE_LINKS="+pairs)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "DONE") {
			t.Fatalf("proveo_clone_links: %v\n%s", err, out)
		}
		return string(out)
	}
	run()
	for _, rel := range []string{"_spec", "docs/shared"} {
		if got, err := os.Readlink(filepath.Join(clone, rel)); err != nil || got != target {
			t.Errorf("%s = %q, %v — a gitignored symlink the mount plan resolved must exist in the clone", rel, got, err)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(clone, "a")); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("a path the clone already has was replaced by a link")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(clone), "escape")); err == nil {
		t.Error("a rel with .. escaped the workdir")
	}
	if st := gitIn(t, clone, "status", "--porcelain"); st != "" {
		t.Errorf("the relinked paths show in git status, so the teardown snapshot would commit them:\n%s", st)
	}
	run()
	b, _ := os.ReadFile(filepath.Join(clone, ".git", "info", "exclude"))
	if strings.Count(string(b), "/_spec\n") != 1 {
		t.Errorf("a reattached boot must not append the exclude twice:\n%s", b)
	}
}
