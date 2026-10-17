//go:build e2e

// SPEC: _spec/internal/workspace/git-mount-by-scope.puml, _spec/_paradigms/git-identity.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/workspace"
)

func newTempRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, t.TempDir())
}

func newTempMonorepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"apps/web/index.ts", "_spec/c.puml", "libs/core/core.go", "unmounted/deep/f.txt"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return seedRepo(t, dir)
}

func seedRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "e2e@proveo.test"},
		{"config", "user.name", "proveo e2e"},
		{"add", "-A"},
		{"commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hostUIDGID(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		t.Fatal(err)
	}
	gout, err := exec.Command("id", "-g").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out)) + ":" + strings.TrimSpace(string(gout))
}

// TestGitIsWritableInEveryHarness drives a real `proveo run <target>` through
// its kit per harness and probes it with `sbx exec`, so the git identity
// bridging under test is the run's own seed, not a manual replica of it.
func TestGitIsWritableInEveryHarness(t *testing.T) {
	proveoBin := buildProveo(t)
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			requireHarness(t, name)
			repo := newTempRepo(t)
			k := launchKit(t, proveoBin, name, repo, nil)

			script := `export GIT_AUTHOR_NAME="proveo e2e" GIT_AUTHOR_EMAIL=e2e@proveo.test
export GIT_COMMITTER_NAME="proveo e2e" GIT_COMMITTER_EMAIL=e2e@proveo.test
git rev-parse --is-inside-work-tree >/dev/null
before=$(git rev-parse HEAD)
printf 'v1\n' > written-inside.txt
git add written-inside.txt
git commit -q -m "written from inside the harness"
after=$(git rev-parse HEAD)
[ "$before" != "$after" ] || { echo "HEAD did not advance"; exit 1; }
git branch proveo-write-probe >/dev/null
git branch -D proveo-write-probe >/dev/null
echo "GIT_WRITE_OK"`

			out, status := k.exec(script, 60*time.Second)
			if status != 0 || !strings.Contains(out, "GIT_WRITE_OK") {
				t.Errorf("%s cannot write git history (exit %d):\n%s", name, status, out)
			}
		})
	}
}

// TestGitIsUsableInEveryScopeMode launches a real `proveo run opencode`
// per scope mode, pointing --input at the scoped subdirectory itself
// — the subdir-scope detection and worktree overlay are the run's own
// (subdir-scope-mounts.puml), not a manually invoked `scope_git_worktree`.
func TestGitIsUsableInEveryScopeMode(t *testing.T) {
	const target = "opencode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	for _, tc := range []struct {
		mode  string
		scope string
	}{
		{"root", ""},
		{"subproject", "apps"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			repo := newTempMonorepo(t)
			input := repo
			if tc.scope != "" {
				input = filepath.Join(repo, tc.scope)
			}
			k := launchKit(t, proveoBin, target, input, nil)

			script := `export GIT_AUTHOR_NAME="proveo e2e" GIT_AUTHOR_EMAIL=e2e@proveo.test
export GIT_COMMITTER_NAME="proveo e2e" GIT_COMMITTER_EMAIL=e2e@proveo.test
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "GIT_UNUSABLE"; exit 1; }
echo "PHANTOM=$(git status --porcelain | grep -c '^ D' || true)"
git log --oneline >/dev/null || { echo "LOG_FAILED"; exit 1; }
target=$(git ls-files | head -1)
printf 'edit\n' >> "$target"
git add "$target" && git commit -q -m scoped || { echo "COMMIT_FAILED"; exit 1; }
echo "HEAD=$(git rev-parse --short HEAD)"
echo "GIT_SCOPE_OK"`

			out, status := k.exec(script, 60*time.Second)
			if status != 0 || !strings.Contains(out, "GIT_SCOPE_OK") {
				t.Fatalf("git unusable in %s scope (exit %d):\n%s", tc.mode, status, out)
			}
			if !strings.Contains(out, "PHANTOM=0") {
				t.Errorf("%s scope: git reports unmounted paths as deleted — `git commit -a` "+
					"would commit those deletions\n%s", tc.mode, out)
			}
		})
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestGitWorktreeLinkageIsCoherentAndHostSafe(t *testing.T) {
	proveoBin := buildProveo(t)
	for _, name := range []string{"claudecode", "opencode"} { // one layout, two workspace shapes
		t.Run(name, func(t *testing.T) {
			requireHarness(t, name)
			// Keep the generated pointer files out of the developer's ~/.proveo.
			t.Setenv("PROVEO_HOME", t.TempDir())

			base := t.TempDir()
			main := filepath.Join(base, "main")
			if err := os.MkdirAll(main, 0o755); err != nil {
				t.Fatal(err)
			}
			seedRepo(t, main)
			tree := filepath.Join(base, "wt")
			gitIn(t, main, "worktree", "add", "-q", tree, "-b", "proveo-e2e")
			// seedRepo's untracked file lives in main; the worktree is a clean
			// checkout, so give it its own file for the container to commit.
			if err := os.WriteFile(filepath.Join(tree, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if fi, err := os.Lstat(filepath.Join(tree, ".git")); err != nil || fi.IsDir() {
				t.Fatalf("expected a linked worktree whose .git is a file: %v", err)
			}
			pointerBefore, err := os.ReadFile(filepath.Join(tree, ".git"))
			if err != nil {
				t.Fatal(err)
			}

			k := launchKit(t, proveoBin, name, tree, nil)
			script := `export GIT_AUTHOR_NAME="proveo e2e" GIT_AUTHOR_EMAIL=e2e@proveo.test
export GIT_COMMITTER_NAME="proveo e2e" GIT_COMMITTER_EMAIL=e2e@proveo.test
[ -z "${GIT_DIR:-}" ] || { echo "GIT_DIR is pinned; the overlay should make that unnecessary"; exit 1; }
git rev-parse --is-inside-work-tree >/dev/null
# a tool that does its own discovery rather than inheriting the env
env -u GIT_DIR -u GIT_WORK_TREE git status --short >/dev/null
# a sibling repo must resolve to ITSELF, not to this worktree's admin dir
mkdir -p /tmp/other && git -C /tmp/other init -q
[ "$(git -C /tmp/other rev-parse --git-dir)" = ".git" ] || { echo "sibling repo captured"; exit 1; }
# prune must be a no-op: the chain resolves, so nothing looks stale
git worktree prune
if [ -d ` + workspace.ContainerGitCommonDir + ` ]; then
  ls ` + workspace.ContainerGitCommonDir + `/worktrees/ >/dev/null || { echo "prune destroyed the admin dir"; exit 1; }
fi
git rev-parse --git-dir >/dev/null 2>&1 || { echo "prune broke git resolution"; exit 1; }
printf 'v1\n' > written-inside.txt
git add written-inside.txt
git commit -q -m "written from inside a linked worktree"
echo "WORKTREE_OK"`

			out, status := k.exec(script, 60*time.Second)
			if status != 0 || !strings.Contains(out, "WORKTREE_OK") {
				t.Fatalf("%s worktree linkage is not usable (exit %d):\n%s", name, status, out)
			}

			// The host pointer must be untouched, or the operator's own worktree breaks.
			pointerAfter, err := os.ReadFile(filepath.Join(tree, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			if string(pointerAfter) != string(pointerBefore) {
				t.Errorf("host .git pointer was rewritten:\n before: %q\n after:  %q", pointerBefore, pointerAfter)
			}
			if strings.Contains(string(pointerAfter), workspace.ContainerGitCommonDir) {
				t.Errorf("host .git now holds a container path: %q", pointerAfter)
			}
			gitIn(t, tree, "status", "--short")
			if screen, ended := k.end(); !ended {
				t.Fatalf("proveo did not finish its teardown after the session closed\n%s", screen)
			}
			if !hostCarriesCommit(t, tree, "written from inside a linked worktree") {
				t.Errorf("the container's commit reached the host by no route — neither the "+
					"worktree's own history nor refs/proveo/*:\n%s", hostRefLog(t, tree))
			}
		})
	}
}

func hostCarriesCommit(t *testing.T, dir, subject string) bool {
	t.Helper()
	return strings.Contains(hostRefLog(t, dir), subject)
}

func hostRefLog(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--oneline", "--all",
		"--glob=refs/proveo/*").CombinedOutput()
	if err != nil {
		t.Fatalf("host worktree is broken after the run: %v\n%s", err, out)
	}
	return string(out)
}
