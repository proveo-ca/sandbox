//go:build e2e

// SPEC: _spec/internal/sbx/startup-dispatch.puml
package e2e

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestSbxStartupStepsDoNotGateTheAgent pins Docker's documented Kit contract on
// stock sbx: a startup step, foreground or background, never holds the agent's
// entrypoint. The background row is the one proveo's seed split relies on.
func TestSbxStartupStepsDoNotGateTheAgent(t *testing.T) {
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	step := durationEnv(t, "PROVEO_DISPATCH_STEP", 60*time.Second)
	version, _ := exec.Command("sbx", "version").CombinedOutput()
	sbxVersion := strings.TrimSpace(string(version))
	t.Logf("sbx version: %s", sbxVersion)

	for _, tc := range []struct {
		name       string
		background bool
	}{
		{"foreground", false},
		{"background", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, stepStart, stepEnd := runDispatchProbe(t, step, tc.background)
			if stepStart == 0 {
				t.Fatalf("the probe Kit's startup step never ran — the probe measured nothing (sbx %s)", sbxVersion)
			}
			if agent == 0 {
				t.Fatalf("the shell agent never started (step began %d, ended %d; sbx %s)", stepStart, stepEnd, sbxVersion)
			}
			if stepEnd != 0 && agent >= stepEnd {
				t.Errorf("sbx started the agent at %d, after a %s startup step ended at %d (began %d). "+
					"Docker documents that startup steps \"don't gate the agent's entrypoint\" "+
					"(https://docs.docker.com/ai/sandboxes/customize/kits/). Report upstream with: sbx version %q",
					agent, tc.name, stepEnd, stepStart, sbxVersion)
			}
		})
	}
}

// runDispatchProbe starts stock sbx's shell agent under a one-step mixin whose
// step sleeps for step, and returns the agent's and the step's timestamps.
func runDispatchProbe(t *testing.T, step time.Duration, background bool) (agent, stepStart, stepEnd int64) {
	t.Helper()
	kit, err := sbx.WriteKit(filepath.Join(t.TempDir(), "kit"), sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          "proveo-dispatch-probe",
		Setup: &sbx.KitSetup{Startup: []sbx.KitCommand{{
			Command: []string{"/bin/sh", "-c", fmt.Sprintf(
				"date +%%s > /tmp/proveo-probe-step-start; sleep %d; date +%%s > /tmp/proveo-probe-step-end",
				int(step.Seconds()))},
			Background:  background,
			Description: "proveo probe: one startup step that outlasts the agent's start",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("proveo-dispatch-%d", time.Now().UnixNano()%1_000_000_000)
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	run := exec.Command("sbx", "run", "--name", name, "--kit", kit, sbx.ShellAgent, t.TempDir(),
		"--", "-c", "date +%s > /tmp/proveo-probe-agent; exec sleep 900", "proveo-probe")
	ptmx, err := pty.Start(run)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	t.Cleanup(func() { _ = run.Process.Kill(); _ = ptmx.Close() })
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	deadline := time.Now().Add(step + 5*time.Minute)
	for time.Now().Before(deadline) && (agent == 0 || stepEnd == 0) {
		agent, stepStart, stepEnd = probeDispatch(name)
		time.Sleep(2 * time.Second)
	}
	return agent, stepStart, stepEnd
}

// probeDispatch reads the three probe timestamps from inside the sandbox; a
// missing one reads as 0.
func probeDispatch(name string) (agent, stepStart, stepEnd int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sbx", "exec", name, "--", "sh", "-c",
		"for f in agent step-start step-end; do cat /tmp/proveo-probe-$f 2>/dev/null || echo 0; done").Output()
	if err != nil {
		return 0, 0, 0
	}
	var v [3]int64
	for i, l := range strings.Fields(string(out)) {
		if i < len(v) {
			v[i], _ = strconv.ParseInt(l, 10, 64)
		}
	}
	return v[0], v[1], v[2]
}
