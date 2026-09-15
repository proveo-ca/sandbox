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

func TestGitIsWritableInEveryHarness(t *testing.T) {
	proveoBin := buildProveo(t)
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			requireHarness(t, name)
			repo := newTempRepo(t)
			sess := launchShell(t, proveoBin, name, repo)

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

			out, status := shellExec(t, sess, script, 60*time.Second)
			if status != 0 || !strings.Contains(out, "GIT_WRITE_OK") {
				t.Errorf("%s cannot write git history (exit %d):\n%s", name, status, out)
			}
		})
	}
}

func TestGitRunsWhenWorktreeOwnerDiffersFromRunAsUID(t *testing.T) {
	img := harnessImage(t, "opencode")
	repo := newTempRepo(t)

	run := func(bridge bool) (string, error) {
		script := "git status --porcelain 2>&1 | head -1"
		if bridge {
			script = `source /entrypoint-lib.sh 2>/dev/null
ensure_git_safe_directory /app >/dev/null 2>&1
` + script
		}
		out, err := exec.Command("docker", "run", "--rm",
			"-v", repo+"/.git:/app/.git",
			"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
			"-w", "/app", "--user", hostUIDGID(t),
			"--entrypoint", "bash", img, "-c", script).CombinedOutput()
		return string(out), err
	}

	raw, _ := run(false)
	if !strings.Contains(raw, "dubious ownership") {
		t.Skipf("this runtime uid-maps the mount, so the abort cannot be reproduced here:\n%s", raw)
	}
	bridged, err := run(true)
	if err != nil {
		t.Fatalf("bridged run failed: %v\n%s", err, bridged)
	}
	if strings.Contains(bridged, "dubious ownership") {
		t.Errorf("ensure_git_safe_directory must declare the worktree safe so git can run:\n%s", bridged)
	}
}

func workspaceMountArgs(t *testing.T, target, repo string, input ...string) []string {
	t.Helper()
	in := repo
	if len(input) > 0 && input[0] != "" {
		in = input[0]
	}
	bin := buildProveo(t)
	cmd := exec.Command(bin, "run", target, "--credentials", "forward", "--input", in, "--print")
	cmd.Env = append(os.Environ(), "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}
	fields := strings.Fields(string(out))
	var args []string
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] != "-v" {
			continue
		}
		spec := fields[i+1]
		parts := strings.Split(spec, ":")
		if len(parts) < 2 {
			continue
		}
		dst := parts[len(parts)-1]
		if dst == "ro" || dst == "rw" {
			dst = parts[len(parts)-2]
		}
		if dst == "/app" || strings.HasPrefix(dst, "/app/") ||
			strings.HasPrefix(dst, "/workspace/") ||
			dst == workspace.ContainerGitCommonDir || strings.HasPrefix(dst, workspace.ContainerGitCommonDir+"/") {
			args = append(args, "-v", spec)
		}
	}
	if !strings.Contains(string(out), "docker run") {
		t.Skipf("this host resolved %s to the sandbox backend, and the mount table this "+
			"probe reads exists only in the docker rendering — pinning the backend to "+
			"force it is banned, so the case is skipped where its subject is absent\n"+
			"--- plan ---\n%s", target, out)
	}
	return args
}

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
			sess := launchShell(t, proveoBin, target, input)

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

			out, status := shellExec(t, sess, script, 60*time.Second)
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

func scopedMountArgs(t *testing.T, target, repo, scope string) []string {
	t.Helper()
	input := repo
	if scope != "" {
		input = filepath.Join(repo, scope)
	}
	return workspaceMountArgs(t, target, repo, input)
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

			sess := launchShell(t, proveoBin, name, tree)
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

			out, status := shellExec(t, sess, script, 60*time.Second)
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
			if err := sess.SendText("exit"); err != nil {
				t.Fatal(err)
			}
			if err := sess.Enter(); err != nil {
				t.Fatal(err)
			}
			screen, exited := waitSessionExit(sess, durationEnv(t, "PROVEO_TEST_TEARDOWN_TIMEOUT", 5*time.Minute))
			if !exited {
				t.Fatalf("proveo did not exit after the shell left\n%s", screen)
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
