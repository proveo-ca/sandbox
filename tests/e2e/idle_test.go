//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// runIDPattern finds the sandbox/run name proveo prints, so teardown can name it.
var runIDPattern = regexp.MustCompile(`proveo-\d+-\d+`)

// authFailures are the outputs that mean the agent never reached a prompt. They
// SKIP rather than fail: a session that could not start proves nothing either way
// about whether an idle one survives, and reporting that as a failure would send
// the reader after the wrong thing.
var authFailures = []string{
	"Failed to authenticate",
	"Credit balance is too low",
	"needs a subscription login",
	"Please run /login",
	// The TUI reports it in the status bar rather than as an error, and the agent
	// then exits — which is the whole failure this suite exists to distinguish
	// from a session that was stopped underneath a HEALTHY agent.
	"Not logged in",
	"Run /login",
}

// ansiSeq matches the escape sequences a TUI writes. Stripping them is not
// cosmetic: the status bar is laid out with cursor-positioning escapes BETWEEN
// words, so "Not logged in" reaches the buffer as "Not\x1b[177Glogged\x1b[184Gin"
// and every plain substring match for it silently fails.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[]P][^\x1b\x07]*(?:\x1b\\|\x07)|\x1b.`)

// plain renders terminal output as the words a reader would see, so a marker can
// be matched without depending on where the cursor happened to be.
func plain(s string) string {
	return strings.Join(strings.Fields(ansiSeq.ReplaceAllString(s, " ")), " ")
}

// blockedMarkers are screens that WAIT for a human. The session is alive but can
// never proceed unattended, which is a different fault from dying and must not be
// reported as one — Claude Code's first-run theme picker is the one that bit us,
// and it appears whenever HOME names a path with no config in it.
var blockedMarkers = []string{
	"Choose the text style",
	"Let's get started",
}

// reachedPrompt reports whether the agent is up and waiting.
//
// It matches the TUI's OWN frame rather than proveo's entrypoint banner. The
// banner is not a reliable signal: it is printed before the agent starts, so a
// session can be fully up without it having been the last thing said — and the
// stock sbx image never prints it at all, which the ladder's rung 0 needs. The
// frame is the one thing every backend and every image agree on.
func reachedPrompt(raw string) bool {
	out := plain(raw)
	for _, m := range []string{"Claude Code", "bypass permissions"} {
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

// An operator who walks away has to come back to a live session. Every failure in
// this class looked identical from outside — "sandbox was stopped", the run
// reported as failed — and none could be told apart from a session that simply
// ended, because nothing was ever watching an IDLE one. The auto-stop grace
// period is 30s after the session disconnects, so a five-minute wait clears it by
// an order of magnitude.
//
// The test therefore does the one thing no other case here does: it reaches a
// prompt and then does NOTHING, longer than any timer in the stack, and asserts
// the agent is still there. It spends no model call — an agent sitting at its
// prompt has not been asked anything — and it types nothing, which is the point:
// input is the variable being held at zero.
//
// tmux cannot host this. `sbx run -t <image> shell <ws>` exits within seconds in a
// detached pane with proveo uninvolved (see sbx_test.go), so the session needs a
// REAL pty; pty.Start gives the child one without a multiplexer in between.
func TestAgentSurvivesIdleAtPrompt(t *testing.T) {
	if os.Getenv("PROVEO_IDLE_TEST") != "1" {
		t.Skip("set PROVEO_IDLE_TEST=1 to run the idle-survival check (it waits on purpose, ~6 minutes)")
	}
	const target = "claudecode"
	harnessImage(t, target) // skips unless docker and the image are both here
	proveoBin := buildProveo(t)

	idle := durationEnv(t, "PROVEO_IDLE_FOR", 5*time.Minute)
	startup := durationEnv(t, "PROVEO_IDLE_STARTUP", 5*time.Minute) // image load can be slow
	work := t.TempDir()

	args := append(childEnvArgsNoCredential(t), proveoBin, "run", target, "--input", work)
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
		if reachedPrompt(out) {
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
	backend := "docker+egress"
	if strings.Contains(plain(seen()), "docker sandboxes (sbx)") {
		backend = "sbx"
	}
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

// sandboxRunning reports whether sbx still lists name as running. An unreadable
// listing is reported as running: this is a corroborating check, and failing the
// test because `sbx ls` hiccuped would blame the agent for the tool.
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
