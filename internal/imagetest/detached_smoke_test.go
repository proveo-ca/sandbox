//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml, _spec/_devops/image-lineage-and-publish.puml
package imagetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

// smokeTargets pairs each target with its repository; the tag resolves per run.
var smokeTargets = []struct{ target, repo string }{
	{"cecli", "proveo/cecli"},
	{"claudecode", "proveo/claudecode"},
	{"opencode", "proveo/opencode"},
	{"cursor", "proveo/cursor"},
}

// SPEC: _spec/_plans/retire-model-bridging.puml
const smokeEnv = `# Non-secret values for detached image smoke tests only.
CECLI_MODEL=openai/gpt-4o-mini
CECLI_EDITOR_MODEL=openai/gpt-4o-mini
CECLI_WEAK_MODEL=openai/gpt-4o-mini
OPENCODE_MODEL=openai/gpt-4o-mini
OPENCODE_SMALL_MODEL=openai/gpt-4o-mini
OPENAI_API_KEY=proveo-smoke-test-key
ANTHROPIC_API_KEY=proveo-smoke-test-key
CLAUDE_CODE_OAUTH_TOKEN=proveo-smoke-test-token
`

var smokeKeepRE = regexp.MustCompile(`^(1|true|yes|on)$`)

func TestImageDetachedSmoke(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("Docker is required for detached image smoke tests.")
	}
	if r := imagetest.Docker(30*time.Second, nil, "info"); !r.OK() {
		t.Fatal("Docker is installed but the daemon is not available.")
	}

	timeout := 30 * time.Second
	if v := os.Getenv("PROVEO_DOCKER_SMOKE_TIMEOUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PROVEO_DOCKER_SMOKE_TIMEOUT=%q: want whole seconds", v)
		}
		timeout = time.Duration(n) * time.Second
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(workspace, ".env")
	if err := os.WriteFile(envFile, []byte(smokeEnv), 0o644); err != nil {
		t.Fatal(err)
	}

	var containers []string
	t.Cleanup(func() {
		if t.Failed() && smokeKeepRE.MatchString(os.Getenv("PROVEO_DOCKER_SMOKE_KEEP_FAILED")) {
			t.Logf("Keeping failed smoke containers for inspection: %s", strings.Join(containers, " "))
			return
		}
		for _, c := range containers {
			imagetest.Docker(30*time.Second, nil, "rm", "-f", c)
		}
	})

	for _, tg := range smokeTargets {
		desc := tg.target + " emitted smoke-ready log"
		image := imagetest.Resolve("", tg.repo+":latest")
		if !imagetest.Present(image) {
			t.Run(desc, func(t *testing.T) {
				t.Skipf("skip %s: neither %s:local nor %s:latest is built", tg.target, tg.repo, tg.repo)
			})
			continue
		}
		t.Logf("smoke %s using %s", tg.target, image)
		container := smokeContainerName(tg.target)
		containers = append(containers, container)
		t.Run(desc, func(t *testing.T) {
			smokeTarget(t, tg.target, image, workspace, envFile, container, timeout)
		})
	}
}

func smokeContainerName(target string) string {
	safe := regexp.MustCompile(`[^a-zA-Z0-9_.-]`).ReplaceAllString(target, "-")
	return fmt.Sprintf("proveo-smoke-%s-%d", safe, os.Getpid())
}

func smokeTarget(t *testing.T, target, image, workspace, envFile, container string, timeout time.Duration) {
	expected := "✅ PROVEO_SMOKE_READY " + target
	args := []string{
		"run", "-d",
		"--name", container,
		"--env-file", envFile,
		"-e", "PROVEO_SMOKE_TEST=1",
		"-e", "PROVEO_SMOKE_TARGET=" + target,
		"-e", "PROVEO_SMOKE_EXPECTED=" + expected,
	}
	switch target {
	case "cecli", "opencode", "claudecode":
		args = append(args, "-v", workspace+":/app", "-w", "/app")
	}
	args = append(args, image)
	if target == "cecli" {
		args = append(args, "bash", "-lc", `printf "%s\n" "$PROVEO_SMOKE_EXPECTED"; exec sleep infinity`)
	}

	t.Logf("==> %s (%s)", target, image)
	imagetest.Docker(30*time.Second, nil, "rm", "-f", container)
	if r := imagetest.Docker(imagetest.DefaultTimeout, nil, args...); !r.OK() {
		t.Fatalf("docker run: %v\n%s", r.Err, strings.TrimSpace(r.Out))
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		logs := imagetest.Docker(30*time.Second, nil, "logs", container).Out
		if strings.Contains(logs, expected) {
			return
		}
		running := imagetest.Docker(30*time.Second, nil, "inspect", "-f", "{{.State.Running}}", container)
		if strings.TrimSpace(running.Out) != "true" {
			t.Fatalf("Container exited before smoke signal: %s\n%s", container, logs)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Timed out waiting for smoke signal: %s\n%s", expected, logs)
		case <-time.After(time.Second):
		}
	}
}
