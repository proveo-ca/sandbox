//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml, _spec/tests/00-testing-overview.puml

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"

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

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
}

func harnessImageName(target string) string {
	if pinned := env("PROVEO_TEST_IMAGE_"+strings.ToUpper(target), ""); pinned != "" {
		return pinned
	}
	// Resolve it the way a run does, not the way a test finds it convenient:
	// proveo takes :local only when it is NEWER than :latest, so a stale local
	// build is what the suite would certify and what no operator would run.
	ref, _ := maintain.ResolveImage("proveo/"+target+":"+maintain.PublishTag, imageCreated)
	return ref
}

func imageCreated(ref string) (time.Time, bool) {
	out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Created}}", ref).Output()
	if err != nil {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// imageExists reports whether the local daemon holds ref. Split out of
// dockerImagePresent so image resolution can happen outside a *testing.T.
func imageExists(ref string) bool {
	return exec.Command("docker", "image", "inspect", ref).Run() == nil
}

func harnessImage(t *testing.T, target string) string {
	t.Helper()
	requireDocker(t)
	sweepSandboxesAfter(t)
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
