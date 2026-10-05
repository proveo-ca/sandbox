// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const (
	DefaultReady = "❯"
	readyWait    = 10 * time.Minute
	typeAttempts = 3
)

var (
	pollEvery     = 500 * time.Millisecond
	settleQuiet   = 3 * time.Second
	settleMax     = time.Minute
	acceptWait    = 30 * time.Second
	submitWait    = 5 * time.Second
	budgetPoll    = 15 * time.Second
	exitPoll      = 5 * time.Second
	interruptWait = 5 * time.Second
	exitGrace     = 90 * time.Second
	termGrace     = time.Minute
	doneQuiet     = 20 * time.Second
	doneMax       = 3 * time.Minute
)

// defaultAccepted is the pane text a harness shows once it took an instruction, by target and mode.
var defaultAccepted = map[string]string{"hermes goal": "Goal (active"}

var errGone = errors.New("session ended")

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

// acceptedMarker is the pane text that proves the agent took the instruction: the job's, the
// harness's, else the prompt's opening words.
func (j Job) acceptedMarker(prompt string) string {
	if j.Accepted != "" {
		return j.Accepted
	}
	if m, ok := defaultAccepted[j.Target+" "+j.ModeOrDefault()]; ok && len(j.Command) == 0 {
		return m
	}
	r := []rune(strings.Join(strings.Fields(prompt), " "))
	return string(r[:min(24, len(r))])
}

// awaitText polls the pane for text; errGone when the session ends first.
func awaitText(sess *tmux.Session, text string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if !sess.Alive() {
			return false, errGone
		}
		if pane, err := sess.Capture(); err == nil && strings.Contains(pane, text) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(pollEvery)
	}
}

// awaitReady waits for the ready marker, then for the pane to hold still for settleQuiet.
func awaitReady(sess *tmux.Session, marker string, timeout time.Duration) (string, error) {
	seen, err := awaitText(sess, marker, timeout)
	if err != nil {
		return "", err
	}
	pane, _ := sess.Capture()
	if !seen {
		return pane, fmt.Errorf("no %q within %s", marker, timeout)
	}
	return awaitQuiet(sess, pane, settleQuiet, settleMax)
}

// awaitQuiet waits until the pane holds still for quietFor, or max passes.
func awaitQuiet(sess *tmux.Session, pane string, quietFor, max time.Duration) (string, error) {
	quiet, end := time.Now(), time.Now().Add(max)
	for time.Since(quiet) < quietFor && time.Now().Before(end) {
		time.Sleep(pollEvery)
		if !sess.Alive() {
			return pane, errGone
		}
		if cur, err := sess.Capture(); err == nil && cur != pane {
			pane, quiet = cur, time.Now()
		}
	}
	return pane, nil
}

// endedEarly is ended when the agent exited 0 (or unknown) after taking its instruction, else failed.
func endedEarly(finish func(outcome, detail string) Result, transcript string) Result {
	if code, ok := exitStatus(transcript); ok && code != 0 {
		return finish("failed", exitedDetail(transcript, "before the budget"))
	}
	return finish("ended", "the agent exited before the budget")
}

// exitedDetail names the agent's exit status and the transcript's last lines.
func exitedDetail(transcript, when string) string {
	d := "the agent exited " + when
	if code, ok := exitStatus(transcript); ok {
		d = fmt.Sprintf("the agent exited %d %s", code, when)
	}
	if b, err := os.ReadFile(transcript); err == nil {
		t := agentio.NewTail(3)
		_, _ = t.Write(b)
		if lines := t.Lines(); len(lines) > 0 {
			d += ": " + strings.Join(lines, " | ")
		}
	}
	return d
}

// ReportFile is where a prompt asks the agent to write its final report, in the job's workdir.
const ReportFile = "report.md"

// WorkDir is the directory a job's agent runs in (and proveo mounts as its workspace).
func (j Job) WorkDir(home, name string) string {
	if j.Workdir != "" {
		return j.Workdir
	}
	return filepath.Join(home, "schedule", safe(name))
}

// collectReport moves the agent's report next to the transcript and returns its RESULT line.
func collectReport(report, transcript string) (summary, path string) {
	b, err := os.ReadFile(report)
	if err != nil {
		return "", ""
	}
	path = strings.TrimSuffix(transcript, ".log") + ".report.md"
	if os.Rename(report, path) != nil {
		path = report
	}
	return reportSummary(string(b)), path
}

// reportSummary is the report's first "RESULT:" line, else its first non-empty line.
func reportSummary(report string) string {
	first := ""
	for l := range strings.SplitSeq(report, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#*-> "))
		if l == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(l, "RESULT:"); ok {
			return strings.TrimSpace(strings.TrimLeft(rest, "*_ "))
		}
		if first == "" {
			first = l
		}
	}
	return first
}

// Result is what one run leaves behind.
type Result struct {
	Job        string    `json:"job"`
	Entry      string    `json:"entry"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Outcome    string    `json:"outcome"` // done | ended | budget | not-ready | failed
	Detail     string    `json:"detail,omitempty"`
	Transcript string    `json:"transcript"`
	Summary    string    `json:"summary,omitempty"` // the report's RESULT line
	Report     string    `json:"report,omitempty"`  // the agent's report, moved next to the transcript
}

// Failed reports whether the run needs a retry.
func (r Result) Failed() bool { return r.Outcome == "failed" || r.Outcome == "not-ready" }

// ExitFile holds the launched command's exit status, next to its transcript.
func ExitFile(transcript string) string { return strings.TrimSuffix(transcript, ".log") + ".exit" }

func exitStatus(transcript string) (int, bool) {
	b, err := os.ReadFile(ExitFile(transcript))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil
}

// RecordLaunchFailure leaves a failed result for a run that never started.
func RecordLaunchFailure(home, name, entry string, err error) Result {
	now := time.Now()
	r := Result{Job: name, Entry: entry, Started: now, Finished: now, Outcome: "failed", Detail: "launch: " + err.Error()}
	writeResult(home, name, r)
	return r
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

// ErrRunning means the job's previous run still holds its tmux session.
var ErrRunning = errors.New("still running")

// Launch starts the job's tmux session and a detached watcher; it returns once both run.
func Launch(home, proveo, name, entry string, j Job, run tmux.Runner) error {
	run = orTmux(run)
	sess := tmux.New(SessionName(name), run)
	if sess.Alive() {
		return fmt.Errorf("%s: %w (tmux session %s)", name, ErrRunning, sess.Name)
	}
	dir := LogDir(home, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	transcript := filepath.Join(dir, stamp+".log")
	wd := j.WorkDir(home, name)
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return err
	}
	cmd := "cd " + shellQuote(wd) + " && " + shellJoin(j.RunArgv(proveo)) + "; echo $? > " + shellQuote(ExitFile(transcript))
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
	return watch(home, name, entry, transcript, j, run, notify, false)
}

// Rewatch takes over a run whose instruction was already typed: it only waits for the report or the budget.
func Rewatch(home, name, entry, transcript string, j Job, run tmux.Runner, notify func(title, body string)) Result {
	return watch(home, name, entry, transcript, j, run, notify, true)
}

func watch(home, name, entry, transcript string, j Job, run tmux.Runner, notify func(title, body string), typed bool) Result {
	res := Result{Job: name, Entry: entry, Started: time.Now(), Transcript: transcript}
	report := filepath.Join(j.WorkDir(home, name), ReportFile)
	if !typed {
		_ = os.Remove(report)
	}
	finish := func(outcome, detail string) Result {
		res.Outcome, res.Detail, res.Finished = outcome, detail, time.Now()
		res.Summary, res.Report = collectReport(report, transcript)
		writeResult(home, name, res)
		if notify != nil {
			body := outcome + ": " + detail
			if res.Summary != "" {
				body = outcome + ": " + res.Summary
			}
			notify("proveo "+name, body)
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
	instruction := j.Instruction(string(prompt))
	for attempt := 1; !typed; attempt++ {
		pane, err := awaitReady(sess, j.readyMarker(), readyWait)
		if errors.Is(err, errGone) {
			return finish("failed", exitedDetail(transcript, "before it was ready"))
		}
		if err != nil {
			sess.Kill()
			return finish("not-ready", fmt.Sprintf("no %q within %s; pane tail: %s", j.readyMarker(), readyWait, tail(pane, 300)))
		}
		if err := sess.SendText(instruction); err != nil {
			sess.Kill()
			return finish("failed", "type instruction: "+err.Error())
		}
		if err := sess.Enter(); err != nil {
			sess.Kill()
			return finish("failed", "submit instruction: "+err.Error())
		}
		took, err := awaitText(sess, j.acceptedMarker(string(prompt)), submitWait)
		if err == nil && !took {
			_ = sess.Enter()
			took, err = awaitText(sess, j.acceptedMarker(string(prompt)), acceptWait)
		}
		if errors.Is(err, errGone) {
			return endedEarly(finish, transcript)
		}
		if took {
			break
		}
		if attempt == typeAttempts {
			sess.Kill()
			return finish("failed", fmt.Sprintf("the agent never showed %q after %d tries", j.acceptedMarker(string(prompt)), typeAttempts))
		}
	}
	budget, _ := j.BudgetDuration()
	started := time.Now()
	deadline := started.Add(budget)
	for time.Now().Before(deadline) {
		if !sess.Alive() {
			return endedEarly(finish, transcript)
		}
		if fi, err := os.Stat(report); err == nil && fi.Size() > 0 {
			pane, _ := sess.Capture()
			if _, err := awaitQuiet(sess, pane, doneQuiet, doneMax); errors.Is(err, errGone) {
				return endedEarly(finish, transcript)
			}
			took := compact(time.Since(started).Round(time.Minute))
			return finish("done", "report written after "+took+"; "+stopAgent(sess, "stopped early"))
		}
		time.Sleep(budgetPoll)
	}
	return finish("budget", stopAgent(sess, "stopped at "+compact(budget)))
}

// stopAgent ends a run at its budget, gentlest first: interrupt the turn and /exit, then
// SIGTERM the pane's command so `proveo run` tears its sandbox down, then kill the session.
func stopAgent(sess *tmux.Session, why string) string {
	_ = sess.SendKeys("C-c")
	time.Sleep(interruptWait)
	_ = sess.SendText("/exit")
	_ = sess.Enter()
	time.Sleep(interruptWait)
	if sess.Alive() {
		_ = sess.Enter()
	}
	if awaitGone(sess, exitGrace) {
		return why + " with /exit"
	}
	if pid, err := panePID(sess); err == nil && terminateChildren(pid) == nil && awaitGone(sess, termGrace) {
		return fmt.Sprintf("%s; /exit did not end it within %s, SIGTERM did", why, exitGrace)
	}
	sess.Kill()
	return why + "; neither /exit nor SIGTERM ended it, session killed"
}

func awaitGone(sess *tmux.Session, timeout time.Duration) bool {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(exitPoll) {
		if !sess.Alive() {
			return true
		}
	}
	return !sess.Alive()
}

func panePID(sess *tmux.Session) (int, error) {
	out, err := sess.PanePID()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
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

// CurrentRun is when the run in progress started: the newest transcript without an exit file, or zero.
func CurrentRun(home, name string) time.Time {
	m, _ := filepath.Glob(filepath.Join(LogDir(home, name), "*.log"))
	for i := len(m) - 1; i >= 0; i-- {
		if _, err := os.Stat(ExitFile(m[i])); err == nil {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", strings.TrimSuffix(filepath.Base(m[i]), ".log"), time.Local)
		if err == nil {
			return t
		}
	}
	return time.Time{}
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
