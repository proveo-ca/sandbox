//go:build e2e

// SPEC: _spec/internal/sbx/startup-dispatch.puml, _spec/internal/sbx/sbx-kit-v3.puml
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// dispatchProbeImage is a stock sbx template, so the probe measures sbx and no proveo image.
const dispatchProbeImage = "docker/sandbox-templates:shell-docker"

// TestWorkloadCreateWaitsForForegroundStartupHooksOnly pins P5 on the host's sbx: `sbx create`
// returns after a foreground startup hook ends, and a background one does not hold it.
func TestWorkloadCreateWaitsForForegroundStartupHooksOnly(t *testing.T) {
	requireDocker(t)
	if ok, why := sbxReadyForTests(); !ok {
		t.Skipf("sbx not available: %s", why)
	}
	step := durationEnv(t, "PROVEO_DISPATCH_STEP", 20*time.Second)
	secs := int(step.Seconds())
	version, _ := exec.Command(sbx.Binary, "version").CombinedOutput()
	t.Logf("sbx version: %s", strings.TrimSpace(string(version)))

	hook := func(tag string, sleep int, background bool) sbx.HookV3 {
		return sbx.HookV3{
			Command: []string{"/bin/sh", "-c", fmt.Sprintf(
				"date +%%s > /tmp/proveo-probe-%[1]s-start; sleep %[2]d; date +%%s > /tmp/proveo-probe-%[1]s-end", tag, sleep)},
			Background:  background,
			Description: "proveo probe: " + tag + " startup hook",
		}
	}
	kit := probeWorkload(t, "proveo-dispatch-probe", sbx.RecipeV3{From: dispatchProbeImage}, sbx.CapabilityV3{
		Type:   sbx.CapLifecycle,
		Config: sbx.LifecycleV3{Startup: []sbx.HookV3{hook("fg", secs, false), hook("bg", 3*secs, true)}},
	})
	name := fmt.Sprintf("proveo-dispatch-%d", time.Now().UnixNano()%1_000_000_000)
	began := time.Now()
	createWorkloadSandbox(t, sbx.RunConfig{Name: name, KitDir: kit, Mounts: mountsOf(t.TempDir())})
	took := time.Since(began)

	got := probeDispatch(t, name, "fg-start", "fg-end", "bg-start", "bg-end")
	if got["fg-start"] == 0 || got["bg-start"] == 0 {
		t.Fatalf("a startup hook never ran (%v) — the probe measured nothing", got)
	}
	if got["fg-end"] == 0 {
		t.Errorf("sbx create returned after %s, before the %s foreground hook ended (%v); "+
			"proveo launches right after create and relies on P5", took.Round(time.Second), step, got)
	}
	if got["bg-end"] != 0 {
		t.Errorf("sbx create waited %s for the %s background hook (%v); proveo's seed is backgrounded "+
			"so the agent never waits on the toolchain", took.Round(time.Second), 3*step, got)
	}
}

// probeDispatch reads the probe's timestamp files from inside the sandbox; a missing one reads as 0.
func probeDispatch(t *testing.T, name string, tags ...string) map[string]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	script := "for f in " + strings.Join(tags, " ") + "; do cat /tmp/proveo-probe-$f 2>/dev/null || echo 0; done"
	out, err := exec.CommandContext(ctx, sbx.Binary, "exec", name, "--", "sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("read the probe's timestamps: %v", err)
	}
	got := map[string]int64{}
	for i, l := range strings.Fields(string(out)) {
		if i < len(tags) {
			got[tags[i]], _ = strconv.ParseInt(l, 10, 64)
		}
	}
	return got
}
