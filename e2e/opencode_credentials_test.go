//go:build e2e

// SPEC: _spec/defs/opencode/engine-local-history.puml
package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func opencodeImageVersion(t *testing.T, image string) string {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "-f", `{{index .Config.Labels "proveo.agent.version"}}`, image).Output()
	if err != nil {
		t.Fatalf("inspect OpenCode image version: %v", err)
	}
	version := strings.TrimSpace(string(out))
	if !strings.HasPrefix(version, "2.") {
		t.Fatalf("OpenCode image %s must pin v2, got %q", image, version)
	}
	return version
}

func TestOpenCodeRuntimeInImage(t *testing.T) {
	image := os.Getenv("PROVEO_E2E_OPENCODE_IMAGE")
	if image == "" {
		image = "proveo/opencode:local"
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("OpenCode image %s is unavailable", image)
	}
	opencodeImageVersion(t, image)
	cmd := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "python3",
		"--mount", "type=bind,source="+repoRootDir(t)+",target=/fixture,readonly", image,
		"-B", "/fixture/packages/lib/test_opencode_runtime.py", "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode runtime image regression failed: %v\n%s", err, out)
	}
}
