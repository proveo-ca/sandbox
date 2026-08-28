//go:build e2e

// SPEC: _spec/internal/proveohome/proveo-home-lifecycle.puml
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestResumeStateSurvivesSandboxTeardown is the sbx-side half of resumability.
//
// The existing resume assertions in home_test.go all run on the DOCKER backend,
// where HOME points at the mounted proveo home and transcripts persist for free.
// That is why dropping the HOME redirect on sbx broke `--resume` without turning
// a single test red: nothing asserted persistence on the backend that had just
// lost it. sbx mounts ~/.claude/projects as a per-sandbox volume and teardown
// removes "VM + images + volumes", so the transcripts died with the run.
//
// The assertion is deliberately made AFTER `sbx rm --force`: surviving the
// teardown is the property, and checking while the sandbox still exists would
// pass even with no persistence whatsoever.
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
// statsig and shell-snapshots are also per-sandbox volumes, and copying them
// back would grow the operator's home every run without ever being read.
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

func sbxExec(t *testing.T, name, script string) string {
	t.Helper()
	out, err := exec.Command("sbx", "exec", name, "--", "bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sbx exec %s: %v\n%s", name, err, out)
	}
	return string(out)
}
