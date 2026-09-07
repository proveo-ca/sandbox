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

	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestResumeStateSurvivesSandboxTeardown is the sbx-side half of
// resumability.
func TestResumeStateSurvivesSandboxTeardown(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")
	freshTemplate(t, img)

	// Stand in for the operator's proveo home, holding one earlier session.
	home := t.TempDir()
	prior := filepath.Join(home, ".claude", "projects", "-work-prior", "prior.jsonl")
	if err := os.MkdirAll(filepath.Dir(prior), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prior, []byte(`{"session":"prior"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "resume-guard-" + filepath.Base(home)
	// -e PROVEO_STATE_HOME is how proveo publishes the host path; the save step
	// reads it from the sandbox environment rather than being handed it again.
	create := exec.Command("sbx", "create", "--name", name, "-t", img,
		"-e", sbx.StateHomeVar+"="+home, "claude", home)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("sbx create: %v\n%s", err, out)
	}
	removed := false
	t.Cleanup(func() {
		if !removed {
			_ = exec.Command("sbx", "rm", "--force", name).Run()
		}
	})

	// The seed's half: yesterday's transcripts must be in place before the agent
	// looks for them, and the volume must be the thing they land in.
	restore := sbxExec(t, name, `. /entrypoint-lib.sh && proveo_sync_state restore
printf 'volumes=%s\n' "$(_proveo_volume_state_dirs | tr '\n' ',')"
printf 'restored=%s\n' "$(find "$HOME/.claude/projects" -name '*.jsonl' | wc -l | tr -d ' ')"`)
	if !strings.Contains(restore, "/.claude/projects") {
		t.Errorf("the projects volume was not discovered, so nothing would be saved:\n%s", restore)
	}
	if !strings.Contains(restore, "restored=1") {
		t.Errorf("the earlier session did not reach the sandbox:\n%s", restore)
	}

	// Stand in for the run itself producing a new transcript.
	sbxExec(t, name, `mkdir -p "$HOME/.claude/projects/-work-live" &&
echo '{"session":"live"}' > "$HOME/.claude/projects/-work-live/live.jsonl"`)

	// The teardown's half, through the exact argv proveo uses.
	save := exec.Command("sbx", sbx.SaveStateArgs(name)...)
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("save state: %v\n%s", err, out)
	}

	if out, err := exec.Command("sbx", sbx.RemoveArgs(name)...).CombinedOutput(); err != nil {
		t.Fatalf("teardown: %v\n%s", err, out)
	}
	removed = true

	// The property: the volumes are gone, and the transcripts are not.
	live := filepath.Join(home, ".claude", "projects", "-work-live", "live.jsonl")
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the session written during the run did not survive teardown, so "+
			"`--resume` has nothing to offer next run: %v", err)
	}
	if _, err := os.Stat(prior); err != nil {
		t.Errorf("the earlier session was lost on the way back: %v", err)
	}
}

// TestSaveStateSkipsTelemetryVolumes keeps the copy to state worth resuming.
func TestSaveStateSkipsTelemetryVolumes(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")
	freshTemplate(t, img)

	name := "resume-skip-" + filepath.Base(t.TempDir())
	if out, err := exec.Command("sbx", "create", "--name", name, "-t", img,
		"claude", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	got := sbxExec(t, name, `. /entrypoint-lib.sh && _proveo_volume_state_dirs`)
	for _, skip := range []string{"statsig", "shell-snapshots"} {
		if strings.Contains(got, skip) {
			t.Errorf("%s is telemetry/scratch and must not be copied home:\n%s", skip, got)
		}
	}
	if !strings.Contains(got, "projects") {
		t.Errorf("the projects volume must still be listed:\n%s", got)
	}
}

// TestSaveStateSurvivesAnInvalidatedWorkspaceCwd is the teardown half of the
// virtiofs failure in _spec/internal/sbx/virtiofs-cwd-invalidation.puml.
func TestSaveStateSurvivesAnInvalidatedWorkspaceCwd(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	img := harnessImage(t, "claudecode")
	freshTemplate(t, img)

	home := t.TempDir()
	parent := t.TempDir()
	ws := filepath.Join(parent, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "resume-cwd-" + filepath.Base(parent)
	create := exec.Command("sbx", "create", "--name", name, "-t", img,
		"-e", sbx.StateHomeVar+"="+home, "claude", ws, home)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "--force", name).Run() })

	// The run's transcript, written while the workspace is still healthy.
	sbxExec(t, name, `mkdir -p "$HOME/.claude/projects/-work-live" &&
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
