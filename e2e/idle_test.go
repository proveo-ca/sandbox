//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// runIDPattern finds the sandbox/run name proveo prints, so teardown can name it.
var runIDPattern = regexp.MustCompile(`proveo-\d+-\d+`)

var authFailures = []string{
	"Failed to authenticate",
	"Credit balance is too low",
	// The same state in other vendors' words. It arrives as a 403, which reads
	// as an egress denial and was misread as one for a whole session before
	// anyone opened the response body — so the wording, not the status, is what
	// gets recognised here.
	// SPEC: _spec/internal/sbx/kit-sandbox-credential-gap.puml
	"used all available credits",
	"reached its monthly spending limit",
	"insufficient_quota",
	"needs a subscription login",
	"Please run /login",
	"Not logged in",
	"Run /login",
	// cursor-agent's first-run screen. It IS at a prompt, but the only key it
	// accepts starts a login — so an unattended session can never proceed.
	"Press any key to log in",
}

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[]P][^\x1b\x07]*(?:\x1b\\|\x07)|\x1b.`)

// plain renders terminal output as the words a reader would see, so a marker can
// be matched without depending on where the cursor happened to be.
func plain(s string) string {
	return strings.Join(strings.Fields(ansiSeq.ReplaceAllString(s, " ")), " ")
}

var blockedMarkers = []string{
	"Choose the text style",
	"Let's get started",
}

func promptMarkers(target string) []string {
	switch target {
	case "cursor":
		return []string{"Cursor Agent", "cursor-agent"}
	case "opencode":
		return []string{"opencode"}
	case "cecli":
		return []string{"cecli", "aider"}
	default: // claudecode
		return []string{"Claude Code", "bypass permissions"}
	}
}

func reachedPromptFor(raw, target string) bool {
	out := plain(raw)
	for _, m := range promptMarkers(target) {
		if strings.Contains(out, m) {
			return true
		}
	}
	return false
}

// deathMarkers are what the operator actually sees when this regresses.
var deathMarkers = []string{
	"was stopped",
	"kept for diagnosis",
	"agent exited with code",
}

func detectBackend(raw string) string {
	out := plain(raw)
	for _, m := range []string{
		"docker sandboxes (sbx)", // proveo's own backend line
		"Created sandbox ",       // sbx's
		"/sbx/kit",               // the kit path in the posture block
		"/sbx/policy-log.json",   // the egress record
	} {
		if strings.Contains(out, m) {
			return "sbx"
		}
	}
	return "docker+egress"
}

func idleTargets() []string {
	raw := strings.TrimSpace(os.Getenv("PROVEO_IDLE_TARGETS"))
	if raw == "" {
		return []string{"claudecode"}
	}
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func TestAgentSurvivesIdleAtPrompt(t *testing.T) {
	if os.Getenv("PROVEO_IDLE_TEST") != "1" {
		t.Skip("set PROVEO_IDLE_TEST=1 to run the idle-survival check (it waits on purpose, ~6 minutes)")
	}
	for _, target := range idleTargets() {
		t.Run(target, func(t *testing.T) { idleAtPrompt(t, target) })
	}
}

func idleAtPrompt(t *testing.T, target string) {
	harnessImage(t, target) // skips unless docker and the image are both here
	proveoBin := buildProveo(t)

	idle := durationEnv(t, "PROVEO_IDLE_FOR", 5*time.Minute)
	startup := durationEnv(t, "PROVEO_IDLE_STARTUP", 5*time.Minute) // image load can be slow
	work := t.TempDir()

	trace := filepath.Join(t.TempDir(), "stdin.trace")
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		b, err := os.ReadFile(trace)
		if err != nil || len(b) == 0 {
			t.Logf("stdin trace: nothing recorded (%v) — the agent was sent no input at all", err)
			return
		}
		t.Logf("-- stdin trace (what the agent was sent, and the verdict) --\n%s", b)
	})

	args := append(childEnvArgsNoCredential(t), "PROVEO_TRACE_STDIN="+trace,
		proveoBin, "run", target, "--input", work)
	cmd := exec.Command("env", args...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	// A TUI that cannot read the window size draws one character per line, and the
	// launch banner never appears in a recognisable form.
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})

	var mu sync.Mutex
	var buf strings.Builder
	seen := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	t.Cleanup(func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		// A kept sandbox outlives the test process, and this suite must not leave
		// one behind for the next run to trip over.
		if name := runIDPattern.FindString(seen()); name != "" {
			_ = exec.Command("sbx", "rm", "--force", name).Run()
		}
	})

	// ── reach a prompt ───────────────────────────────────────────────────────
	deadline := time.Now().Add(startup)
	for {
		out := seen()
		for _, f := range authFailures {
			if strings.Contains(plain(out), f) {
				t.Skipf("agent never reached a prompt (%q) — idle survival is untestable without a session", f)
			}
		}
		if reachedPromptFor(out, target) {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("proveo exited before the agent reached a prompt (%v)\n%s", err, lastLines(out, 25))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent never reached a prompt within %s\n%s", startup, lastLines(out, 25))
		}
		time.Sleep(2 * time.Second)
	}
	backend := detectBackend(seen())
	t.Logf("agent reached a prompt on the %s backend — now waiting %s with ZERO input", backend, idle)

	// ── do nothing, on purpose ───────────────────────────────────────────────
	select {
	case err := <-exited:
		out := seen()
		// An agent that was never logged in did not "die idle" — it could not
		// start. Reported as a skip so the reader is not sent after the wrong bug.
		for _, f := range authFailures {
			if strings.Contains(plain(out), f) {
				t.Skipf("agent reached a prompt but was not authenticated (%q) — it exits on its own, "+
					"so idle survival is untestable until the login works", f)
			}
		}
		for _, m := range deathMarkers {
			if strings.Contains(plain(out), m) {
				t.Fatalf("the agent died while IDLE after reaching a prompt: %q (%v)\n%s", m, err, lastLines(out, 30))
			}
		}
		t.Fatalf("proveo exited while the session was idle (%v)\n%s", err, lastLines(out, 30))
	case <-time.After(idle):
	}

	// ── it is still there ────────────────────────────────────────────────────
	out := seen()
	for _, m := range deathMarkers {
		if strings.Contains(plain(out), m) {
			t.Errorf("output carries %q even though proveo is still running\n%s", m, lastLines(out, 30))
		}
	}
	if name := runIDPattern.FindString(out); name != "" && backend == "sbx" {
		if !sandboxRunning(name) {
			t.Errorf("sandbox %s is no longer running after %s idle — the session was stopped underneath the agent", name, idle)
		}
	}
	if t.Failed() {
		return
	}
	t.Logf("✅ IDLE SURVIVED: %s sat at a prompt for %s with no input and is still alive — "+
		"an operator can step away this long and come back to a live session", target, idle)
}

func sandboxRunning(name string) bool {
	out, err := exec.Command("sbx", "ls").CombinedOutput()
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name) {
			return strings.Contains(line, "running")
		}
	}
	return false // listed nowhere: it is gone, which is the failure this catches
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", "\n"), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		out = append([]string{lines[i]}, out...)
	}
	return "── last output ──\n" + strings.Join(out, "\n")
}

func TestDetectBackendReadsMoreThanProveosOwnLine(t *testing.T) {
	t.Parallel()
	// Every one of these appeared in the output of a run that WAS sbx.
	for _, witness := range []string{
		"● backend: docker sandboxes (sbx)",
		"✓ Created sandbox proveo-1788711006-59906",
		"  kit        /Users/x/.local/state/proveo/egress/proveo-1/sbx/kit",
		"● egress record: /Users/x/.local/state/proveo/egress/proveo-1/sbx/policy-log.json",
	} {
		if got := detectBackend(witness); got != "sbx" {
			t.Errorf("detectBackend(%q) = %q, want sbx", witness, got)
		}
	}
	// And a genuine docker run must not be misread the other way.
	for _, witness := range []string{
		"● backend: docker + egress sidecars",
		"docker run -d --rm --name proveo-1-squid",
		"",
	} {
		if got := detectBackend(witness); got != "docker+egress" {
			t.Errorf("detectBackend(%q) = %q, want docker+egress", witness, got)
		}
	}
}
