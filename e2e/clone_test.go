//go:build e2e

// SPEC: _spec/packages/lib/dependency-trees.puml, _spec/internal/sbx/clone-workspace.puml

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

var machO = []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c}

// TestCloneLeavesTheHostTreeAlone is the regression guard for the ping-pong.
func TestCloneLeavesTheHostTreeAlone(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	// node_modules is gitignored, so it is exactly the kind of tree a clone must
	// leave behind — and the kind an in-place run would overwrite.
	dep := filepath.Join(work, "node_modules", "foo", "a.node")
	mkdirAll(t, filepath.Dir(dep))
	writeFile(t, dep, machO)
	writeFile(t, filepath.Join(work, "package.json"), []byte(`{"name":"t","packageManager":"pnpm@10.33.0"}`))
	writeFile(t, filepath.Join(work, ".gitignore"), []byte("node_modules/\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-cloneguard-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})

	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--clone", "--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute)
	w := newWatcher(t, sess)
	w.until("the agent shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	script := `printf 'origin=%s\ndeps=%s\n' ` +
		`"$(git remote get-url origin 2>/dev/null)" ` +
		`"$(test -e node_modules && echo present || echo absent)"`
	out, status := shellExec(t, sess, script, 60*time.Second)
	if status != 0 {
		t.Fatalf("probe exited %d:\n%s", status, out)
	}
	if !strings.Contains(out, "origin=/run/sandbox/source") {
		t.Errorf("workspace is not a clone of the host repo:\n%s", out)
	}
	if !strings.Contains(out, "deps=absent") {
		t.Errorf("the untracked macOS tree crossed into the clone, so the reinstall "+
			"will run and write back:\n%s", out)
	}

	// The promise itself: the HOST file, untouched by the run.
	after, err := os.ReadFile(dep)
	if err != nil {
		t.Fatalf("the sandbox removed a host file --clone promised not to touch: %v", err)
	}
	if !bytes.Equal(after, machO) {
		t.Errorf("the sandbox rewrote the host dependency tree: want Mach-O magic %x, got %x",
			machO, after)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitInit makes work a repository with one commit: sbx clones from HEAD, so a
// repo with no commit has nothing to clone.
func gitInit(t *testing.T, work string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=e2e@proveo.test", "-c", "user.name=e2e", "commit", "-qm", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
