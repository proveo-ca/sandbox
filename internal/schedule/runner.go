// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

const (
	DefaultReady = "❯"
	readyWait    = 10 * time.Minute
	exitGrace    = 90 * time.Second
)

// SessionName is the tmux session a job runs in; one per job at a time.
func SessionName(job string) string { return "proveo-sched-" + safe(job) }

var unsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func safe(s string) string { return unsafe.ReplaceAllString(s, "-") }

// LogDir holds a job's transcripts and results.
func LogDir(home, job string) string { return filepath.Join(home, "logs", "schedule", safe(job)) }

// RunArgv is the command a job launches: its own command, else an unattended `proveo run`.
func (j Job) RunArgv(proveo string) []string {
	if len(j.Command) > 0 {
		return append([]string(nil), j.Command...)
	}
	argv := []string{proveo, "run", j.Target, "--yes"}
	if j.Model != "" {
		argv = append(argv, "--local-model", j.Model)
	}
	for _, a := range j.Addons {
		argv = append(argv, "--addon", a)
	}
	return append(argv, j.Args...)
}

// Instruction is the one line typed into the agent: the prompt file flattened, under the job's mode.
func (j Job) Instruction(prompt string) string {
	flat := strings.Join(strings.Fields(prompt), " ")
	switch j.ModeOrDefault() {
	case "goal":
		return "/goal " + flat
	case "loop":
		return "/loop " + flat
	}
	return flat
}

func (j Job) readyMarker() string {
	if j.Ready != "" {
		return j.Ready
	}
	return DefaultReady
}

// Result is what one run leaves behind.
type Result struct {
	Job        string    `json:"job"`
	Entry      string    `json:"entry"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Outcome    string    `json:"outcome"` // ended | budget | not-ready | failed
	Detail     string    `json:"detail,omitempty"`
	Transcript string    `json:"transcript"`
}

func tmuxRun(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return string(out), err
}

func orTmux(run tmux.Runner) tmux.Runner {
	if run == nil {
		return tmuxRun
	}
	return run
}

// Launch starts the job's tmux session and a detached watcher; it returns once both run.
func Launch(home, proveo, name, entry string, j Job, run tmux.Runner) error {
	run = orTmux(run)
	sess := tmux.New(SessionName(name), run)
	if _, err := run("has-session", "-t", sess.Name); err == nil {
		return fmt.Errorf("%s is still running (tmux session %s)", name, sess.Name)
	}
	dir := LogDir(home, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	transcript := filepath.Join(dir, stamp+".log")
	wd := j.Workdir
	if wd == "" {
		wd = filepath.Join(home, "schedule", safe(name))
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return err
	}
	cmd := "cd " + shellQuote(wd) + " && exec " + shellJoin(j.RunArgv(proveo))
	if err := sess.Start(200, 50, "sh", "-c", cmd); err != nil {
		return fmt.Errorf("start tmux session: %w", err)
	}
	if _, err := run("pipe-pane", "-t", sess.Name, "-o", "cat >> "+shellQuote(transcript)); err != nil {
		sess.Kill()
		return fmt.Errorf("tee transcript: %w", err)
	}
	w := exec.Command(proveo, "schedule", "watch", name, "--entry", entry, "--transcript", transcript)
	w.Stdout, w.Stderr = nil, nil
	detach(w)
	if err := w.Start(); err != nil {
		sess.Kill()
		return fmt.Errorf("start watcher: %w", err)
	}
	_ = w.Process.Release()
	return nil
}

// Watch types the instruction once the agent is ready, then ends the session at the budget.
func Watch(home, name, entry, transcript string, j Job, run tmux.Runner, notify func(title, body string)) Result {
	res := Result{Job: name, Entry: entry, Started: time.Now(), Transcript: transcript}
	finish := func(outcome, detail string) Result {
		res.Outcome, res.Detail, res.Finished = outcome, detail, time.Now()
		writeResult(home, name, res)
		if notify != nil {
			notify("proveo "+name, outcome+": "+detail)
		}
		return res
	}
	stopAwake := keepAwake()
	defer stopAwake()

	run = orTmux(run)
	sess := tmux.New(SessionName(name), run)
	prompt, err := os.ReadFile(j.PromptFile)
	if err != nil {
		sess.Kill()
		return finish("failed", "prompt: "+err.Error())
	}
	if _, err := sess.WaitFor(j.readyMarker(), readyWait); err != nil {
		pane, _ := sess.Capture()
		sess.Kill()
		return finish("not-ready", fmt.Sprintf("no %q within %s; pane tail: %s", j.readyMarker(), readyWait, tail(pane, 300)))
	}
	if err := sess.SendText(j.Instruction(string(prompt))); err != nil {
		sess.Kill()
		return finish("failed", "type instruction: "+err.Error())
	}
	if err := sess.Enter(); err != nil {
		sess.Kill()
		return finish("failed", "submit instruction: "+err.Error())
	}
	budget, _ := j.BudgetDuration()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := run("has-session", "-t", sess.Name); err != nil {
			return finish("ended", "the agent exited before the budget")
		}
		time.Sleep(15 * time.Second)
	}
	_ = sess.SendText("/exit")
	_ = sess.Enter()
	end := time.Now().Add(exitGrace)
	for time.Now().Before(end) {
		if _, err := run("has-session", "-t", sess.Name); err != nil {
			return finish("budget", fmt.Sprintf("stopped at %s with /exit", budget))
		}
		time.Sleep(5 * time.Second)
	}
	sess.Kill()
	return finish("budget", fmt.Sprintf("stopped at %s; /exit did not end it within %s, session killed", budget, exitGrace))
}

func writeResult(home, name string, r Result) {
	dir := LogDir(home, name)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, r.Started.Format("20060102-150405")+".json"), b, 0o600)
}

// LastResult is the newest result a job left, or nil.
func LastResult(home, name string) *Result {
	m, _ := filepath.Glob(filepath.Join(LogDir(home, name), "*.json"))
	if len(m) == 0 {
		return nil
	}
	newest := m[len(m)-1]
	b, err := os.ReadFile(newest)
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// keepAwake holds off idle sleep on macOS while the run lasts.
func keepAwake() func() {
	if runtime.GOOS != "darwin" {
		return func() {}
	}
	c := exec.Command("caffeinate", "-i", "-w", fmt.Sprint(os.Getpid()))
	if c.Start() != nil {
		return func() {}
	}
	return func() { _ = c.Process.Kill() }
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}
