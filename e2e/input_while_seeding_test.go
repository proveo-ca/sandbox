//go:build e2e

// SPEC: _spec/internal/sbx/startup-dispatch.puml
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// loginScreen marks an agent waiting on a login rather than on input: a missing
// prerequisite on this host, not a responsiveness defect.
var loginScreen = regexp.MustCompile(`(?i)(log ?in|sign ?in|api key|authenticate|select login method)`)

// TestAgentTakesInputWhileSeeding launches every sbx harness through `proveo run`,
// types into the agent while the background provision step is still installing,
// and asserts each keystroke echoes promptly. For a split seed it also asserts
// the agent takes input soon after the release, not after the installs.
func TestAgentTakesInputWhileSeeding(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	proveoBin := buildProveo(t)
	gate := make(chan struct{}, 1)
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			if _, known := readySpecs[target]; !known {
				t.Fatalf("%s runs on sbx but readySpecs has no row for it — a missing row reads as an oversight", target)
			}
			t.Parallel()
			gate <- struct{}{}
			defer func() { <-gate }()
			requireHarness(t, target)
			sweepSandboxesAfter(t)
			inputWhileSeedingProbe(t, proveoBin, target)
		})
	}
}

func inputWhileSeedingProbe(t *testing.T, proveoBin, target string) {
	t.Helper()
	p90Max := durationEnv(t, "PROVEO_TEST_ECHO_P90", 250*time.Millisecond)
	echoMax := durationEnv(t, "PROVEO_TEST_ECHO_MAX", time.Second)
	readyMax := durationEnv(t, "PROVEO_TEST_READY_AFTER_RELEASE", 20*time.Second)

	work := readyWorkspace(t)
	sess := tmux.New(fmt.Sprintf("proveo-input-%s-%d", target, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	if err := sess.Start(220, 50, "env", "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0", "PROVEO_EGRESS_ENV_FILE=/dev/null",
		proveoBin, "run", target, "--egress-mode", "open", "--input", work); err != nil {
		t.Fatalf("start proveo run %s: %v", target, err)
	}
	w := newWatcher(t, sess)

	nameRe := regexp.MustCompile(`proveo-` + regexp.QuoteMeta(target) + `-[0-9a-f]{8}`)
	var name string
	w.until("the sandbox name on screen", durationEnv(t, "PROVEO_TEST_TIMEOUT", 5*time.Minute), func() bool {
		name = nameRe.FindString(w.Screen())
		return name != ""
	})
	w.until("the seed's release marker", 3*time.Minute, func() bool {
		return vmExec(name, "test -e "+releaseMarker+" && echo yes") == "yes"
	})
	waitPaneSettled(sess, 1500*time.Millisecond, 90*time.Second)

	split := sbx.BuiltinAgent(target) != ""
	seedingAtStart := vmExec(name, "test -e "+seedDone+" || echo running") == "running"

	lead := "zqx"
	if err := sess.SendText(lead); err != nil {
		t.Fatal(err)
	}
	if !paneShows(sess, lead, 30*time.Second) {
		if loginScreen.MatchString(w.Screen()) {
			t.Skipf("%s is waiting on a login, not on input — log it in once to exercise this path%s", target, diagnostics(w.Screen()))
		}
		w.Fatalf("%s never echoed typed input %q within 30s of settling", target, lead)
	}
	clock := strings.Fields(vmExec(name, "date +%s.%N; stat -c %Y "+releaseMarker))

	typed, letters := lead, "jkvwpbmhgfdtlcy"
	var lat []time.Duration
	for _, c := range letters {
		typed += string(c)
		start := time.Now()
		if err := sess.SendText(string(c)); err != nil {
			t.Fatal(err)
		}
		if !paneShows(sess, typed, 5*time.Second) {
			w.Fatalf("%s dropped keystroke %q: %q never appeared", target, string(c), typed)
		}
		lat = append(lat, time.Since(start))
		time.Sleep(150 * time.Millisecond)
	}
	seedingAtEnd := vmExec(name, "test -e "+seedDone+" || echo running") == "running"
	for range typed {
		_ = sess.SendKeys("BSpace")
	}

	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p90, worst := sorted[len(sorted)*9/10], sorted[len(sorted)-1]
	t.Logf("%s echo latency p90=%s max=%s all=%v (background seed running at start=%v, at end=%v)",
		target, p90.Round(time.Millisecond), worst.Round(time.Millisecond), lat, seedingAtStart, seedingAtEnd)
	if split && !seedingAtStart {
		t.Logf("%s: the background provision step had already finished when typing began — latency was measured without it", target)
	}
	if p90 > p90Max {
		t.Errorf("%s echo latency p90 = %s, want <= %s — keystrokes lag while the seed runs", target, p90.Round(time.Millisecond), p90Max)
	}
	if worst > echoMax {
		t.Errorf("%s echo latency max = %s, want <= %s", target, worst.Round(time.Millisecond), echoMax)
	}
	if split && len(clock) == 2 {
		now, _ := strconv.ParseFloat(clock[0], 64)
		rel, _ := strconv.ParseFloat(clock[1], 64)
		if gap := time.Duration((now - rel) * float64(time.Second)); gap > readyMax {
			t.Errorf("%s took input %s after the release, want <= %s — the agent is waiting on the installs again",
				target, gap.Round(time.Second), readyMax)
		}
	}
}

// vmExec runs script in the sandbox and returns its trimmed stdout; "" on error.
func vmExec(name, script string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sbx.Binary, "exec", "-w", "/", name, "--", "sh", "-c", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// paneShows polls the visible pane every 10ms until it contains s.
func paneShows(sess *tmux.Session, s string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if screen, err := sess.Capture(); err == nil && strings.Contains(screen, s) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitPaneSettled returns once the pane has not changed for quiet, or at timeout.
func waitPaneSettled(sess *tmux.Session, quiet, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	last, since := "", time.Now()
	for time.Now().Before(deadline) {
		screen, _ := sess.Capture()
		if screen != last {
			last, since = screen, time.Now()
		} else if time.Since(since) >= quiet {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
