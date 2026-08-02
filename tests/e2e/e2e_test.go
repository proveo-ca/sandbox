//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/tests/41-agent-e2e-sequence.puml

// Package e2e is the agent end-to-end suite. It (1) runs a real harness image,
// (2) attaches a LOCAL model (Ollama), (3) drives the agent NON-INTERACTIVELY
// (`opencode run --auto`, task from argv), and (4) asserts observable SIDE
// EFFECTS on the host — the mounted sample workspace was seen, files were
// changed, and a page was scraped over egress — never the model's prose. Gated
// on PROVEO_LLM_TEST=1 and the local stack, so it never fails CI for missing
// infra.
//
//	PROVEO_LLM_TEST=1 [PROVEO_TEST_LOCAL_MODEL=gemma4] \
//	  go test -tags=e2e ./tests/e2e/ -run PromptfulE2E -v -timeout 360s
//
// The harness is opencode-specific here: `run --auto --agent build` is opencode's
// non-interactive form. opencode is the default target and (as of this writing)
// the one with working local-model support — see defs/opencode/entrypoint.sh
// (configure_opencode_local_model), the fix this very test surfaced.
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

// TestPromptfulE2E runs a real harness image with a LOCAL model, drives the agent
// NON-INTERACTIVELY through ONE deterministic task, and asserts the SIDE EFFECTS
// on the host rather than the model's prose:
//
//	samples/ mounted → FROM_SAMPLE.txt == the mounted README's first line
//	files changed     → DONE.txt contains the marker
//	web scraped        → SCRAPED.html contains example.com's stable title
//
// Handing the model one exact shell command keeps the small local model reliable
// while still exercising the full run → local-LLM → tool-call → side-effect loop.
func TestPromptfulE2E(t *testing.T) {
	if os.Getenv("PROVEO_LLM_TEST") != "1" {
		t.Skip("set PROVEO_LLM_TEST=1 to run the local-model agent E2E")
	}
	if !tmux.Available() {
		t.Skip("tmux not installed (brew install tmux)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	target := env("PROVEO_TEST_TARGET", "opencode")
	image := env("PROVEO_TEST_IMAGE", "proveo/"+target+":latest")
	if !dockerImagePresent(t, image) {
		t.Skipf("harness image %s not built", image)
	}
	model := env("PROVEO_TEST_LOCAL_MODEL", "gemma4")
	if !ollamaHasModel(model) {
		t.Skipf("Ollama model %q not available on the host", model)
	}

	proveoBin := buildProveo(t)

	// Mount a COPY of the sample monorepo so the tracked sample stays pristine;
	// the agent edits the copy and we assert the host-side side effects.
	work := copySampleWorkspace(t)
	mustRun(t, work, "git", "init", "-q", ".")
	sampleAnchor := firstLine(t, filepath.Join(work, "README.md"))

	const marker = "BANANA-E2E-OK"
	// One deterministic shell command drives all three effects: curl (web scrape),
	// head of a mounted sample file (mount proof), and a marker file (side effect).
	task := "Use your bash tool to run exactly this one command and then stop: " +
		"curl -sS https://example.com -o SCRAPED.html && head -1 README.md > FROM_SAMPLE.txt && printf %s " + marker + " > DONE.txt"

	sess := tmux.New(fmt.Sprintf("proveo-e2e-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	// Drive the harness NON-INTERACTIVELY: everything after `--` is forwarded to the
	// agent, so `opencode run --auto --agent build <task>` executes the task from
	// argv (--auto approves the sandboxed local model's tool calls) and exits. No
	// keystrokes, so no TUI readiness race. tmux only supplies the PTY the harness's
	// `docker run -it` requires. Broker mode + --local-model gives an
	// internet-capable bridge (Ollama sidecar via NO_PROXY), so curl reaches
	// example.com.
	//
	// --scope . selects the repo root non-interactively: the sample is a polyglot
	// monorepo, so without it `proveo run` would pop its interactive sub-project
	// picker (TTY + git repo + sub-projects) and block, since we send no keys.
	if err := sess.Start(200, 50, proveoBin, "run", target,
		"--egress-mode", "broker", "--local-model", model, "--input", work, "--scope", ".",
		"--", "run", "--auto", "--agent", "build", task); err != nil {
		t.Fatalf("start session: %v", err)
	}

	// Poll host-side for ALL THREE side effects (prose-independent). Generous
	// enough for a small local model on GPU to churn through the full harness
	// context (seeded crew + AGENTS.md) + a runtime provider-package install; this
	// suite is opt-in, not on CI's critical path.
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
	return exec.Command("docker", "image", "inspect", image).Run() == nil
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

// copySampleWorkspace copies tests/e2e/samples/ into a fresh temp dir so the
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
	repoRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(t.TempDir(), "proveo")
	c := exec.Command("go", "build", "-o", bin, "./cmd/proveo")
	c.Dir = repoRoot
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build proveo: %v\n%s", err, out)
	}
	return bin
}
