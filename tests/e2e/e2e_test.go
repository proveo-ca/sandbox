//go:build e2e

// SPEC: _spec/tests/40-agent-e2e-components.puml, _spec/tests/41-agent-e2e-sequence.puml

// Package e2e is the agent end-to-end suite. It (1) runs a real harness image,
// (2) attaches a LOCAL model (Ollama), (3) drives the agent NON-INTERACTIVELY
// (`opencode run --auto`, task from argv), and (4) asserts observable SIDE
// EFFECTS on the host — the mounted sample workspace was seen, files were
// changed, and a page was scraped over egress — never the model's prose. The
// e2e build tag is the only gate; each test then skips on its own missing
// prerequisites (see preconditions_test.go), so it never fails CI for missing
// infra.
//
//	[PROVEO_TEST_LOCAL_MODEL=gemma4] \
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
	// This suite runs entirely on the LOCAL model, so the workspace must carry no
	// project .env at all. Two separate reasons, both fatal:
	//
	//	samples/.env is a symlink into the repo root, so `cp -a` leaves a DANGLING
	//	link here — and this posture bind-mounts the workspace .env rather than
	//	masking it, which makes a dangling link a broken mount source.
	//
	//	A REAL .env is worse than a dangling one. The entrypoint sources the mounted
	//	file after docker has applied `-e`, so the repo's ARCHITECT_MODEL wins over
	//	the ollama/* alias --local-model just bridged in, and the main agent goes to
	//	the cloud provider — which is exactly the credential this suite is built to
	//	not need.
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

	// Drive the harness NON-INTERACTIVELY: everything after `--` is forwarded to the
	// agent, so `opencode run --auto --agent build <task>` executes the task from
	// argv (--auto approves the sandboxed local model's tool calls) and exits. No
	// keystrokes, so no TUI readiness race. tmux only supplies the PTY the harness's
	// `docker run -it` requires.
	//
	// `open` + `forward` is the plain-bridge posture, and this suite needs BOTH
	// halves of it. It is the only shape that reaches the HOST's Ollama
	// (host.docker.internal): every other tier puts the agent on an internal
	// network with DNS blackholed, so the only reachable model server is the
	// sidecar — which on macOS has no GPU and generates at CPU speed. It is also
	// what gives curl an internet-capable bridge to example.com.
	//
	// Naming both axes is the post-rename spelling of what this test always asked
	// for. It said `--egress-mode broker` back when one flag carried network tier
	// AND credential handling; `broker` now aliases to the `open` TIER alone and
	// credentials default to brokering, which lands in the intercepting branch and
	// silently demotes the model to the CPU sidecar.
	//
	// --scope . selects the repo root non-interactively, and PROVEO_WIZARD=off
	// keeps the sub-project picker and the choice form off this PTY — the run then
	// takes the manifest defaults for every axis these flags do not name, instead
	// of whatever the operator's remembered posture happens to hold.
	if err := sess.Start(200, 50, "env", "PROVEO_WIZARD=off", proveoBin, "run", target,
		"--egress-mode", "open", "--credentials", "forward",
		"--local-model", model, "--input", work, "--scope", ".",
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
