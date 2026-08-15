//go:build e2e

// SPEC: _spec/internal/workspace/subdir-scope-mounts.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A subdir scope must still carry the repo's shared directories. _spec is the
// one that matters most here: agents are asked to read AND maintain the specs,
// and plantuml is on the floor precisely so they can verify what they edited —
// none of which works if _spec never reaches the container. The mount plan is
// unit-tested; this asserts the container can actually reach it.
func TestScopeCarriesRootSpecDir(t *testing.T) {
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
			args = append(args, "-w", "/app", "--user", hostUIDGID(t),
				"--entrypoint", "bash", img, "-c", `
[ -d /app/_spec ] || { echo "MISSING_SPEC_DIR"; exit 1; }
grep -q x /app/_spec/c.puml || { echo "SPEC_UNREADABLE"; exit 1; }
printf 'edited by the agent\n' > /app/_spec/written.puml || { echo "SPEC_READONLY"; exit 1; }
rm -f /app/_spec/written.puml
[ -d /app/apps/web ] || { echo "MISSING_SCOPE"; exit 1; }
echo "SPEC_OK"`)

			out, err := exec.Command("docker", args...).CombinedOutput()
			s := string(out)
			if err != nil || !strings.Contains(s, "SPEC_OK") {
				t.Fatalf("%s scope cannot reach a writable _spec: %v\n%s", tc.mode, err, s)
			}
		})
	}
}

// The scope must still NARROW the tree — otherwise rootDirs would just be a
// roundabout way of mounting everything, and the git scoping in
// scope_git_worktree would have nothing to hide.
func TestSubdirScopeOmitsUnmountedPaths(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)
	repo := newTempMonorepo(t)

	args := []string{"run", "--rm"}
	args = append(args, scopedMountArgs(t, target, repo, "apps")...)
	args = append(args, "-w", "/app", "--user", hostUIDGID(t),
		"--entrypoint", "bash", img, "-c", `
[ -e /app/libs ] && { echo "LIBS_PRESENT"; exit 1; }
[ -e /app/unmounted ] && { echo "UNMOUNTED_PRESENT"; exit 1; }
echo "NARROWED_OK"`)

	out, err := exec.Command("docker", args...).CombinedOutput()
	s := string(out)
	if err != nil || !strings.Contains(s, "NARROWED_OK") {
		t.Fatalf("a subdir scope must not carry unrelated repo paths: %v\n%s", err, s)
	}
}

// newTempWorktree reproduces the shape that broke: a linked git worktree, whose
// .git is a FILE pointing under the main repo, with .env symlinked back to the
// main checkout. Both references escape the mounted tree.
func newTempWorktree(t *testing.T) (worktree string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "monorepo")
	wt := filepath.Join(base, "worktrees", "hotfix")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(main, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(main, "init", "-q", ".")
	run(main, "config", "user.email", "e2e@proveo.test")
	run(main, "config", "user.name", "proveo e2e")
	write(".gitignore", "node_modules\n.env\n")
	write(".env", "SECRET=from-monorepo\n")
	write("package.json", `{"name":"m","scripts":{"test":"echo t"}}`+"\n")
	run(main, "add", "-A")
	run(main, "commit", "-q", "-m", "seed")
	run(main, "worktree", "add", "-q", wt, "-b", "hotfix")
	if err := os.Symlink("../../monorepo/.env", filepath.Join(wt, ".env")); err != nil {
		t.Fatal(err)
	}
	return wt
}

// A linked worktree must behave like any other checkout. Its .git is a pointer
// FILE to a path under the main repo, and its .env a symlink out of the tree —
// neither of which is reachable from a container unless proveo carries them in.
func TestWorktreeWorkspaceIsFullyUsable(t *testing.T) {
	const target = "claudecode"
	img := harnessImage(t, target)
	wt := newTempWorktree(t)

	args := []string{"run", "--rm"}
	args = append(args, workspaceMountArgs(t, target, wt)...)
	args = append(args, worktreeEnvArgs(t, target, wt)...)
	args = append(args,
		"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
		"-w", "/workspace/input", "--user", hostUIDGID(t),
		"--entrypoint", "bash", img, "-c", `
source /entrypoint-lib.sh 2>/dev/null || true
ensure_git_safe_directory "$PWD" >/dev/null 2>&1 || true
grep -q from-monorepo .env || { echo "ENV_UNREACHABLE"; exit 1; }
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "NOT_A_REPO"; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = hotfix ] || { echo "WRONG_BRANCH"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "DIRTY: $(git status --porcelain | head -2)"; exit 1; }
proveo-entrypoint verify "$PWD" 2>/dev/null | grep -q . || { echo "NO_VERIFY_COMMANDS"; exit 1; }
touch CLAUDE.md.probe 2>/dev/null || { echo "WORKSPACE_READONLY"; exit 1; }
rm -f CLAUDE.md.probe
echo "WORKTREE_OK"`)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if s := string(out); err != nil || !strings.Contains(s, "WORKTREE_OK") {
		t.Fatalf("worktree workspace unusable: %v\n%s", err, s)
	}
}

func worktreeEnvArgs(t *testing.T, target, input string) []string {
	t.Helper()
	bin := buildProveo(t)
	cmd := exec.Command(bin, "run", target, "--input", input, "--print")
	cmd.Env = append(os.Environ(), "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}
	var args []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "GIT_DIR=") || strings.HasPrefix(f, "GIT_WORK_TREE=") {
			args = append(args, "-e", f)
		}
	}
	return args
}

// The claudecode entrypoint must operate on the directory that actually holds
// the workspace. It used to work in /workspace — the image's own directory, one
// level above the mount — so verify detection scanned an empty tree and the
// CLAUDE.md seed failed with a misleading "workspace may be read-only".
// Runs the REAL entrypoint (no override) so the cwd it picks is what is tested.
func TestClaudecodeEntrypointOperatesOnTheInputDir(t *testing.T) {
	const target = "claudecode"
	img := harnessImage(t, target)
	wt := newTempWorktree(t)

	args := []string{"run", "--rm", "--name", "proveo-cwd-probe-" + t.Name()}
	args = append(args, workspaceMountArgs(t, target, wt)...)
	args = append(args, worktreeEnvArgs(t, target, wt)...)
	args = append(args,
		"-v", entrypointLibPath(t)+":/entrypoint-lib.sh:ro",
		// The image bakes its own entrypoint; run the working-tree one so this tests
		// current source rather than whenever the image was last built. Invoked via
		// bash because the tracked file carries no exec bit.
		"-v", filepath.Join(repoRootDir(t), "defs/claudecode/mcp/entrypoint.sh")+":/proveo-ep.sh:ro",
		"--user", hostUIDGID(t),
		"-e", "PROVEO_SMOKE_TEST=1", "-e", "PROVEO_SMOKE_TARGET=claudecode",
		"--entrypoint", "bash", img, "/proveo-ep.sh")

	cmd := exec.Command("docker", args...)
	done := make(chan struct{})
	var out []byte
	go func() { out, _ = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		_ = exec.Command("docker", "rm", "-f", "proveo-cwd-probe-"+t.Name()).Run()
		<-done
	}
	s := string(out)

	if !strings.Contains(s, "PROVEO_SMOKE_READY") {
		t.Fatalf("entrypoint did not reach smoke readiness:\n%s", s)
	}
	if strings.Contains(s, "Could not seed CLAUDE.md") {
		t.Errorf("entrypoint seeded into a directory it cannot write — it is not working "+
			"in the mounted input dir:\n%s", s)
	}
	if !verifyCommandsListed(s) {
		t.Errorf("Verification Commands section is empty — the entrypoint scanned a directory "+
			"that holds no project markers:\n%s", s)
	}
}

// verifyCommandsListed reports whether at least one command follows the
// "Verification Commands" banner before the section closes.
func verifyCommandsListed(out string) bool {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "Verification Commands") {
			continue
		}
		for _, next := range lines[i+1:] {
			t := strings.TrimSpace(next)
			if t == "" || strings.HasPrefix(t, "─") {
				break
			}
			return true
		}
	}
	return false
}
