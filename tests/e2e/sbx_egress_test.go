//go:build e2e

// SPEC: _spec/_plans/revision-env-egress.puml, _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const egressProbeHost = "proveo-egress-probe.invalid"

const deniedHost = "example.com"

func sandboxBaselineAllowsEverything(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command(sbx.Binary, sbx.CheckNetworkArgs(egressProbeHost)...).Output()
	if err != nil {
		t.Skipf("sbx policy check unavailable: %v", err)
	}
	var decision struct {
		Allowed bool `json:"allowed"`
	}
	if err := json.Unmarshal(out, &decision); err != nil {
		t.Skipf("sbx policy check answered in an unknown shape: %s", out)
	}
	return decision.Allowed
}

func TestSandboxNetworkBaselineIsStatedHonestly(t *testing.T) {
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	proveoBin := buildProveo(t)
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")

	cmd := exec.Command(proveoBin, "run", "claudecode", "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_EGRESS_ROOT="+t.TempDir(),
		"PROVEO_WIZARD=off",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run claudecode --print: %v\n%s", err, out)
	}
	got := plain(string(out))
	warned := strings.Contains(got, "allows every host")

	if allowAll := sandboxBaselineAllowsEverything(t); allowAll != warned {
		if allowAll {
			t.Fatalf("the daemon allows %s, so this run's Kit allowlist narrows nothing — "+
				"proveo printed a posture without saying so:\n%s", egressProbeHost, got)
		}
		t.Fatalf("the daemon denies %s, so the Kit allowlist binds — proveo warned anyway:\n%s",
			egressProbeHost, got)
	}
}

func TestSandboxEgressIsConfinedToTheKitAllowlist(t *testing.T) {
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	if sandboxBaselineAllowsEverything(t) {
		t.Skipf("this host's global sbx policy allows every destination, so a Kit allowlist "+
			"cannot narrow anything and there is no confinement to assert. "+
			"Initialise a baseline first: `%s policy init deny-all` (or `balanced`)", sbx.Binary)
	}
	requireTmux(t)
	const target = "claudecode"
	if !sbxShellHoldsInADetachedPane(t) {
		t.Skipf("sbx's own shell agent does not survive a detached tmux pane on this host")
	}
	requireHarnessCredential(t, target)

	proveoBin := buildProveo(t)
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	state := t.TempDir()
	before, canList := sbxSandboxNames()

	sess := tmux.New(fmt.Sprintf("proveo-sbx-egress-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})

	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_EGRESS_ROOT="+state,
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}

	w := newWatcher(t, sess)
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 6*time.Minute)
	w.until("the sandbox shell prompt", timeout, func() bool {
		if line := sbxError(w.Screen()); line != "" && !retryable(line) {
			w.Fatalf("sbx refused the run — %s", line)
		}
		answerSbxPrompt(sess, w.Screen())
		return promptReady(w.Screen())
	})

	const results = "SBX_EGRESS.txt"
	probe := "{ printf DIRECT:; curl -sS -o /dev/null -w '%{http_code}' --max-time 20 https://" + deniedHost +
		"/ 2>/dev/null || printf REFUSED; echo; " +
		"printf DAEMON:; docker pull --quiet alpine:3.20 >/dev/null 2>&1 && echo PULLED || echo REFUSED; } > " + results + " 2>&1"
	if err := sess.SendText(probe); err != nil {
		t.Fatalf("send egress probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send probe newline: %v", err)
	}
	w.until("the egress probe to finish", 4*time.Minute, func() bool {
		s := readIn(work, results)
		return strings.Contains(s, "DIRECT:") && strings.Contains(s, "DAEMON:")
	})
	got := readIn(work, results)

	if !strings.Contains(got, "DIRECT:REFUSED") {
		t.Errorf("the agent reached %s, which no Kit allowlists — the sandbox allowlist is not confining direct egress:\n%s",
			deniedHost, got)
	}
	if strings.Contains(got, "DAEMON:PULLED") {
		t.Errorf("a container pull inside the sandbox succeeded against a registry no Kit allowlists — "+
			"the per-sandbox daemon is a path around the Kit policy, which is the property dind gates behind "+
			"five conditions on the docker backend:\n%s", got)
	}

	_ = sess.SendText("exit")
	_ = sess.Enter()
	waitSessionExit(sess, 3*time.Minute)

	matches, _ := filepath.Glob(filepath.Join(state, "egress", "*", "sbx", runlog.PolicyLogFile))
	if len(matches) == 0 {
		t.Fatalf("the run left no %s under %s", runlog.PolicyLogFile, state)
	}
	record, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		BlockedHosts []struct {
			Host string `json:"host"`
		} `json:"blocked_hosts"`
	}
	if err := json.Unmarshal(record, &log); err != nil {
		t.Fatalf("captured policy log is not the shape the daemon documents: %v\n%s", err, record)
	}
	var blocked []string
	for _, h := range log.BlockedHosts {
		blocked = append(blocked, h.Host)
	}
	if len(blocked) == 0 {
		t.Errorf("the probe was refused but the captured record names no blocked host — "+
			"an sbx run's only egress evidence is this file:\n%s", record)
	}
	t.Logf("confined: direct probe and daemon pull refused; record names %v", blocked)
}
