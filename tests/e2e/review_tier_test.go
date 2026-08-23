//go:build e2e

// SPEC: _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/egress/egress-tiers.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestReviewTierConsentGate is the only test that exercises the review tier as a
// whole. Its parts are unit-tested — the gate's cache and fail-closed paths, the
// PTY proxy's pumps, the plan's socket mount — but a consent gate fails silently
// OPEN at the seams, so the integration is what matters:
//
//   - a real CONNECT from inside the container reaches the host-side gate over the
//     bind-mounted unix socket
//   - the overlay draws on the operator's terminal while the agent holds the PTY
//   - "n" denies and the agent sees a blocked connection (not a hang)
//   - "y" allows, and the answer is cached so the same host is not asked twice
func TestReviewTierConsentGate(t *testing.T) {
	const target = "opencode"
	requireHarness(t, target)

	home := t.TempDir()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".proveo"), 0o755); err != nil {
		t.Fatal(err)
	}
	proveoBin := buildProveo(t)

	sess := tmux.New(fmt.Sprintf("proveo-review-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		forceClean(proveoBin)
	})

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgs(t)...)
	cmd = append(cmd,
		// childEnvArgs disables the wizard for the harness suites; this test is ABOUT
		// an interactive prompt, so turn it back on. A later NAME=value wins in env(1).
		"PROVEO_WIZARD=on",
		"HOME="+home,
		"DOCKER_HOST="+dockerHost(t),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target,
		"--egress-mode", "review",
		"--shell",
		"--input", work,
	)
	if err := sess.Start(200, 50, cmd...); err != nil {
		t.Fatalf("start review session: %v", err)
	}
	acceptChoicePrompt(t, sess, target)

	w := newWatcher(t, sess)
	// The container shell is the signal that the topology came up and the agent is
	// running on the PTY proveo owns.
	// tmux trims trailing whitespace, so the prompt reads "...:/app$" with nothing
	// after it — matching on "$ " never fires.
	w.until("the agent shell", 4*time.Minute, func() bool {
		scr := w.Screen()
		return strings.Contains(scr, "@") && (strings.Contains(scr, ":/app$") ||
			strings.Contains(scr, ":/workspace$") || strings.Contains(scr, ":/app#"))
	})

	// ---- deny: the overlay must appear, and refusing must block the request ----
	const denyHost = "example.com"
	probe := func(host, marker string) {
		t.Helper()
		// --max-time keeps a denied request from hanging the shell if the gate ever
		// fails open-but-silent; the marker distinguishes success from failure.
		line := fmt.Sprintf("curl -sS --max-time 25 -o /dev/null https://%s/ && echo %s_OK || echo %s_FAIL",
			host, marker, marker)
		if err := sess.SendText(line); err != nil {
			t.Fatalf("send probe: %v", err)
		}
		if err := sess.Enter(); err != nil {
			t.Fatalf("send probe newline: %v", err)
		}
	}

	probe(denyHost, "DENY")
	w.until("the consent overlay for "+denyHost, 90*time.Second, func() bool {
		return strings.Contains(w.Screen(), "allow connection to "+denyHost)
	})
	if err := sess.SendText("n"); err != nil {
		t.Fatalf("answer no: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("answer no newline: %v", err)
	}
	w.until("the denied probe to finish", 90*time.Second, func() bool {
		return strings.Contains(w.Screen(), "DENY_FAIL") || strings.Contains(w.Screen(), "DENY_OK")
	})
	if strings.Contains(w.Screen(), "DENY_OK") {
		w.Fatalf("declining consent still let the request through — the gate failed OPEN")
	}

	// The agent's TUI must survive the overlay: the shell has to still accept input
	// after the forced repaint, or the overlay corrupted the terminal.
	if err := sess.SendText("echo STILL_ALIVE"); err != nil {
		t.Fatalf("send liveness probe: %v", err)
	}
	_ = sess.Enter()
	w.until("the shell to respond after the overlay", 60*time.Second, func() bool {
		return strings.Count(w.Screen(), "STILL_ALIVE") >= 2 // the echo, then its output
	})

	// ---- allow: consenting must let it through, and cache the answer ----------
	const allowHost = "cdn.jsdelivr.net"
	probe(allowHost, "ALLOW")
	w.until("the consent overlay for "+allowHost, 90*time.Second, func() bool {
		return strings.Contains(w.Screen(), "allow connection to "+allowHost)
	})
	if err := sess.SendText("y"); err != nil {
		t.Fatalf("answer yes: %v", err)
	}
	_ = sess.Enter()
	w.until("the allowed probe to finish", 90*time.Second, func() bool {
		return strings.Contains(w.Screen(), "ALLOW_OK") || strings.Contains(w.Screen(), "ALLOW_FAIL")
	})
	if strings.Contains(w.Screen(), "ALLOW_FAIL") {
		w.Fatalf("consent was granted for %s but the request still failed", allowHost)
	}

	// ---- cache: the same host must not be asked twice -------------------------
	before := strings.Count(w.Screen(), "allow connection to "+allowHost)
	probe(allowHost, "AGAIN")
	w.until("the repeat probe to finish", 90*time.Second, func() bool {
		return strings.Contains(w.Screen(), "AGAIN_OK") || strings.Contains(w.Screen(), "AGAIN_FAIL")
	})
	if after := strings.Count(w.Screen(), "allow connection to "+allowHost); after > before {
		w.Fatalf("%s was asked about twice (%d prompts): the per-host decision is not cached",
			allowHost, after)
	}
	if strings.Contains(w.Screen(), "AGAIN_FAIL") {
		w.Fatalf("the cached allow for %s did not hold on a second request", allowHost)
	}

	_ = sess.SendText("exit")
	_ = sess.Enter()
	t.Logf("review tier verified: overlay drawn, deny blocked, allow permitted, decision cached")
}
