//go:build e2e

// SPEC: _spec/internal/workspace/git-mount-by-scope.puml, _spec/_paradigms/git-identity.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/workspace"
)

var harnessWorkdir = map[string]string{
	"opencode":   "/app",
	"cursor":     "/app",
	"cecli":      "/app",
	"claudecode": "/workspace/input",
}

func newTempRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, t.TempDir())
}

// newTempMonorepo seeds a repo with a sub-project plus paths that a subdir scope
// will NOT mount, which is what makes the whole-repo index diverge from the
// container's partial worktree.
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

// Every harness must be able to READ history and WRITE to .git — agents commit,
// amend and branch as ordinary work. Runs a full cycle against a throwaway repo
// so nothing touches the developer's tree.
func TestGitIsWritableInEveryHarness(t *testing.T) {
	for _, name := range toolchainHarnesses {
		t.Run(name, func(t *testing.T) {
			img := harnessImage(t, name)
			repo := newTempRepo(t)
			wd := harnessWorkdir[name]
			if wd == "" {
				t.Fatalf("no workdir mapped for harness %q", name)
			}

			script := `set -e
source /entrypoint-lib.sh 2>/dev/null || true
bridge_git_identity "$PWD" 2>/dev/null || true
git rev-parse --is-inside-work-tree >/dev/null
before=$(git rev-parse HEAD)
git add tracked.txt
git commit -q -m "written from inside the harness"
after=$(git rev-parse HEAD)
[ "$before" != "$after" ] || { echo "HEAD did not advance"; exit 1; }
git branch proveo-write-probe >/dev/null
git branch -D proveo-write-probe >/dev/null
echo "GIT_WRITE_OK"`

			args := []string{"run", "--rm"}
			args = append(args, workspaceMountArgs(t, name, repo)...)
			args = append(args,
				"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
				"-w", wd, "--user", hostUIDGID(t),
				"-e", "GIT_AUTHOR_NAME=proveo e2e", "-e", "GIT_AUTHOR_EMAIL=e2e@proveo.test",
				"-e", "GIT_COMMITTER_NAME=proveo e2e", "-e", "GIT_COMMITTER_EMAIL=e2e@proveo.test",
				"--entrypoint", "bash", img, "-c", script)
			out, err := exec.Command("docker", args...).CombinedOutput()

			if err != nil || !strings.Contains(string(out), "GIT_WRITE_OK") {
				t.Errorf("%s cannot write git history: %v\n%s", name, err, out)
			}
		})
	}
}

// Regression guard for the ownership abort. Under a subdir scope /app is the
// IMAGE's directory rather than a bind mount, so its owner differs from the
// run-as uid and git refuses outright with "dubious ownership" until
// ensure_git_safe_directory declares it safe.
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

// workspaceMountArgs asks the real planner what it would mount for this harness
// against repo, and returns only the workspace binds. Going through `--print`
// rather than hand-writing `-v repo:/app` is what makes this an assertion about
// proveo: a harness that pinned .git read-only would emit that override here.
func workspaceMountArgs(t *testing.T, target, repo string, input ...string) []string {
	t.Helper()
	in := repo
	if len(input) > 0 && input[0] != "" {
		in = input[0]
	}
	bin := buildProveo(t)
	// --credentials forward is load-bearing, not incidental: it is the only posture
	// that carries the project .env INTO the container. Under the default broker the
	// credential policy masks that path with /dev/null instead, so the escaping-.env
	// mount these probes assert would not exist at all.
	cmd := exec.Command(bin, "run", target, "--credentials", "forward", "--input", in, "--print")
	cmd.Env = append(os.Environ(), "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}
	// Filter by CONTAINER destination, not host prefix: a worktree's shared .git
	// and an escaping .env symlink both resolve to paths OUTSIDE the workspace,
	// so a host-prefix filter silently drops the very mounts under test.
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
	if len(args) == 0 {
		t.Fatalf("no workspace mounts planned for %s against %s:\n%s", target, repo, out)
	}
	return args
}

// Git must be usable in BOTH scope modes. At the repo root that is nearly free;
// under a subdir scope the container's worktree root is /app while only part of
// the repo is mounted there, so without scope_git_worktree git reports every
// unmounted tracked path as deleted — and `git commit -a` would commit those
// deletions.
func TestGitIsUsableInEveryScopeMode(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)

	for _, tc := range []struct {
		mode  string
		scope string
	}{
		{"root", ""},
		{"subproject", "apps"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			repo := newTempMonorepo(t)
			args := []string{"run", "--rm"}
			args = append(args, scopedMountArgs(t, target, repo, tc.scope)...)
			if tc.scope != "" {
				args = append(args, "-e", "PROVEO_SCOPE_REL="+tc.scope)
			}
			args = append(args,
				"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
				"-w", "/app", "--user", hostUIDGID(t),
				"-e", "GIT_AUTHOR_NAME=proveo e2e", "-e", "GIT_AUTHOR_EMAIL=e2e@proveo.test",
				"-e", "GIT_COMMITTER_NAME=proveo e2e", "-e", "GIT_COMMITTER_EMAIL=e2e@proveo.test",
				"--entrypoint", "bash", img, "-c", `
source /entrypoint-lib.sh 2>/dev/null || true
ensure_git_safe_directory "$PWD" >/dev/null 2>&1 || true
scope_git_worktree "$PWD" >/dev/null 2>&1 || true
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "GIT_UNUSABLE"; exit 1; }
echo "PHANTOM=$(git status --porcelain | grep -c '^ D' || true)"
git log --oneline >/dev/null || { echo "LOG_FAILED"; exit 1; }
target=$(git ls-files | head -1)
printf 'edit\n' >> "$target"
git add "$target" && git commit -q -m scoped || { echo "COMMIT_FAILED"; exit 1; }
echo "HEAD=$(git rev-parse --short HEAD)"
echo "GIT_SCOPE_OK"`)

			out, err := exec.Command("docker", args...).CombinedOutput()
			s := string(out)
			if err != nil || !strings.Contains(s, "GIT_SCOPE_OK") {
				t.Fatalf("git unusable in %s scope: %v\n%s", tc.mode, err, s)
			}
			if !strings.Contains(s, "PHANTOM=0") {
				t.Errorf("%s scope: git reports unmounted paths as deleted — `git commit -a` "+
					"would commit those deletions\n%s", tc.mode, s)
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

// A LINKED worktree is the hard case: its .git is a FILE pointing into the main
// repo, and two pointer files hold HOST paths that do not exist in the
// container. Left incoherent, git marks the worktree prunable — so a routine
// `git worktree prune` (reachable through gc) deletes the host's registration,
// and any tool doing its own discovery fails outright. Asserts the container
// view is coherent, writes land, and the HOST worktree survives intact.
func TestGitWorktreeLinkageIsCoherentAndHostSafe(t *testing.T) {
	for _, name := range []string{"claudecode", "opencode"} { // input-output and app layouts
		t.Run(name, func(t *testing.T) {
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

			img := harnessImage(t, name)
			wd := harnessWorkdir[name]
			script := `set -e
source /entrypoint-lib.sh 2>/dev/null || true
ensure_git_safe_directory "$PWD" 2>/dev/null || true
[ -z "${GIT_DIR:-}" ] || { echo "GIT_DIR is pinned; the overlay should make that unnecessary"; exit 1; }
git rev-parse --is-inside-work-tree >/dev/null
# a tool that does its own discovery rather than inheriting the env
env -u GIT_DIR -u GIT_WORK_TREE git status --short >/dev/null
# a sibling repo must resolve to ITSELF, not to this worktree's admin dir
mkdir -p /tmp/other && git -C /tmp/other init -q
[ "$(git -C /tmp/other rev-parse --git-dir)" = ".git" ] || { echo "sibling repo captured"; exit 1; }
# prune must be a no-op: the chain resolves, so nothing looks stale
git worktree prune
ls /proveo-git/worktrees/ >/dev/null || { echo "prune destroyed the admin dir"; exit 1; }
git add tracked.txt
git commit -q -m "written from inside a linked worktree"
echo "WORKTREE_OK"`

			args := []string{"run", "--rm"}
			args = append(args, workspaceMountArgs(t, name, tree)...)
			args = append(args,
				"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
				"-w", wd, "--user", hostUIDGID(t),
				"-e", "GIT_AUTHOR_NAME=proveo e2e", "-e", "GIT_AUTHOR_EMAIL=e2e@proveo.test",
				"-e", "GIT_COMMITTER_NAME=proveo e2e", "-e", "GIT_COMMITTER_EMAIL=e2e@proveo.test",
				"--entrypoint", "bash", img, "-c", script)
			out, err := exec.Command("docker", args...).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "WORKTREE_OK") {
				t.Fatalf("%s worktree linkage is not usable: %v\n%s", name, err, out)
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
			// The host worktree still works AND sees what the container committed.
			gitIn(t, tree, "status", "--short")
			log, err := exec.Command("git", "-C", tree, "log", "--oneline", "-1").Output()
			if err != nil {
				t.Fatalf("host worktree is broken after the run: %v", err)
			}
			if !strings.Contains(string(log), "written from inside a linked worktree") {
				t.Errorf("host worktree does not see the container's commit: %q", log)
			}
		})
	}
}
