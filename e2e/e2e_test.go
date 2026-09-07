//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/tests/41-agent-e2e-sequence.puml

// Package e2e is the agent end-to-end suite.
package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestPromptfulE2E runs a real harness image with a LOCAL model, drives the
// agent NON-INTERACTIVELY through ONE deterministic task, and asserts the
// SIDE EFFECTS on the host rather than the model's prose:
func TestPromptfulE2E(t *testing.T) {
	requireTmux(t)
	requireDocker(t)
	target := env("PROVEO_TEST_TARGET", "opencode")
	image := env("PROVEO_TEST_IMAGE", harnessImageName(target))
	if !dockerImagePresent(t, image) {
		t.Skipf("harness image %s not built (mise run build %s)", image, target)
	}
	model := env("PROVEO_TEST_LOCAL_MODEL", "gemma4")
	if !ollamaHasModel(model) {
		t.Skipf("Ollama model %q not available on the host", model)
	}

	proveoBin := buildProveo(t)

	// Mount a COPY of the sample monorepo so the tracked sample stays pristine;
	// the agent edits the copy and we assert the host-side side effects.
	work := copySampleWorkspace(t)
	removeWorkspaceEnv(t, work)
	mustRun(t, work, "git", "init", "-q", ".")
	sampleAnchor := firstLine(t, filepath.Join(work, "README.md"))

	const marker = "BANANA-E2E-OK"
	// One deterministic shell command drives all three effects: curl (web scrape),
	// head of a mounted sample file (mount proof), and a marker file (side effect).
	task := "Use your bash tool to run exactly this one command and then stop: " +
		"curl -sS https://example.com -o SCRAPED.html && head -1 README.md > FROM_SAMPLE.txt && printf %s " + marker + " > DONE.txt"

	sess := tmux.New(fmt.Sprintf("proveo-e2e-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	if err := sess.Start(200, 50, "env", "PROVEO_WIZARD=off", proveoBin, "run", target,
		"--egress-mode", "open", "--credentials", "forward",
		"--local-model", model, "--input", work, "--scope", ".",
		"--", "run", "--auto", "--agent", "build", task); err != nil {
		t.Fatalf("start session: %v", err)
	}

	deadline := time.Now().Add(4 * time.Minute)
	for {
		mounted := strings.Contains(readIn(work, "FROM_SAMPLE.txt"), sampleAnchor)  // samples/ mounted
		changed := strings.Contains(readIn(work, "DONE.txt"), marker)               // files changed
		scraped := strings.Contains(readIn(work, "SCRAPED.html"), "Example Domain") // web scraped
		if mounted && changed && scraped {
			return // all four E2E steps verified
		}
		if time.Now().After(deadline) {
			screen, _ := sess.CaptureAll()
			t.Fatalf("E2E side effects incomplete after timeout: mounted=%v changed=%v scraped=%v\n--- screen ---\n%s",
				mounted, changed, scraped, screen)
		}
		time.Sleep(3 * time.Second)
	}
}

// ── helpers ─────────────────────────────────────────────────

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func dockerImagePresent(t *testing.T, image string) bool {
	t.Helper()
	return imageExists(image)
}

func ollamaHasModel(model string) bool {
	resp, err := http.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	// Match "gemma4" against "gemma4:latest" etc.
	return strings.Contains(string(buf[:n]), strings.SplitN(model, ":", 2)[0])
}

// removeWorkspaceEnv drops the sample's .env from the copy, leaving the run with
// no project env file to mount or source.
func removeWorkspaceEnv(t *testing.T, work string) {
	t.Helper()
	path := filepath.Join(work, ".env")
	if _, err := os.Lstat(path); err != nil {
		return // no .env in the sample — nothing to drop
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// copySampleWorkspace copies e2e/samples/ into a fresh temp dir so the
// agent edits a throwaway copy while the tracked sample stays pristine.
func copySampleWorkspace(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if out, err := exec.Command("cp", "-a", filepath.Join(wd, "samples")+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("copy sample workspace: %v\n%s", err, out)
	}
	return dst
}

func firstLine(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.SplitN(strings.TrimRight(string(b), "\r\n"), "\n", 2)[0]
}

func readIn(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

func buildProveo(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("PROVEO_TEST_BIN"); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("PROVEO_TEST_BIN=%s: %v", bin, err)
		}
		return bin
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(wd, "..")
	bin := filepath.Join(t.TempDir(), "proveo")
	c := exec.Command("go", "build", "-o", bin, "./cmd/proveo")
	c.Dir = repoRoot
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build proveo: %v\n%s", err, out)
	}
	return bin
}
