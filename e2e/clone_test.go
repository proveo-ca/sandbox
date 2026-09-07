//go:build e2e

// SPEC: _spec/packages/lib/dependency-trees.puml
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var machO = []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c}

// TestCloneLeavesTheHostTreeAlone is the regression guard for the ping-pong.
func TestCloneLeavesTheHostTreeAlone(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")
	freshTemplate(t, img)

	work := t.TempDir()
	// node_modules is gitignored, so it is exactly the kind of tree a clone must
	// leave behind — and the kind an in-place run would overwrite.
	dep := filepath.Join(work, "node_modules", "foo", "a.node")
	mkdirAll(t, filepath.Dir(dep))
	writeFile(t, dep, machO)
	writeFile(t, filepath.Join(work, "package.json"), []byte(`{"name":"t","packageManager":"pnpm@10.33.0"}`))
	writeFile(t, filepath.Join(work, ".gitignore"), []byte("node_modules/\n"))
	gitInit(t, work)

	name := "clone-guard-" + filepath.Base(work)
	create := exec.Command("sbx", "create", "--name", name, "--clone", "-t", img, "claude", work)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("sbx create --clone: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	probe := exec.Command("sbx", "exec", name, "--", "sh", "-c",
		"cd "+work+" && printf 'origin=%s\\ndeps=%s\\n' "+
			"\"$(git remote get-url origin 2>/dev/null)\" "+
			"\"$(test -e node_modules && echo present || echo absent)\"")
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "origin=/run/sandbox/source") {
		t.Errorf("workspace is not a clone of the host repo:\n%s", got)
	}
	if !strings.Contains(got, "deps=absent") {
		t.Errorf("the untracked macOS tree crossed into the clone, so the reinstall "+
			"will run and write back:\n%s", got)
	}

	// The promise itself.
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
