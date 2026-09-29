// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

func ny(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseAtReadsWeekdayAndClock(t *testing.T) {
	t.Parallel()
	wd, h, m, err := parseAt("Thu 18:45")
	if err != nil || wd != time.Thursday || h != 18 || m != 45 {
		t.Errorf("parseAt = %v %d:%d %v", wd, h, m, err)
	}
	for _, bad := range []string{"thu", "xyz 10:00", "sun 25:00", "mon 9", "tue 10:61"} {
		if _, _, _, err := parseAt(bad); err == nil {
			t.Errorf("parseAt(%q) must fail", bad)
		}
	}
}

func TestOccurrencesAreInTheJobsTimezone(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	// Sunday 2026-09-27 16:00 EDT.
	now := time.Date(2026, 9, 27, 16, 0, 0, 0, loc)
	last, _ := lastOccurrence("sun 11:35", now, loc)
	if want := time.Date(2026, 9, 27, 11, 35, 0, 0, loc); !last.Equal(want) {
		t.Errorf("last = %v, want %v", last, want)
	}
	last, _ = lastOccurrence("sun 18:55", now, loc)
	if want := time.Date(2026, 9, 20, 18, 55, 0, 0, loc); !last.Equal(want) {
		t.Errorf("an entry later today must resolve to last week: %v, want %v", last, want)
	}
	next, _ := NextOccurrence("mon 18:50", now, loc)
	if want := time.Date(2026, 9, 28, 18, 50, 0, 0, loc); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
	// The clock time holds across the DST change (Nov 1 2026, EDT → EST).
	after := time.Date(2026, 11, 2, 12, 0, 0, 0, loc)
	last, _ = lastOccurrence("sun 11:35", after, loc)
	if last.Hour() != 11 || last.Minute() != 35 || last.Day() != 1 {
		t.Errorf("across DST: %v, want Sun Nov 1 11:35 local", last)
	}
}

func TestDueFiresOnceWithinCatchUp(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	c := Config{Jobs: map[string]Job{"lineup": {Target: "hermes", PromptFile: "p", At: []string{"sun 11:35", "sun 15:00"}}}}
	st := State{}

	early := time.Date(2026, 9, 27, 11, 34, 0, 0, loc)
	if d := c.Due(early, st); len(d) != 0 {
		t.Errorf("a minute early nothing is due: %v", d)
	}
	on := time.Date(2026, 9, 27, 11, 36, 0, 0, loc)
	d := c.Due(on, st)
	if len(d) != 1 || d[0].Entry != "sun 11:35" {
		t.Fatalf("due = %v, want sun 11:35", d)
	}
	st.Mark(d[0].Job, d[0].Entry, d[0].At)
	if again := c.Due(on.Add(time.Minute), st); len(again) != 0 {
		t.Errorf("a fired entry must not fire twice: %v", again)
	}
	late := time.Date(2026, 9, 27, 12, 0, 0, 0, loc)
	if d := c.Due(late, State{}); len(d) != 0 {
		t.Errorf("25 min late is past the 20 min catch-up: %v", d)
	}
	next := time.Date(2026, 10, 4, 11, 40, 0, 0, loc)
	if d := c.Due(next, st); len(d) != 1 {
		t.Errorf("next week the entry fires again: %v", d)
	}
}

func TestInstructionFlattensThePromptUnderTheMode(t *testing.T) {
	t.Parallel()
	prompt := "Line one.\n\n  - item two\n"
	for mode, want := range map[string]string{
		"":      "/goal Line one. - item two",
		"goal":  "/goal Line one. - item two",
		"loop":  "/loop Line one. - item two",
		"plain": "Line one. - item two",
	} {
		if got := (Job{Mode: mode}).Instruction(prompt); got != want {
			t.Errorf("mode %q: %q, want %q", mode, got, want)
		}
	}
}

func TestRunArgvIsUnattended(t *testing.T) {
	t.Parallel()
	j := Job{Target: "hermes", Model: "qwen3.8:27b-mlx", Addons: []string{"host-chrome"}, Args: []string{"--egress-mode", "open"}}
	got := j.RunArgv("/bin/proveo")
	want := []string{"/bin/proveo", "run", "hermes", "--yes", "--local-model", "qwen3.8:27b-mlx", "--addon", "host-chrome", "--egress-mode", "open"}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %v\nwant   %v", got, want)
	}
}

func TestLoadValidatesJobs(t *testing.T) {
	home := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(Path(home), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    at: [\"sun 11:35\"]\n")
	if _, err := Load(home); err != nil {
		t.Errorf("valid file: %v", err)
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    at: [\"someday\"]\n")
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "someday") {
		t.Errorf("bad at must name it: %v", err)
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    mode: forever\n    at: []\n")
	if _, err := Load(home); err == nil {
		t.Error("unknown mode must be refused")
	}
}

func TestLaunchdPlistRunsTheTickEveryMinute(t *testing.T) {
	t.Parallel()
	p := LaunchdPlist("/Users/a&b/proveo", "/usr/bin:/bin", "/tmp/tick.log")
	for _, want := range []string{"<string>" + LaunchdLabel + "</string>", "<string>/Users/a&amp;b/proveo</string>",
		"<string>schedule</string><string>tick</string>", "<key>StartInterval</key><integer>60</integer>", "/usr/bin:/bin"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

// fakeTmux scripts a pane: it shows the ready marker, records what is typed, and ends when told.
type fakeTmux struct {
	mu      sync.Mutex
	pane    string
	typed   []string
	alive   bool
	endOn   string
	killed  bool
	swallow int               // typed lines dropped before one shows on the pane
	echo    bool              // a kept line shows as "RECEIVED: <line>"
	writes  map[string]string // files the agent writes when it ends on endOn
	keys    []string          // non-literal keys sent, e.g. C-c
	pid     string            // what #{pane_pid} reports
	gone    func() bool       // the pane's process ended on its own
}

func (f *fakeTmux) run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch args[0] {
	case "has-session":
		if f.gone != nil && f.gone() {
			f.alive = false
		}
		if f.alive {
			return "", nil
		}
		return "", os.ErrNotExist
	case "capture-pane":
		return f.pane, nil
	case "display-message":
		return f.pid, nil
	case "send-keys":
		if args[3] != "-l" {
			f.keys = append(f.keys, args[3:]...)
		}
		if args[3] == "-l" {
			f.typed = append(f.typed, args[4])
			if f.swallow > 0 {
				f.swallow--
			} else if f.echo {
				f.pane += "\nRECEIVED: " + args[4]
			}
			if f.endOn != "" && strings.HasPrefix(args[4], f.endOn) {
				f.alive = false
				for path, body := range f.writes {
					_ = os.WriteFile(path, []byte(body), 0o600)
				}
			}
		}
		return "", nil
	case "kill-session":
		f.killed, f.alive = true, false
	}
	return "", nil
}

// fastWatch shrinks Watch's polling so a test runs in milliseconds.
func fastWatch(t *testing.T) {
	t.Helper()
	p, q, m, a, b, e := pollEvery, settleQuiet, settleMax, acceptWait, budgetPoll, exitPoll
	i, g, tg := interruptWait, exitGrace, termGrace
	pollEvery, settleQuiet, settleMax, acceptWait = time.Millisecond, 5*time.Millisecond, time.Second, 50*time.Millisecond
	budgetPoll, exitPoll, interruptWait, exitGrace, termGrace = time.Millisecond, time.Millisecond, time.Millisecond, 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() {
		pollEvery, settleQuiet, settleMax, acceptWait, budgetPoll, exitPoll = p, q, m, a, b, e
		interruptWait, exitGrace, termGrace = i, g, tg
	})
}

func TestWatchTypesTheGoalOnceTheAgentIsReady(t *testing.T) {
	fastWatch(t)
	home := t.TempDir()
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("Set the best lineup.\nBench anyone Out."), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{pane: "hermes ready\n❯ ", alive: true, endOn: "/goal"}
	j := Job{Target: "hermes", PromptFile: prompt, Budget: "1m"}
	var note string
	res := Watch(home, "lineup", "sun 11:35", "t.log", j, f.run, func(_, body string) { note = body })

	if len(f.typed) == 0 || f.typed[0] != "/goal Set the best lineup. Bench anyone Out." {
		t.Fatalf("typed = %q", f.typed)
	}
	if res.Outcome != "ended" || !strings.HasPrefix(note, "ended") {
		t.Errorf("outcome = %q (%s), note %q", res.Outcome, res.Detail, note)
	}
	if last := LastResult(home, "lineup"); last == nil || last.Entry != "sun 11:35" {
		t.Errorf("the result must be recorded: %+v", last)
	}
}

func TestCommandReplacesTheHarnessRun(t *testing.T) {
	t.Parallel()
	j := Job{Command: []string{"sh", "-c", "read x"}, Model: "ignored"}
	if got := j.RunArgv("/bin/proveo"); !slices.Equal(got, []string{"sh", "-c", "read x"}) {
		t.Errorf("argv = %v", got)
	}
	if err := (Job{Command: []string{"true"}, PromptFile: "p"}).validate(); err != nil {
		t.Errorf("a command job needs no target: %v", err)
	}
}

func TestWatchFailsARunThatExitsNonZero(t *testing.T) {
	fastWatch(t)
	home := t.TempDir()
	prompt := filepath.Join(home, "p.md")
	transcript := filepath.Join(home, "20260927-113500.log")
	if err := os.WriteFile(prompt, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExitFile(transcript), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{pane: "❯ ", alive: true, endOn: "/goal"}
	res := Watch(home, "lineup", "sun 11:35", transcript, Job{Target: "hermes", PromptFile: prompt, Budget: "1m"}, f.run, nil)
	if res.Outcome != "failed" || !strings.Contains(res.Detail, "exited 3") || !res.Failed() {
		t.Errorf("outcome = %q (%s), want failed with the exit status", res.Outcome, res.Detail)
	}
}

func TestWatchRetypesAnInstructionTheAgentSwallowed(t *testing.T) {
	fastWatch(t)
	home := t.TempDir()
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("Set the best lineup."), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{pane: "❯ ", alive: true, swallow: 1, echo: true, endOn: "/exit"}
	j := Job{Command: []string{"agent"}, PromptFile: prompt, Budget: "1ms"}
	res := Watch(home, "lineup", "sun 11:35", filepath.Join(home, "t.log"), j, f.run, nil)
	if n := strings.Count(strings.Join(f.typed, "\n"), "/goal Set the best lineup."); n != 2 {
		t.Errorf("typed the goal %d times, want 2 (the first was swallowed): %q", n, f.typed)
	}
	if res.Outcome != "budget" {
		t.Errorf("outcome = %q (%s), want budget once the retype took", res.Outcome, res.Detail)
	}

	f = &fakeTmux{pane: "❯ ", alive: true, swallow: typeAttempts, echo: true}
	res = Watch(home, "lineup", "sun 11:35", filepath.Join(home, "t.log"), j, f.run, nil)
	if res.Outcome != "failed" || !strings.Contains(res.Detail, "never showed") || !f.killed {
		t.Errorf("outcome = %q (%s), killed %v; want failed after %d swallowed tries", res.Outcome, res.Detail, f.killed, typeAttempts)
	}
}

func TestWatchFailsFastWhenTheAgentExitsBeforeReady(t *testing.T) {
	fastWatch(t)
	home := t.TempDir()
	transcript := filepath.Join(home, "20260928-155014.log")
	if err := os.WriteFile(transcript, []byte("\x1b[31m× local model m: needs ~21.0 GiB\x1b[0m\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExitFile(transcript), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{pane: "starting", alive: false}
	start := time.Now()
	res := Watch(home, "lineup", "mon 18:50", transcript, Job{Target: "hermes", PromptFile: prompt}, f.run, nil)
	if time.Since(start) > 5*time.Second {
		t.Errorf("Watch took %s; an ended session must not wait out readyWait", time.Since(start))
	}
	want := "the agent exited 1 before it was ready: × local model m: needs ~21.0 GiB"
	if res.Outcome != "failed" || res.Detail != want {
		t.Errorf("result = %q %q\nwant   failed %q", res.Outcome, res.Detail, want)
	}
}

func TestAcceptedMarkerPrefersJobThenHarness(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		j    Job
		want string
	}{
		{Job{Target: "hermes", Accepted: "OK>"}, "OK>"},
		{Job{Target: "hermes"}, "Goal (active"},
		{Job{Target: "hermes", Mode: "plain"}, "You manage the Yahoo Fan"},
		{Job{Target: "claude"}, "You manage the Yahoo Fan"},
	} {
		if got := c.j.acceptedMarker("You manage the Yahoo\nFantasy team."); got != c.want {
			t.Errorf("%+v: accepted = %q, want %q", c.j, got, c.want)
		}
	}
}

func TestLaunchFailureLeavesAFailedResult(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	RecordLaunchFailure(home, "lineup", "sun 11:35", errors.New("start tmux session: no server"))
	last := LastResult(home, "lineup")
	if last == nil || !last.Failed() || last.Entry != "sun 11:35" || !strings.Contains(last.Detail, "no server") {
		t.Errorf("last = %+v, want a failed result naming the launch error", last)
	}
	for outcome, failed := range map[string]bool{"failed": true, "not-ready": true, "ended": false, "budget": false} {
		if (Result{Outcome: outcome}).Failed() != failed {
			t.Errorf("Result{%s}.Failed() != %v", outcome, failed)
		}
	}
}

func TestWatchKeepsTheAgentsReport(t *testing.T) {
	fastWatch(t)
	home, work := t.TempDir(), t.TempDir()
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("Set the lineup."), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(work, ReportFile)
	if err := os.WriteFile(report, []byte("RESULT: stale from last week"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(home, "20260928-161052.log")
	j := Job{Command: []string{"agent"}, Workdir: work, PromptFile: prompt}
	f := &fakeTmux{pane: "❯ ", alive: true, endOn: "/goal", writes: map[string]string{
		report: "# Lineup report\n\nRESULT: no-op — every Week 3 slot is locked\n\nBefore: …\n",
	}}
	res := Watch(home, "lineup", "mon 18:50", transcript, j, f.run, nil)
	if res.Summary != "no-op — every Week 3 slot is locked" {
		t.Errorf("summary = %q", res.Summary)
	}
	if res.Report != filepath.Join(home, "20260928-161052.report.md") {
		t.Errorf("report path = %q", res.Report)
	}
	if b, err := os.ReadFile(res.Report); err != nil || !strings.Contains(string(b), "Before: …") {
		t.Errorf("the report must move next to the transcript: %v %q", err, b)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Errorf("report.md must leave the workdir so the next run cannot reuse it: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(work, "lineup.md")); err != nil || !strings.HasPrefix(string(b), "# Lineup report") {
		t.Errorf("<task>.md must keep the newest report in the workdir for the next run: %v %q", err, b)
	}

	if err := os.WriteFile(report, []byte("RESULT: stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	f = &fakeTmux{pane: "❯ ", alive: true, endOn: "/goal"}
	if res := Watch(home, "lineup", "mon 18:50", transcript, j, f.run, nil); res.Summary != "" || res.Report != "" {
		t.Errorf("a stale report from an earlier run leaked in: %+v", res)
	}
}

func TestReportSummaryPrefersTheResultLine(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"# Report\n**RESULT:** changed 2 — Etienne → Warren\n": "changed 2 — Etienne → Warren",
		"RESULT: blocked — Chrome is logged out":               "blocked — Chrome is logged out",
		"\n## Lineup before and after\nQB Burrow\n":            "Lineup before and after",
		"": "",
	} {
		if got := reportSummary(in); got != want {
			t.Errorf("reportSummary(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBudgetStopInterruptsThenExits(t *testing.T) {
	fastWatch(t)
	f := &fakeTmux{alive: true, endOn: "/exit"}
	got := stopAgent(tmux.New("s", f.run), "stopped at 1m")
	if got != "stopped at 1m with /exit" {
		t.Errorf("stop = %q", got)
	}
	if len(f.keys) == 0 || f.keys[0] != "C-c" || len(f.typed) != 1 || f.typed[0] != "/exit" {
		t.Errorf("keys %q, typed %q: want C-c to interrupt the turn before /exit", f.keys, f.typed)
	}
	if f.killed {
		t.Error("a run that took /exit must not be killed")
	}
}

func TestBudgetStopSignalsTheRunBeforeKilling(t *testing.T) {
	fastWatch(t)
	pane := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := pane.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = pane.Wait(); close(done) }()
	t.Cleanup(func() { _ = pane.Process.Kill() })
	time.Sleep(200 * time.Millisecond) // sh has forked sleep
	ended := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	f := &fakeTmux{alive: true, pid: strconv.Itoa(pane.Process.Pid), gone: ended}
	got := stopAgent(tmux.New("s", f.run), "stopped at 1m")
	if !strings.Contains(got, "SIGTERM did") || f.killed {
		t.Errorf("stop = %q, killed %v; want SIGTERM to end the pane's command without a kill", got, f.killed)
	}

	f = &fakeTmux{alive: true}
	if got := stopAgent(tmux.New("s", f.run), "stopped at 1m"); !strings.Contains(got, "session killed") || !f.killed {
		t.Errorf("stop = %q, killed %v; want the kill as the last resort", got, f.killed)
	}
}

func TestWatchStopsEarlyOnceTheReportIsWritten(t *testing.T) {
	fastWatch(t)
	dq, dm := doneQuiet, doneMax
	doneQuiet, doneMax = 5*time.Millisecond, time.Second
	t.Cleanup(func() { doneQuiet, doneMax = dq, dm })
	home, work := t.TempDir(), t.TempDir()
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("Set the lineup."), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(work, ReportFile)
	f := &fakeTmux{pane: "❯ ", alive: true, echo: true, endOn: "/exit"}
	j := Job{Command: []string{"agent"}, Workdir: work, PromptFile: prompt, Budget: "1h"}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(report, []byte("RESULT: changed 1 — Etienne → Warren\n"), 0o600)
	}()
	start := time.Now()
	res := Watch(home, "lineup", "sun 11:35", filepath.Join(home, "20260928-161052.log"), j, f.run, nil)
	if res.Outcome != "done" || res.Summary != "changed 1 — Etienne → Warren" {
		t.Errorf("result = %q %q (%s), want done with the report's RESULT", res.Outcome, res.Summary, res.Detail)
	}
	if !strings.Contains(res.Detail, "stopped early with /exit") || time.Since(start) > 10*time.Second {
		t.Errorf("detail %q after %s: a written report must end the run long before its 1h budget", res.Detail, time.Since(start))
	}
}
