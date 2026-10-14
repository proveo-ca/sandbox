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

// TestSbxForegroundStartupDoesNotGateTheAgent pins Docker's documented Kit
// contract on stock sbx: a foreground startup step blocks the steps after it,
// never the agent's entrypoint.
func TestSbxForegroundStartupDoesNotGateTheAgent(t *testing.T) {
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	step := durationEnv(t, "PROVEO_DISPATCH_STEP", 60*time.Second)
	version, _ := exec.Command("sbx", "version").CombinedOutput()
	sbxVersion := strings.TrimSpace(string(version))
	t.Logf("sbx version: %s", sbxVersion)

	kit, err := sbx.WriteKit(filepath.Join(t.TempDir(), "kit"), sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          "proveo-dispatch-probe",
		Setup: &sbx.KitSetup{Startup: []sbx.KitCommand{{
			Command: []string{"/bin/sh", "-c", fmt.Sprintf(
				"date +%%s > /tmp/proveo-probe-step-start; sleep %d; date +%%s > /tmp/proveo-probe-step-end",
				int(step.Seconds()))},
			Description: "proveo probe: one foreground step that outlasts the agent's start",
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

	var agent, stepStart, stepEnd int64
	deadline := time.Now().Add(step + 5*time.Minute)
	for time.Now().Before(deadline) && (agent == 0 || stepEnd == 0) {
		agent, stepStart, stepEnd = probeDispatch(name)
		time.Sleep(2 * time.Second)
	}
	if stepStart == 0 {
		t.Fatalf("the probe Kit's startup step never ran in %s — the probe measured nothing (sbx %s)", name, sbxVersion)
	}
	if agent == 0 {
		t.Fatalf("the shell agent never started in %s (step began %d, ended %d; sbx %s)", name, stepStart, stepEnd, sbxVersion)
	}
	if stepEnd != 0 && agent >= stepEnd {
		t.Errorf("sbx started the agent at %d, after the foreground startup step ended at %d (began %d). "+
			"Docker documents that startup steps \"don't gate the agent's entrypoint\" "+
			"(https://docs.docker.com/ai/sandboxes/customize/kits/). Report upstream with: sbx version %q",
			agent, stepEnd, stepStart, sbxVersion)
	}
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
