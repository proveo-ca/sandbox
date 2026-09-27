//go:build e2e

// SPEC: _spec/internal/schedule/schedule.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestScheduleFiresAtItsTimeAndTypesThePrompt drives the real tick → tmux → watcher chain with no
// model: a stand-in agent prints a ready marker and echoes the one line it is given. It asserts only
// the two things a schedule promises — WHEN it fired, and WHAT it typed.
func TestScheduleFiresAtItsTimeAndTypesThePrompt(t *testing.T) {
	requireTmux(t)
	proveo := buildProveo(t)

	home, err := os.MkdirTemp("/tmp", "sched")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	job := fmt.Sprintf("e2e-%d", os.Getpid())
	t.Cleanup(func() { tmux.New(schedule.SessionName(job), nil).Kill() })

	prompt := filepath.Join(home, "prompt.md")
	if err := os.WriteFile(prompt, []byte("Set the best lineup for Muse de Parchita.\nBench anyone tagged Out.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := `printf 'FAKE-READY> '; IFS= read -r line; printf '\nRECEIVED: %s\n' "$line"; sleep 2`
	cfg := fmt.Sprintf(`jobs:
  %s:
    command: ["sh", "-c", %q]
    ready: "FAKE-READY>"
    prompt_file: %s
    mode: goal
    budget: 2m
    tz: America/New_York
    at: ["sun 11:35"]
`, job, agent, prompt)
	if err := os.WriteFile(filepath.Join(home, schedule.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	tick := exec.Command(proveo, "schedule", "tick")
	tick.Env = append(os.Environ(), "PROVEO_HOME="+home, "PROVEO_SCHEDULE_NOW=2026-09-27T11:36:00-04:00")
	out, err := tick.CombinedOutput()
	if err != nil {
		t.Fatalf("tick: %v\n%s", err, out)
	}
	wantAt := fmt.Sprintf(`started %s for "sun 11:35" (due 2026-09-27T11:35:00-04:00)`, job)
	if !strings.Contains(string(out), wantAt) {
		t.Fatalf("tick output %q\nwant it to contain %q", out, wantAt)
	}

	var res *schedule.Result
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if res = schedule.LastResult(home, job); res != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if res == nil {
		t.Fatalf("the watcher left no result in %s within 90s", schedule.LogDir(home, job))
	}
	transcript, err := os.ReadFile(res.Transcript)
	if err != nil {
		t.Fatalf("transcript: %v (result %+v)", err, *res)
	}
	wantPrompt := "RECEIVED: /goal Set the best lineup for Muse de Parchita. Bench anyone tagged Out."
	if !strings.Contains(string(transcript), wantPrompt) {
		t.Errorf("transcript does not show the typed prompt %q; result %+v\n%s", wantPrompt, *res, transcript)
	}
}
