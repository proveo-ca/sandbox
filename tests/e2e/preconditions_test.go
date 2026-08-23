//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml, _spec/tests/00-testing-overview.puml

// Layer 4 preconditions. The `//go:build e2e` tag IS the opt-in — a suite that
// compiles under it must decide for ITSELF whether this host can run it, so the
// only reason to skip is a MISSING PREREQUISITE (PTY driver, container backend,
// harness image, credential), never an unset ceremonial flag. A green run with
// every test skipped is then an honest statement about the host, and one that
// `go test -v` prints the reason for.
package e2e

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// requireTmux skips unless the PTY driver every harness run needs is installed.
func requireTmux(t *testing.T) {
	t.Helper()
	if !tmux.Available() {
		t.Skip("tmux not installed (brew install tmux)")
	}
}

// requireDocker skips unless a docker CLI is on PATH. It deliberately checks the
// CLI rather than the daemon: every suite here shells out to `docker`, and the
// endpoint itself (context or DOCKER_HOST) is resolved per-run by dockerHost.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
}

// harnessImageName is target's image reference, overridable per target with
// PROVEO_TEST_IMAGE_<TARGET>.
func harnessImageName(target string) string {
	return env("PROVEO_TEST_IMAGE_"+strings.ToUpper(target), "proveo/"+target+":latest")
}

// harnessImage skips unless docker is present AND target's image is built
// locally, and returns the image. Building it is a maintainer action
// (mise run build <target>), so a missing image is a skip, not a failure.
func harnessImage(t *testing.T, target string) string {
	t.Helper()
	requireDocker(t)
	image := harnessImageName(target)
	if !dockerImagePresent(t, image) {
		t.Skipf("harness image %s not built (mise run build %s)", image, target)
	}
	return image
}

// requireHarness is the whole Layer 4 floor for one target: PTY driver,
// container backend, and the image the run mounts into.
func requireHarness(t *testing.T, target string) string {
	t.Helper()
	requireTmux(t)
	return harnessImage(t, target)
}
