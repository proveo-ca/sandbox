//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// deadAfter is how many consecutive empty captures mean the pane is really gone.
const deadAfter = 4

// diagnostics renders everything worth knowing when a session-driven wait fails:
// the last scrollback captured while alive, plus every container including exited.
func diagnostics(lastScreen string) string {
	var b strings.Builder
	b.WriteString("\n--- last scrollback (captured while alive) ---\n")
	if strings.TrimSpace(lastScreen) == "" {
		b.WriteString("(nothing was ever captured — the session may have failed to start)\n")
	} else {
		b.WriteString(lastScreen)
		b.WriteString("\n")
	}
	if log := runLogTail(lastScreen); log != "" {
		b.WriteString("--- run log (proveo's own, every line incl. warnings) ---\n")
		b.WriteString(log)
		b.WriteString("\n")
	}
	b.WriteString("--- containers (incl. exited) ---\n")
	b.WriteString(dockerPSAll())
	return b.String()
}

// runLogTail reads the log the run names in its own header.
func runLogTail(screen string) string {
	m := runLogPath.FindStringSubmatch(screen)
	if m == nil {
		return ""
	}
	b, err := os.ReadFile(strings.TrimSpace(m[1]))
	if err != nil {
		return ""
	}
	return tail(string(b), 40)
}

var runLogPath = regexp.MustCompile(`run log: (\S+\.log)`)

// dockerPSAll lists every container including exited ones.
func dockerPSAll() string {
	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}").Output()
	if err != nil {
		return "docker ps failed: " + err.Error()
	}
	return string(out)
}

// watcher polls a session-driven condition, keeping the scrollback fresh so a
// failure can explain itself. Use it instead of a bare loop plus Capture().
type watcher struct {
	t          *testing.T
	sess       *tmux.Session
	lastScreen string
	misses     int
}

func newWatcher(t *testing.T, sess *tmux.Session) *watcher {
	t.Helper()
	return &watcher{t: t, sess: sess}
}

// tick refreshes the snapshot and reports whether the session still looks alive.
func (w *watcher) tick() (alive bool) {
	if screen, err := w.sess.CaptureAll(); err == nil && strings.TrimSpace(screen) != "" {
		w.lastScreen, w.misses = screen, 0
		return true
	}
	// Before anything has ever been captured, an empty pane just means "not yet".
	if w.lastScreen == "" {
		return true
	}
	w.misses++
	return w.misses < deadAfter
}

// Screen is the freshest scrollback seen while the session was alive.
func (w *watcher) Screen() string { return w.lastScreen }

var credentialUnavailable = regexp.MustCompile(`(?i)` +
	`credit balance is too low|` +
	`insufficient (credits?|quota|balance)|` +
	`authentication_error|invalid x-api-key|invalid api key`)

// Fatalf fails the test with the message plus full diagnostics — unless the
// pane says the credential was the problem, which Layer 4 treats as a missing
// prerequisite rather than a defect.
func (w *watcher) Fatalf(format string, args ...any) {
	w.t.Helper()
	if m := credentialUnavailable.FindString(w.lastScreen); m != "" {
		w.t.Skipf("the agent could not spend its credential (%q) — supply a funded one to "+
			"exercise this path%s", m, diagnostics(w.lastScreen))
	}
	w.t.Fatalf(format+"%s", append(args, diagnostics(w.lastScreen))...)
}

func harnessSecrets(t *testing.T, target string) []string {
	t.Helper()
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	var out []string
	for _, m := range ms {
		if m.Name != target {
			continue
		}
		for _, e := range m.Env {
			if e.Secret {
				out = append(out, e.Name)
			}
		}
	}
	return out
}

func requireHarnessCredential(t *testing.T, target string) {
	t.Helper()
	sweepSandboxesAfter(t)
	declared := harnessSecrets(t, target)
	if len(declared) == 0 {
		return // nothing to spend; the harness authenticates some other way
	}
	for _, k := range declared {
		if strings.TrimSpace(hostEnvValue(t, k)) != "" {
			return
		}
	}
	t.Skipf("%s authenticates with one of %v and none is set on this host", target, declared)
}

// until polls cond every second until it returns true, the session dies, or the
// deadline passes. Every exit but success carries diagnostics.
func (w *watcher) until(what string, timeout time.Duration, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if !w.tick() {
			w.Fatalf("session exited before %s", what)
		}
		if services, ok := consentPrompt(w.lastScreen); ok {
			w.t.Fatal(consentFailure(services, "<target>"))
		}
		if time.Now().After(deadline) {
			w.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Second)
	}
}

func acceptChoicePrompt(t *testing.T, sess *tmux.Session, target string) {
	t.Helper()
	w := newWatcher(t, sess)
	if _, err := sess.WaitFor("enter accept", 90*time.Second); err != nil {
		w.tick()
		w.Fatalf("%s: choice prompt never appeared (%v)", target, err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("%s: accept choice prompt: %v", target, err)
	}
}

// sweepSandboxesAfter removes every sandbox that appears while one test runs.
func sweepSandboxesAfter(t *testing.T) {
	t.Helper()
	if _, already := sweptTests.LoadOrStore(t.Name(), true); already {
		return
	}
	before, ok := sbxSandboxNames()
	t.Cleanup(func() {
		sweptTests.Delete(t.Name())
		if !ok {
			t.Logf("sweep skipped: `sbx ls` failed when %s started, so nothing proves which sandboxes it made", t.Name())
			return
		}
		for _, name := range newSandboxes(before) {
			_, _ = removeSandbox(t, name)
		}
	})
}

var sweptTests sync.Map

// removeSandbox runs `sbx rm --force name` within PROVEO_TEST_TEARDOWN (default 3m).
// SPEC: _spec/tests/testing-strategy.puml (A WEDGED GUEST HANGS sbx rm)
func removeSandbox(t *testing.T, name string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
	defer cancel()
	cmd := exec.CommandContext(ctx, sbx.Binary, sbx.RemoveArgs(name)...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("%s rm %s outlived PROVEO_TEST_TEARDOWN; its guest is wedged — SIGKILL the shim whose config.json names SANDBOX_NAME=%s: %w",
			sbx.Binary, name, name, ctx.Err())
	}
	return string(out), err
}
