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
	agent := `printf 'FAKE-READY> '; IFS= read -r line; printf '\nRECEIVED: %s\n' "$line"; printf '# Report\nRESULT: typed ok\n' > report.md; sleep 2`
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
	if res.Summary != "typed ok" || !strings.HasSuffix(res.Report, ".report.md") {
		t.Errorf("the agent's report.md must land in the result: summary %q, report %q", res.Summary, res.Report)
	}
	wantPrompt := "RECEIVED: /goal Set the best lineup for Muse de Parchita. Bench anyone tagged Out."
	if !strings.Contains(string(transcript), wantPrompt) {
		t.Errorf("transcript does not show the typed prompt %q; result %+v\n%s", wantPrompt, *res, transcript)
	}
}

// TestScheduleFailedRunIsListedAndRetried drives a stand-in agent that exits 3 after taking its
// prompt: the watcher must record it failed, `proveo schedule` must name the retry, and
// `proveo schedule retry` must launch it again under the same entry.
func TestScheduleFailedRunIsListedAndRetried(t *testing.T) {
	requireTmux(t)
	proveo := buildProveo(t)

	home, err := os.MkdirTemp("/tmp", "sched")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	job := fmt.Sprintf("e2e-fail-%d", os.Getpid())
	t.Cleanup(func() { tmux.New(schedule.SessionName(job), nil).Kill() })

	prompt := filepath.Join(home, "prompt.md")
	if err := os.WriteFile(prompt, []byte("Set the lineup.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := `printf 'FAKE-READY> '; IFS= read -r line; exit 3`
	cfg := fmt.Sprintf(`jobs:
  %s:
    command: ["sh", "-c", %q]
    ready: "FAKE-READY>"
    prompt_file: %s
    budget: 2m
    tz: America/New_York
    at: ["sun 11:35"]
`, job, agent, prompt)
	if err := os.WriteFile(filepath.Join(home, schedule.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PROVEO_HOME="+home, "PROVEO_SCHEDULE_NOW=2026-09-27T11:36:00-04:00", "NO_COLOR=1")
	proveoCmd := func(args ...string) string {
		t.Helper()
		c := exec.Command(proveo, args...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("proveo %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	waitResult := func(n int) *schedule.Result {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			m, _ := filepath.Glob(filepath.Join(schedule.LogDir(home, job), "*.json"))
			if len(m) >= n {
				return schedule.LastResult(home, job)
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("no result #%d in %s within 90s", n, schedule.LogDir(home, job))
		return nil
	}

	proveoCmd("schedule", "tick")
	res := waitResult(1)
	if res.Outcome != "failed" || !strings.Contains(res.Detail, "exited 3") {
		t.Fatalf("result = %+v, want failed with exit 3", *res)
	}
	if ls := proveoCmd("schedule", "ls"); !strings.Contains(ls, "proveo schedule retry "+job) {
		t.Errorf("listing does not offer the retry:\n%s", ls)
	}

	time.Sleep(time.Second) // a distinct result stamp
	proveoCmd("schedule", "retry", job)
	if again := waitResult(2); again.Entry != "sun 11:35" || again.Outcome != "failed" {
		t.Errorf("retry result = %+v, want the same entry run again", *again)
	}
}

// TestScheduleAgentThatDiesBeforeReadyFailsFast pins the early-exit path: an agent that quits
// before its ready marker must be recorded failed with its exit status and last line within
// seconds, not after the 10-minute ready wait.
func TestScheduleAgentThatDiesBeforeReadyFailsFast(t *testing.T) {
	requireTmux(t)
	proveo := buildProveo(t)

	home, err := os.MkdirTemp("/tmp", "sched")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	job := fmt.Sprintf("e2e-dies-%d", os.Getpid())
	t.Cleanup(func() { tmux.New(schedule.SessionName(job), nil).Kill() })

	prompt := filepath.Join(home, "prompt.md")
	if err := os.WriteFile(prompt, []byte("Set the lineup.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`jobs:
  %s:
    command: ["sh", "-c", "echo 'x local model: needs 21 GiB'; sleep 1; exit 4"]
    ready: "FAKE-READY>"
    prompt_file: %s
    tz: America/New_York
    at: ["sun 11:35"]
`, job, prompt)
	if err := os.WriteFile(filepath.Join(home, schedule.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(proveo, "schedule", "run", job)
	c.Env = append(os.Environ(), "PROVEO_HOME="+home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("schedule run: %v\n%s", err, out)
	}
	var res *schedule.Result
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if res = schedule.LastResult(home, job); res != nil {
			break
		}
	}
	if res == nil {
		t.Fatalf("no result within 30s: the watcher is still waiting for a ready marker from a dead session")
	}
	if res.Outcome != "failed" || !strings.Contains(res.Detail, "exited 4 before it was ready") ||
		!strings.Contains(res.Detail, "needs 21 GiB") {
		t.Errorf("result = %+v, want failed with exit 4 and the agent's last line", *res)
	}
}
