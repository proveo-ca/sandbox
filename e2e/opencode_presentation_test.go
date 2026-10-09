//go:build e2e

// SPEC: _spec/defs/opencode/native-v2-integration.puml
package e2e

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOpenCodePresentationAndAutoacceptInImage(t *testing.T) {
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
	version := opencodeImageVersion(t, image)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--network", "none",
		"--entrypoint", "python3", "--mount", "type=bind,source="+repoRootDir(t)+",target=/fixture,readonly",
		"-e", "PROVEO_TEST_OPENCODE_NATIVE=/usr/local/share/npm-global/bin/opencode",
		"-e", "PROVEO_TEST_OPENCODE_VERSION="+version, image,
		"-B", "/fixture/e2e/testdata/opencode_presentation.py", "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline native presentation/YOLO verification: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
