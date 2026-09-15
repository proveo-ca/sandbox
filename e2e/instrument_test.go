//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
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
		if time.Now().After(deadline) {
			w.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Second)
	}
}

func waitForContainerShell(t *testing.T, w *watcher, timeout time.Duration) {
	t.Helper()
	w.until("the agent shell", timeout, func() bool {
		scr := w.Screen()
		if !strings.Contains(scr, "@") {
			return false
		}
		for _, wd := range []string{"/app", "/workspace"} {
			if strings.Contains(scr, ":"+wd+"$") || strings.Contains(scr, ":"+wd+"#") {
				return true
			}
		}
		return false
	})
}

// launchShell starts a real `proveo run target --shell` session under tmux —
// egress open, credentials forwarded, and the picker skipped — so callers
// exercise the actual entrypoint chain (workspace scoping, git-identity
// bridging, dependency-tree provisioning, all of it) rather than a hand-built
// `docker run --entrypoint bash` substitute for pieces of it. It waits for the
// container's own shell prompt before returning.
func launchShell(t *testing.T, proveoBin, target, dir string, extra ...string) *tmux.Session {
	t.Helper()
	sess := tmux.New(fmt.Sprintf("proveo-shell-%s-%d", target, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env",
		"PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_SBX=off",
		proveoBin, "run", target,
		"--egress-mode", "open", "--credentials", "forward",
		"--input", dir, "--shell",
	}
	cmd = append(cmd, extra...)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start %s --shell: %v", target, err)
	}
	waitForContainerShell(t, newWatcher(t, sess), durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute))
	return sess
}

// shellExec runs script as a child `bash -c` inside sess's already-live
// interactive shell — a child process, not the shell itself, so an internal
// `exit` inside script cannot end the session — and returns everything the
// pane has shown once script's completion marker appears, plus its exit
// status. script is exactly the kind of body that used to run under
// `docker run --entrypoint bash <img> -c script`; callers can keep asserting
// on it with strings.Contains the same way.
func shellExec(t *testing.T, sess *tmux.Session, script string, timeout time.Duration) (string, int) {
	t.Helper()
	marker := fmt.Sprintf("PROVEO-SHELLEXEC-%d", time.Now().UnixNano())
	line := "bash -c " + shellQuote([]string{script}) + fmt.Sprintf("; echo %s=$?", marker)
	if err := sess.SendText(line); err != nil {
		t.Fatalf("send script: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
	screen, err := sess.WaitFor(marker+"=", timeout)
	if err != nil {
		t.Fatalf("script never finished within %s: %v\n--- pane ---\n%s", timeout, err, screen)
	}
	status := -1
	if idx := strings.LastIndex(screen, marker+"="); idx >= 0 {
		fmt.Sscanf(screen[idx+len(marker)+1:], "%d", &status)
	}
	return screen, status
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
