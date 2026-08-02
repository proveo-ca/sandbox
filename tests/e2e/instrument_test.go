//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// This file is the shared instrumentation for the tmux-driven e2e layer. Every
// assertion that can time out should fail through it, because the failures that
// cost the most to diagnose are the ones that report nothing useful.
//
// Three rules, each learned from a failure that hid its own cause:
//
//  1. Snapshot the scrollback WHILE the session is alive. Capturing only at the
//     moment of failure returns "no server running" once the run has exited, which
//     hides whatever it printed on the way out.
//  2. A single empty capture is not death. tmux returns nothing mid-redraw and
//     during long output (an image pull), so only consecutive misses count.
//  3. Always include exited containers. "started and died" and "never started"
//     are different faults and look identical without it.

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
	b.WriteString("--- containers (incl. exited) ---\n")
	b.WriteString(dockerPSAll())
	return b.String()
}

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

// Fatalf fails the test with the message plus full diagnostics.
func (w *watcher) Fatalf(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf(format+"%s", append(args, diagnostics(w.lastScreen))...)
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
		if time.Now().After(deadline) {
			w.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Second)
	}
}

// acceptChoicePrompt accepts the pre-selected choice form. Anchored on the key
// hint rather than the title: the title is copy and has been reworded once, which
// broke a test that waited on it.
//
// Seeding agent-settings.yml does NOT skip this — the prompt always shows so the
// operator sees the posture they are launching — so any test that starts a run on
// a TTY has to answer it.
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

// waitForNewContainer blocks until a container whose name ends in suffix appears
// that was not present before.
func waitForNewContainer(t *testing.T, before map[string]bool, suffix string, timeout time.Duration, sess *tmux.Session) string {
	t.Helper()
	var found string
	newWatcher(t, sess).until("a new "+suffix+" container", timeout, func() bool {
		for _, n := range dockerPSNames() {
			if strings.HasSuffix(n, suffix) && !before[n] {
				found = n
				return true
			}
		}
		return false
	})
	return found
}
