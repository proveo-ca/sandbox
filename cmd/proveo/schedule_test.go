// SPEC: _spec/internal/schedule/schedule.puml
package main

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestOfferRetryAsksByTheJobAloneAndRelaunchesEachYes(t *testing.T) {
	t.Parallel()
	failed := []schedule.Row{
		{Job: "muse-lineup", Failed: &schedule.Result{Entry: "sun 11:35", Outcome: "failed"}},
		{Job: "muse-waivers", Failed: &schedule.Result{Entry: "tue 20:00", Outcome: "not-ready"}},
		{Job: "muse-trades", Failed: &schedule.Result{Entry: "wed 20:00", Outcome: "failed"}},
	}
	var retried []string
	var out strings.Builder
	in := bufio.NewReader(strings.NewReader("y\n\nyes\n"))
	for _, r := range failed {
		err := offerRetry(r, in, &out, func(job string) error {
			retried = append(retried, job)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"muse-lineup", "muse-trades"}; !slices.Equal(retried, want) {
		t.Errorf("retried %v, want %v (Enter means no)", retried, want)
	}
	if !strings.Contains(out.String(), "retry muse-waivers? [y/N]") || strings.Contains(out.String(), "tue 20:00") {
		t.Errorf("the question names the job; the × above it names the run:\n%s", out.String())
	}
}

func TestRetryJobRefusesARunThatDidNotFail(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	c := schedule.Config{Jobs: map[string]schedule.Job{"lineup": {Target: "hermes", PromptFile: "p", At: []string{"sun 11:35"}}}}
	var notRun errNotRun
	if err := retryJob(home, c, "lineup"); !errors.As(err, &notRun) {
		t.Errorf("never-run job: %v", err)
	}
	dir := schedule.LogDir(home, "lineup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260927-113500.json"), []byte(`{"outcome":"budget"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var notFailed errNotFailed
	if err := retryJob(home, c, "lineup"); !errors.As(err, &notFailed) || notFailed.outcome != "budget" {
		t.Errorf("budget run: %v", err)
	}
	var noJob errNoJob
	if err := retryJob(home, c, "nope"); !errors.As(err, &noJob) || !slices.Equal(noJob.jobs, []string{"lineup"}) {
		t.Errorf("unknown job: %v", err)
	}
}

// scheduleHome is an isolated host whose proveo home holds body as schedule.yml, its tick reported by
// tick; it returns that home.
func scheduleHome(t *testing.T, body string, tick schedule.Tick) string {
	t.Helper()
	isolatedHost(t)
	home := os.Getenv("PROVEO_HOME")
	if body != "" {
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schedule.Path(home), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := serviceManager
	t.Cleanup(func() { serviceManager = prev })
	serviceManager = func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "launchctl" && slices.Contains(args, "print"):
			if !tick.Installed {
				return nil, errors.New("exit status 113")
			}
			return []byte("\tstdout path = /l/tick.log\n\tlast exit code = " + cmp.Or(tick.Exit, "0") + "\n"), nil
		case name == "systemctl" && slices.Contains(args, "is-active"):
			if !tick.Installed {
				return []byte("inactive\n"), errors.New("exit status 3")
			}
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	return home
}

// proveoOut runs proveo with args and returns its stdout, its stderr and its error.
func proveoOut(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut strings.Builder
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&errOut)
	root := rootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

const twoJobs = "jobs:\n  lineup:\n    target: hermes\n    prompt_file: /p.md\n    at: [\"mon 18:50\"]\n" +
	"  probe:\n    command: [\"true\"]\n    prompt_file: /p.md\n"

func TestARefusalPrintsOneFixAndIsReported(t *testing.T) {
	scheduleHome(t, twoJobs, schedule.Tick{})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"schedule", "retry", "probe"}, "error: probe — has not run yet\n    `proveo schedule run probe` starts it now\n"},
		{[]string{"schedule", "retry", "nosuch"}, "error: nosuch — no such job\n    jobs: lineup, probe\n"},
		{[]string{"schedule", "run", "nosuch"}, "error: nosuch — no such job\n    jobs: lineup, probe\n"},
	} {
		_, stderr, err := proveoOut(t, c.args...)
		var reported backend.Reported
		if !errors.As(err, &reported) || stderr != c.want {
			t.Errorf("proveo %s = %v\n%s\nwant one × and one dim fix, Reported:\n%s", strings.Join(c.args, " "), err, stderr, c.want)
		}
		if code, ok := exitCode(err); !ok || code != 1 {
			t.Errorf("proveo %s exits %d, %v", strings.Join(c.args, " "), code, ok)
		}
	}
}

func TestARefusalNamesItsJobAndWritesHomePathsWithATilde(t *testing.T) {
	isolatedHost(t)
	home := os.Getenv("HOME")
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w (tmux session proveo-sched-waivers)", schedule.ErrRunning),
			"error: waivers — already running\n    `proveo attach waivers` opens it\n"},
		{fmt.Errorf("%w: %w", schedule.ErrSessionStart, errors.New("exit status 1: no server running")),
			"error: waivers — its tmux session did not start\n    exit status 1: no server running\n"},
		{fmt.Errorf("mkdir %s: permission denied", filepath.Join(home, ".proveo", "logs")),
			"error: waivers — not started\n    mkdir ~/.proveo/logs: permission denied\n"},
	} {
		out := captureUI(t)
		if err := refuse("waivers", c.err); !errors.As(err, new(backend.Reported)) || out.String() != c.want {
			t.Errorf("refuse(%v) = %v\n%s\nwant\n%s", c.err, err, out, c.want)
		}
	}
}

func TestAStrayScheduleArgumentStillListsAndExitsZero(t *testing.T) {
	scheduleHome(t, twoJobs, schedule.Tick{})
	for _, args := range [][]string{{"schedule", "retyr", "lineup"}, {"schedule", "ls", "extra"}} {
		if stdout, _, err := proveoOut(t, args...); err != nil || !strings.Contains(stdout, "lineup:") {
			t.Errorf("proveo %s = %v, stdout %q; want the listing and exit 0", strings.Join(args, " "), err, stdout)
		}
	}
	for _, args := range [][]string{{"schedule", "tick", "extra"}, {"schedule", "uninstall", "extra"}} {
		if _, _, err := proveoOut(t, args...); err != nil {
			t.Errorf("proveo %s = %v; want the argument ignored", strings.Join(args, " "), err)
		}
	}
}

func TestAttachHelpNamesTheProveoHomeNotATildePath(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{attachCmd().Long, scheduleAttachCmd().Long} {
		if strings.Contains(cmd, "~/.proveo/schedule.yml") || !strings.Contains(cmd, "$PROVEO_HOME") {
			t.Errorf("attach's help must name schedule.yml in the proveo home, which PROVEO_HOME moves:\n%s", cmd)
		}
	}
}

func TestTheScheduleListingWithNoFileSaysSoAndExitsZero(t *testing.T) {
	scheduleHome(t, "", schedule.Tick{})
	stdout, stderr, err := proveoOut(t, "schedule")
	if err != nil || stdout != "" || stderr != "no ~/.proveo/schedule.yml\ntick — not installed\n" {
		t.Errorf("proveo schedule with no schedule.yml = %v\nstdout %q\nstderr %q", err, stdout, stderr)
	}
	if stdout, stderr, err := proveoOut(t, "schedule", "tick"); err != nil || stdout+stderr != "" {
		t.Errorf("the tick with no schedule.yml = %v, %q; want nothing due, nothing logged", err, stdout+stderr)
	}
	if _, stderr, err := proveoOut(t, "schedule", "run", "lineup"); !errors.As(err, new(backend.Reported)) ||
		stderr != "error: ~/.proveo/schedule.yml — not found\n    `proveo schedule --help` shows its format\n" {
		t.Errorf("schedule run with no schedule.yml = %v\n%s", err, stderr)
	}
}

func TestAnEmptyScheduleSaysNoJobsOnStderr(t *testing.T) {
	scheduleHome(t, "jobs: {}\n", schedule.Tick{})
	stdout, stderr, err := proveoOut(t, "schedule", "ls")
	if err != nil || stdout != "" || stderr != "no jobs in ~/.proveo/schedule.yml\ntick — not installed\n" {
		t.Errorf("an empty schedule = %v\nstdout %q\nstderr %q", err, stdout, stderr)
	}
}

func TestAnInvalidScheduleIsOneCrossWithItsReasonDim(t *testing.T) {
	scheduleHome(t, "jobs:\n  x:\n    target: hermes\n", schedule.Tick{})
	_, stderr, err := proveoOut(t, "schedule")
	if !errors.As(err, new(backend.Reported)) || stderr != "error: ~/.proveo/schedule.yml — not valid\n    job \"x\": prompt_file is required\n" {
		t.Errorf("an invalid schedule = %v\n%s", err, stderr)
	}
}

func TestTheListingWarnsWhenTheTickReadsAnotherHome(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("proveo registers no tick on " + runtime.GOOS)
	}
	scheduleHome(t, twoJobs, schedule.Tick{Installed: true})
	t.Setenv("PROVEO_HOME", filepath.Join(t.TempDir(), "elsewhere"))
	if err := os.MkdirAll(os.Getenv("PROVEO_HOME"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schedule.Path(os.Getenv("PROVEO_HOME")), []byte(twoJobs), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := proveoOut(t, "schedule")
	if err != nil || !strings.HasSuffix(stdout, "warn: tick — reads ~/.proveo/schedule.yml, not this home's\n") || strings.Contains(stdout, "ok:") {
		t.Errorf("a tick that serves another home must not read ✓:\n%s", stdout)
	}
	t.Setenv("PROVEO_HOME", filepath.Join(os.Getenv("HOME"), ".proveo"))
	if stdout, _, _ := proveoOut(t, "schedule"); !strings.Contains(stdout, "ok: tick — installed\n") {
		t.Errorf("the tick that serves this home reads ✓:\n%s", stdout)
	}
}

func TestUnderAnotherHomeAMissingTickNamesNoInstallThatWouldNotServeIt(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("proveo registers no tick on " + runtime.GOOS)
	}
	scheduleHome(t, "", schedule.Tick{})
	elsewhere := t.TempDir()
	if err := os.WriteFile(schedule.Path(elsewhere), []byte(twoJobs), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_HOME", elsewhere)
	stdout, _, err := proveoOut(t, "schedule")
	if err != nil || !strings.HasSuffix(stdout, "warn: tick — not installed\n") || strings.Contains(stdout, "proveo schedule install") {
		t.Errorf("a how-to whose tick reads ~/.proveo fixes nothing for this home's jobs:\n%s", stdout)
	}
}

func TestInstallAndUninstallSayWhatTheTickIsNow(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("proveo registers no tick on " + runtime.GOOS)
	}
	scheduleHome(t, twoJobs, schedule.Tick{})
	_, stderr, err := proveoOut(t, "schedule", "install")
	manager := "launchd io.proveo.schedule, every 60s\n    logs to ~/.proveo/logs/schedule/tick.log\n"
	if runtime.GOOS == "linux" {
		manager = "systemd --user proveo-schedule.timer, every minute\n"
	}
	if err != nil || stderr != "ok: tick — installed\n    "+manager {
		t.Errorf("schedule install = %v\n%s", err, stderr)
	}
	if _, stderr, err := proveoOut(t, "schedule", "uninstall"); err != nil || stderr != "ok: tick — removed\n" {
		t.Errorf("schedule uninstall = %v\n%s", err, stderr)
	}
	prev := serviceManager
	serviceManager = func(string, ...string) ([]byte, error) { return nil, errors.New("exit status 3") }
	if _, stderr, err := proveoOut(t, "schedule", "uninstall"); err != nil || stderr != "tick — not installed\n" {
		t.Errorf("schedule uninstall with nothing there = %v\n%s", err, stderr)
	}
	serviceManager = prev
	elsewhere := t.TempDir()
	if err := os.WriteFile(schedule.Path(elsewhere), []byte(twoJobs), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_HOME", elsewhere)
	if _, stderr, err := proveoOut(t, "schedule", "install"); err != nil || !strings.HasPrefix(stderr, "warn: tick — reads ~/.proveo/schedule.yml, not this home's\n") {
		t.Errorf("schedule install under another PROVEO_HOME must not read ✓ = %v\n%s", err, stderr)
	}
}

func TestAttachArgsPerTerminal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		readOnly, inTmux bool
		want             string
		nested           bool
	}{
		{false, false, "attach -t s", false},
		{true, false, "attach -r -t s", false},
		{false, true, "switch-client -t s", false},
		{true, true, "attach -r -t s", true},
	} {
		args, nested := attachArgs("s", c.readOnly, c.inTmux)
		if got := strings.Join(args, " "); got != c.want || nested != c.nested {
			t.Errorf("readOnly=%v inTmux=%v: %q nested=%v, want %q nested=%v", c.readOnly, c.inTmux, got, nested, c.want, c.nested)
		}
	}
}

func TestScheduleHelpExampleLoads(t *testing.T) {
	t.Parallel()
	long := scheduleCmd().Long
	start := strings.Index(long, "  jobs:")
	end := strings.Index(long[start:], "\n\n")
	if start < 0 || end < 0 {
		t.Fatalf("no schedule.yml example in:\n%s", long)
	}
	var yml strings.Builder
	for _, l := range strings.Split(long[start:start+end], "\n") {
		yml.WriteString(strings.TrimPrefix(l, "  ") + "\n")
	}
	home := t.TempDir()
	if err := os.WriteFile(schedule.Path(home), []byte(yml.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := schedule.Load(home)
	if err != nil {
		t.Fatalf("the help's example must be a valid schedule.yml: %v\n%s", err, yml.String())
	}
	if j := c.Jobs["sample-task"]; len(j.At) != 2 || j.Target != "hermes" {
		t.Errorf("parsed %+v", j)
	}
}
