//go:build e2e

// SPEC: _spec/internal/proveohome/proveo-home-lifecycle.puml, _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// resumeWorkload is a probe workload carrying the published claude kit's lifecycle hooks; the kit declares no volume.
func resumeWorkload(t *testing.T, img string) string {
	t.Helper()
	k, ok, err := sbx.UpstreamKitFor("claudecode", sbx.UpstreamOptions{})
	if err != nil || !ok {
		t.Fatalf("published claude kit: ok=%v err=%v", ok, err)
	}
	var caps []sbx.CapabilityV3
	for _, c := range k.Capabilities {
		if c.Type == sbx.CapVolume {
			t.Fatalf("the vendored claude kit declares %s again", sbx.CapVolume)
		}
		if c.Type == sbx.CapLifecycle {
			caps = append(caps, c)
		}
	}
	return probeWorkload(t, "proveo-resume", sbx.RecipeV3{From: img}, caps...)
}

// resumeEnv is the sync env proveo hands a claudecode sandbox whose proveo home is home.
func resumeEnv(t *testing.T, home string) []string {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "claudecode" {
			return []string{sbx.StateHomeVar + "=" + home,
				proveohome.ConfigSetVar + "=" + proveohome.ConfigSet(m.Home),
				proveohome.StateSetVar + "=" + proveohome.StateSet(m.Home)}
		}
	}
	t.Fatal("no claudecode manifest")
	return nil
}

// TestResumeStateSurvivesSandboxTeardown is the sbx-side half of
// resumability.
func TestResumeStateSurvivesSandboxTeardown(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")

	// Stand in for the operator's proveo home, holding one earlier session.
	home := t.TempDir()
	prior := filepath.Join(home, ".claude", "projects", "-work-prior", "prior.jsonl")
	if err := os.MkdirAll(filepath.Dir(prior), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prior, []byte(`{"session":"prior"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, ".claude", "projects", "-work-other", "other.jsonl")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"session":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "resume-guard-" + filepath.Base(home)
	// -e PROVEO_STATE_HOME is how proveo publishes the host path; the save step
	// reads it from the sandbox environment rather than being handed it again.
	createWorkloadSandbox(t, sbx.RunConfig{Name: name, KitDir: resumeWorkload(t, img),
		Env: resumeEnv(t, home), Mounts: mountsOf(home)})

	// The seed's half: yesterday's transcripts must be in place before the agent
	// looks for them, in the image home's plain directory.
	restore := sbxExec(t, name, `. /entrypoint-lib.sh && PROVEO_WORKDIR=/work/prior proveo_sync_state restore
printf 'restored=%s\n' "$(find "$HOME/.claude/projects" -name '*.jsonl' | wc -l | tr -d ' ')"`)
	if !strings.Contains(restore, "restored=1") {
		t.Errorf("want the workdir's earlier session in the sandbox and no other workspace's:\n%s", restore)
	}

	// Stand in for the run itself producing a new transcript and its VM-local session record.
	sbxExec(t, name, `mkdir -p "$HOME/.claude/projects/-work-live" "$HOME/.claude/sessions" &&
echo '{"session":"live"}' > "$HOME/.claude/projects/-work-live/live.jsonl" &&
echo '{"pid":1}' > "$HOME/.claude/sessions/1.json"`)

	// The teardown's half, through the exact argv proveo uses.
	save := exec.Command("sbx", sbx.SaveStateArgs(name)...)
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("save state: %v\n%s", err, out)
	}

	if out, err := removeSandbox(t, name); err != nil {
		t.Fatalf("teardown: %v\n%s", err, out)
	}

	// The property: the sandbox is gone, and the transcripts are not.
	if _, err := os.Stat(filepath.Join(home, ".claude", "sessions")); err == nil {
		t.Error("the save carried the VM-local sessions/ to the host")
	}
	live := filepath.Join(home, ".claude", "projects", "-work-live", "live.jsonl")
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the session written during the run did not survive teardown, so "+
			"`--resume` has nothing to offer next run: %v", err)
	}
	if _, err := os.Stat(prior); err != nil {
		t.Errorf("the earlier session was lost on the way back: %v", err)
	}
}

// TestSaveStateKeepsVMLocalPathsInTheSandbox carries only state worth resuming.
func TestSaveStateKeepsVMLocalPathsInTheSandbox(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")

	home := t.TempDir()
	name := "resume-skip-" + filepath.Base(home)
	createWorkloadSandbox(t, sbx.RunConfig{Name: name, KitDir: resumeWorkload(t, img),
		Env: resumeEnv(t, home), Mounts: mountsOf(home)})

	vmLocal := []string{"sessions/1.json", "statsig/cache", "shell-snapshots/s.sh", "daemon.lock", "daemon.status.json",
		"daemon.log", "daemon/roster.json", "jobs/j1/state.json"}
	write := `. /entrypoint-lib.sh && proveo_sync_state restore && mkdir -p "$HOME/.claude/projects/-work-live" &&
echo turn > "$HOME/.claude/projects/-work-live/live.jsonl"`
	for _, rel := range vmLocal {
		write += ` && mkdir -p "$(dirname "$HOME/.claude/` + rel + `")" && echo x > "$HOME/.claude/` + rel + `"`
	}
	sbxExec(t, name, write)
	if out, err := exec.Command("sbx", sbx.SaveStateArgs(name)...).CombinedOutput(); err != nil {
		t.Fatalf("save state: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", "-work-live", "live.jsonl")); err != nil {
		t.Errorf("the transcript did not reach the host: %v", err)
	}
	for _, rel := range vmLocal {
		if _, err := os.Lstat(filepath.Join(home, ".claude", rel)); err == nil {
			t.Errorf(".claude/%s is VM-local and reached the host", rel)
		}
	}
}

// TestSaveStateSurvivesAnInvalidatedWorkspaceCwd is the teardown half of the
// virtiofs failure in _spec/internal/sbx/virtiofs-cwd-invalidation.puml.
func TestSaveStateSurvivesAnInvalidatedWorkspaceCwd(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")

	home := t.TempDir()
	parent := t.TempDir()
	ws := filepath.Join(parent, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "resume-cwd-" + filepath.Base(parent)
	createWorkloadSandbox(t, sbx.RunConfig{Name: name, KitDir: resumeWorkload(t, img),
		Env: resumeEnv(t, home), Mounts: mountsOf(ws, home)})

	// The seed's restore, then the run's transcript, written while the workspace is still healthy.
	sbxExec(t, name, `. /entrypoint-lib.sh && proveo_sync_state restore && mkdir -p "$HOME/.claude/projects/-work-live" &&
echo '{"session":"live"}' > "$HOME/.claude/projects/-work-live/live.jsonl"`)

	// Invalidate the guest's cwd from the host, under the running sandbox.
	if err := os.Rename(ws, ws+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	var probe string
	deadline := time.Now().Add(30 * time.Second)
	for probe == "" && time.Now().Before(deadline) {
		out, err := exec.Command("sbx", "exec", name, "--", "true").CombinedOutput()
		switch {
		case err == nil:
			time.Sleep(time.Second)
		case strings.Contains(string(out), "getcwd"):
			probe = strings.TrimSpace(string(out))
		default:
			t.Fatalf("the probe exec failed, but not at the cwd: %v\n%s", err, out)
		}
	}
	if probe == "" {
		t.Skip("replacing the host directory no longer invalidates the guest cwd on this sbx; " +
			"the fixture cannot reproduce the failure the save has to survive")
	}
	t.Logf("workspace cwd invalidated as measured: %s", probe)

	// The teardown's half, through the exact argv proveo uses.
	if out, err := exec.Command("sbx", sbx.SaveStateArgs(name)...).CombinedOutput(); err != nil {
		t.Fatalf("the teardown save inherited the dead workspace cwd (%v):\n%s\n"+
			"a plain exec had just failed with: %s", err, out, probe)
	}
	live := filepath.Join(home, ".claude", "projects", "-work-live", "live.jsonl")
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the transcript did not reach the host, so `--resume` has nothing to offer: %v", err)
	}
}

func sbxExec(t *testing.T, name, script string) string {
	t.Helper()
	out, err := exec.Command("sbx", "exec", "-w", "/", name, "--", "bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sbx exec %s: %v\n%s", name, err, out)
	}
	return string(out)
}
