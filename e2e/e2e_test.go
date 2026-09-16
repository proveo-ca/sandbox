//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/tests/41-agent-e2e-sequence.puml

// Package e2e is the agent end-to-end suite.
package e2e

import (
	"encoding/json"
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
	model := localModel(t)

	proveoBin := buildProveo(t)

	// Mount a COPY of the sample monorepo so the tracked sample stays pristine;
	// the agent edits the copy and we assert the host-side side effects.
	work := copySampleWorkspace(t)
	removeWorkspaceEnv(t, work)
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "-c", "user.email=e2e@proveo.test", "-c", "user.name=proveo e2e", "commit", "-q", "-m", "sample")
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
		mounted := strings.Contains(delivered(t, work, "FROM_SAMPLE.txt"), sampleAnchor)  // samples/ mounted
		changed := strings.Contains(delivered(t, work, "DONE.txt"), marker)               // files changed
		scraped := strings.Contains(delivered(t, work, "SCRAPED.html"), "Example Domain") // web scraped
		if mounted && changed && scraped {
			return // all four E2E steps verified
		}
		if _, err := sess.CaptureAll(); err != nil {
			settle := time.Now().Add(durationEnv(t, "PROVEO_TEST_TEARDOWN_TIMEOUT", 2*time.Minute))
			for time.Now().Before(settle) {
				if strings.Contains(delivered(t, work, "DONE.txt"), marker) {
					break
				}
				time.Sleep(3 * time.Second)
			}
			mounted = strings.Contains(delivered(t, work, "FROM_SAMPLE.txt"), sampleAnchor)
			changed = strings.Contains(delivered(t, work, "DONE.txt"), marker)
			scraped = strings.Contains(delivered(t, work, "SCRAPED.html"), "Example Domain")
			if mounted && changed && scraped {
				break
			}
			t.Fatalf("E2E side effects incomplete after the run ended: mounted=%v changed=%v scraped=%v\n--- refs ---\n%s",
				mounted, changed, scraped, hostProveoRefs(t, work))
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

// localModel resolves the configured model to the EXACT tag Ollama serves, or
// skips.
func localModel(t *testing.T) string {
	t.Helper()
	want := env("PROVEO_TEST_LOCAL_MODEL", "gemma4")
	tags, err := ollamaTags()
	if err != nil {
		t.Skipf("Ollama unreachable on the host (%v) — no local model to run against", err)
	}
	got, why := resolveOllamaTag(want, tags)
	if why != "" {
		t.Skipf("local model %q: %s", want, why)
	}
	if got != want {
		t.Logf("local model %q resolved to %q", want, got)
	}
	return got
}

// resolveOllamaTag picks the tag to request, or explains why it cannot.
func resolveOllamaTag(want string, tags []string) (string, string) {
	for _, tag := range tags {
		if tag == want {
			return tag, ""
		}
	}
	if strings.Contains(want, ":") {
		return "", fmt.Sprintf("not on the host; present: %s", strings.Join(tags, ", "))
	}
	for _, tag := range tags {
		if tag == want+":latest" {
			return tag, ""
		}
	}
	var under []string
	for _, tag := range tags {
		if strings.HasPrefix(tag, want+":") {
			under = append(under, tag)
		}
	}
	switch len(under) {
	case 0:
		return "", fmt.Sprintf("not on the host; present: %s", strings.Join(tags, ", "))
	case 1:
		return under[0], ""
	default:
		return "", fmt.Sprintf("ambiguous — %s all match; set PROVEO_TEST_LOCAL_MODEL to one",
			strings.Join(under, ", "))
	}
}

// ollamaTags reads every model name the host serves. The body is decoded in
// full: a single Read truncates a long list at an arbitrary byte.
func ollamaTags() ([]string, error) {
	resp, err := http.Get("http://localhost:11434/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/tags: %s", resp.Status)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no models pulled")
	}
	return out, nil
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

// delivered reads a file the agent produced, from the mounted workspace or,
// for a cloned workspace, from the refs teardown fetched into the host repo.
func delivered(t *testing.T, work, rel string) string {
	t.Helper()
	if body := readIn(work, rel); body != "" {
		return body
	}
	_, body := deliveredThroughRefs(t, work, []string{rel, "reports/" + rel})
	return body
}
