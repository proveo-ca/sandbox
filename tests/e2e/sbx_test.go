//go:build e2e

// SPEC: _spec/_experiments/docker-sandbox.puml
//
// Sandbox-backend e2e guards. The sbx path is probed, not assumed: when the
// Docker Sandboxes CLI is absent (the common CI case) everything here skips,
// mirroring the "docker not available" guards in toolchain_test.go.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// sbxAvailable reports whether the host can run the sandbox backend.
func sbxAvailable() bool {
	ok, _ := sbx.Available()
	return ok
}

// pathWithoutSbx builds a PATH string with dir removed, so a run cannot resolve
// the sbx CLI — used to exercise the fallback branch deterministically even on
// hosts that have it installed.
func pathWithoutSbx(t *testing.T) []string {
	t.Helper()
	var parts []string
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(p, sbx.Binary)); err == nil {
			continue // this PATH entry resolves the sbx CLI; drop it
		}
		parts = append(parts, p)
	}
	return []string{"PATH=" + strings.Join(parts, string(os.PathListSeparator))}
}

func printOnlyRun(t *testing.T, workdir string, extraEnv []string, target string) string {
	t.Helper()
	proveoBin := buildProveo(t)
	cmd := exec.Command(proveoBin, "run", target, "--print")
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print-only: %v\n%s", target, err, out)
	}
	return string(out)
}

func TestSandboxBackendPrintOnlyRendersSbx(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	work := t.TempDir()
	out := printOnlyRun(t, work, nil, "claudecode")
	if !strings.Contains(out, "# agent") || !strings.Contains(out, "sbx run ") {
		t.Errorf("print-only should render the sbx invocation, got:\n%s", out)
	}
	if strings.Contains(out, "\ndocker run") {
		t.Errorf("sbx backend selected but docker argv rendered:\n%s", out)
	}
}

func TestSandboxBackendFallsBackToDockerWhenSbxAbsent(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	work := t.TempDir()
	out := printOnlyRun(t, work, pathWithoutSbx(t), "claudecode")
	if !strings.Contains(out, "falling back to docker+egress") {
		t.Errorf("expected a fallback notice, got:\n%s", out)
	}
	if !strings.Contains(out, "docker run") {
		t.Errorf("fallback must render the docker argv, got:\n%s", out)
	}
}
