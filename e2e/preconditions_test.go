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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"

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
//
// Unpinned it prefers :local over :latest, which is what the tag policy means:
// `mise run build` tags a local build :local, and only a publish moves :latest.
// Defaulting to :latest skipped these suites on a machine that had just built the
// thing under test — and on one that had also pulled, ran the PUBLISHED image while
// the report read as though it covered the local build.
func harnessImageName(target string) string {
	if pinned := env("PROVEO_TEST_IMAGE_"+strings.ToUpper(target), ""); pinned != "" {
		return pinned
	}
	repo := "proveo/" + target
	if ref := repo + ":" + maintain.LocalTag; imageExists(ref) {
		return ref
	}
	return repo + ":" + maintain.PublishTag
}

// imageExists reports whether the local daemon holds ref. Split out of
// dockerImagePresent so image resolution can happen outside a *testing.T.
func imageExists(ref string) bool {
	return exec.Command("docker", "image", "inspect", ref).Run() == nil
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

// requireReviewTier skips unless this host can actually carry the review tier's
// consent gate. It mirrors reviewSupported in cmd/proveo, which is the source of
// truth — and which `proveo run` already warns about on an unsupported host:
// "the consent gate cannot be reached from the inspector on this host, so every
// new connection will be DENIED without a prompt". A test driving that tier
// there cannot pass, and it fails by TIMING OUT waiting for an overlay that will
// never render — 90 seconds spent to report a host capability as a defect.
func requireReviewTier(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("--egress-mode review is linux only (GOOS=%s): the consent gate cannot be reached from the inspector", runtime.GOOS)
	}
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" && !strings.HasPrefix(h, "unix://") {
		t.Skipf("--egress-mode review needs a local docker daemon (DOCKER_HOST=%s)", h)
	}
}
