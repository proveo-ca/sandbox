//go:build e2e

// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml
package e2e

import (
	"os"
	"os/exec"
	"testing"
)

func TestOpenCodeCredentialSnapshotsInImage(t *testing.T) {
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
	cmd := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "python3",
		"--mount", "type=bind,source="+repoRootDir(t)+",target=/fixture,readonly", image,
		"-B", "/fixture/packages/lib/test_opencode_credentials.py", "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode credential snapshot image regression failed: %v\n%s", err, out)
	}
}
