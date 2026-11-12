// SPEC: _spec/internal/schedule/schedule.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/ui"
)

// EnvScheduleNow pins the scheduler's clock (RFC 3339); tests use it, operators never need to.
const EnvScheduleNow = "PROVEO_SCHEDULE_NOW"

func scheduleNow() time.Time {
	if v := strings.TrimSpace(os.Getenv(EnvScheduleNow)); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Now()
}

func proveoExe() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "proveo"
}

func scheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Run harnesses unattended at set times; see, retry and attach to runs",
		Long: `Run harnesses unattended at set times. A minutely tick (launchd on macOS,
systemd --user on Linux) starts each due job in a tmux session, types its
prompt once the agent is ready, and ends it at the job's budget.

Jobs live in schedule.yml in the proveo home (~/.proveo, or $PROVEO_HOME);
a job with no at: runs only when ` + "`proveo schedule run`" + ` starts it:

  jobs:
    sample-task:                      # the task name: attach, run and retry take it
      target: hermes                  # or command: [argv] instead of proveo run
      model: muse-glimmer:30b-mlx     # optional local model
      addons: [host-chrome]           # optional
      prompt_file: /abs/path/sample-task.md
      mode: goal                      # goal (default) | loop | plain
      budget: 45m                     # default 45m
      tz: America/New_York            # default America/New_York
      at: ["mon 18:50", "sun 11:35"]  # weekday + 24h clock, in tz

With no subcommand it lists every job, soonest run first, and closes on
whether the tick is installed. A job whose last outcome is failed or
not-ready shows × with its reason; on a terminal it asks to retry, and
` + "`proveo schedule retry <job>`" + ` starts it again. The tick reads
~/.proveo/schedule.yml whatever PROVEO_HOME says.
` + "`proveo attach`" + ` lists the tasks whose tmux session is up.
` + "`proveo attach sample-task`" + ` (or ` + "`proveo schedule attach sample-task`" + `) opens one
by its task name; add -r to watch without typing into it.

Transcripts and results: <proveo home>/logs/schedule/<job>/.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return scheduleTable(cmd) },
	}
	cmd.AddCommand(scheduleLsCmd(), scheduleTickCmd(), scheduleRunCmd(), scheduleRetryCmd(), scheduleWatchCmd(),
		scheduleAttachCmd(), scheduleInstallCmd(), scheduleUninstallCmd())
	return cmd
}

// errNoSchedule is a proveo home with no schedule.yml.
type errNoSchedule struct{ path string }

func (e errNoSchedule) Error() string { return "no " + ui.Tilde(e.path) }

// errBadSchedule is a schedule.yml proveo cannot use.
type errBadSchedule struct {
	path string
	err  error
}

func (e errBadSchedule) Error() string { return e.err.Error() }

func (e errBadSchedule) Unwrap() error { return e.err }

// errNoJob is a job schedule.yml does not hold, with the jobs it does.
type errNoJob struct {
	name string
	jobs []string
}

func (e errNoJob) Error() string {
	return fmt.Sprintf("no job %q (jobs: %s)", e.name, strings.Join(e.jobs, ", "))
}

// errNotRun is a retry of a job that never ran.
type errNotRun struct{ name string }

func (e errNotRun) Error() string { return e.name + " has not run yet" }

// errNotFailed is a retry of a job whose last run did not fail.
type errNotFailed struct{ name, outcome string }

func (e errNotFailed) Error() string {
	return e.name + ": the last run ended " + e.outcome + ", not failed"
}

func loadSchedule() (string, schedule.Config, error) {
	home := proveohome.Root(os.Getenv)
	c, err := schedule.Load(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return home, c, errNoSchedule{schedule.Path(home)}
	case err != nil:
		return home, c, errBadSchedule{schedule.Path(home), err}
	}
	return home, c, nil
}

func jobNamed(c schedule.Config, name string) (schedule.Job, error) {
	j, ok := c.Jobs[name]
	if !ok {
		names := make([]string, 0, len(c.Jobs))
		for n := range c.Jobs {
			names = append(names, n)
		}
		sort.Strings(names)
		return j, errNoJob{name, names}
	}
	return j, nil
}

// refusal is the × item a schedule error stands for, with its one dim line; ok is false for an
// error it does not know, whose head the caller words.
func refusal(job string, err error) (it sandbox.Item, ok bool) {
	var (
		noFile    errNoSchedule
		bad       errBadSchedule
		noJob     errNoJob
		notRun    errNotRun
		notFailed errNotFailed
	)
	it.Fail = true
	switch {
	case errors.As(err, &noFile):
		it.Text, it.Detail = ui.Tilde(noFile.path)+" — not found", []string{"`proveo schedule --help` shows its format"}
	case errors.As(err, &bad):
		it.Text = ui.Tilde(bad.path) + " — not valid"
		it.Detail = errLines(errors.New(strings.TrimPrefix(bad.err.Error(), bad.path+": ")))
	case errors.As(err, &noJob):
		it.Text, it.Detail = noJob.name+" — no such job", []string{"jobs: " + strings.Join(noJob.jobs, ", ")}
	case errors.As(err, &notRun):
		it.Text, it.Detail = notRun.name+" — has not run yet", []string{"`proveo schedule run " + notRun.name + "` starts it now"}
	case errors.As(err, &notFailed):
		it.Text = notFailed.name + " — its last run did not fail"
		it.Detail = []string{"`proveo schedule run " + notFailed.name + "` starts it again"}
	case errors.Is(err, schedule.ErrRunning):
		it.Text, it.Detail = job+" — already running", []string{"`proveo attach " + job + "` opens it"}
	case errors.Is(err, schedule.ErrSessionStart):
		it.Text = job + " — its tmux session did not start"
		it.Detail = errLines(errors.New(strings.TrimPrefix(err.Error(), schedule.ErrSessionStart.Error()+": ")))
	default:
		return it, false
	}
	return it, true
}

// refuse prints err as job's × item and returns it as one main prints nothing for; an error no
// refusal names reads "<job> — not started".
func refuse(job string, err error) error {
	if err == nil {
		return nil
	}
	it, ok := refusal(job, err)
	if !ok {
		it = sandbox.Item{Fail: true, Text: job + " — not started", Detail: errLines(err)}
	}
	return reported(it, err)
}

// serviceManager runs launchctl and systemctl for the tick. Tests replace it.
var serviceManager schedule.Runner = schedule.ExecRunner

// tickStatus is the tick as this host's service manager holds it.
func tickStatus() schedule.Tick { return schedule.TickStatus(serviceManager) }

// scheduleTable lists the jobs and closes on the tick; on a terminal each failed job asks to retry
// under its ×. With nothing to list, every line goes to stderr and stdout stays empty.
func scheduleTable(cmd *cobra.Command) error {
	home, c, err := loadSchedule()
	var none errNoSchedule
	if err != nil && !errors.As(err, &none) {
		return refuse("", err)
	}
	now := scheduleNow()
	rows := schedule.Rows(c, now, liveRun(home), func(job string) *schedule.Result { return schedule.LastResult(home, job) })
	p := ui.New(cmd.OutOrStdout())
	if len(rows) == 0 {
		p = ui.New(cmd.ErrOrStderr())
		if err != nil {
			p.Asyncf("%s", none.Error())
		} else {
			p.Asyncf("no jobs in %s", ui.Tilde(schedule.Path(home)))
		}
	}
	l := schedule.Listing{Now: now, Home: home, Tick: tickStatus()}
	var refused error
	if stdioInteractive(cmd) {
		in := bufio.NewReader(cmd.InOrStdin())
		l.Retry = func(r schedule.Row) {
			err := offerRetry(r, in, cmd.OutOrStdout(), func(job string) error { return retryJob(home, c, job) })
			if refused == nil {
				refused = err
			}
		}
	}
	schedule.Render(p, rows, l)
	return refused
}

// offerRetry asks to retry the failed job r and relaunches it on yes; a refused relaunch prints its ×
// and comes back Reported.
func offerRetry(r schedule.Row, in *bufio.Reader, out io.Writer, retry func(job string) error) error {
	if !promptYesNo(fmt.Sprintf("retry %s?", r.Job), false, in, out) {
		return nil
	}
	return refuse(r.Job, retry(r.Job))
}

// retryJob relaunches a job whose last run failed, under that run's entry.
func retryJob(home string, c schedule.Config, name string) error {
	j, err := jobNamed(c, name)
	if err != nil {
		return err
	}
	last := schedule.LastResult(home, name)
	switch {
	case last == nil:
		return errNotRun{name}
	case !last.Failed():
		return errNotFailed{name, last.Outcome}
	}
	if err := schedule.Launch(home, proveoExe(), name, last.Entry, j, nil); err != nil {
		return err
	}
	ui.Okf("%s — started again", name)
	return nil
}

func scheduleLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List every job, soonest run first: its runs, target, model, mode, budget and last result",
		Args:  noArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return scheduleTable(cmd) },
	}
}

func scheduleTickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
		Short: "Start every job that is due (what launchd/systemd runs each minute)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, c, err := loadSchedule()
			var none errNoSchedule
			if errors.As(err, &none) {
				return nil
			}
			if err != nil {
				return refuse("", err)
			}
			now := scheduleNow()
			st := schedule.LoadState(home)
			for _, d := range c.Due(now, st) {
				st.Mark(d.Job, d.Entry, d.At)
				if err := st.Save(home); err != nil {
					return err
				}
				if err := schedule.Launch(home, proveoExe(), d.Job, d.Entry, c.Jobs[d.Job], nil); err != nil {
					if !errors.Is(err, schedule.ErrRunning) {
						schedule.RecordLaunchFailure(home, d.Job, d.Entry, err)
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "%s tick: %s (%s): %v\n", now.Format(time.RFC3339), d.Job, d.Entry, err)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s tick: started %s for %q (due %s)\n", now.Format(time.RFC3339), d.Job, d.Entry,
					d.At.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func scheduleRunCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "Start a job now, outside its schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return refuse(args[0], err)
			}
			j, err := jobNamed(c, args[0])
			if err != nil {
				return refuse(args[0], err)
			}
			if printOnly {
				return schedule.Describe(cmd.OutOrStdout(), home, proveoExe(), args[0], j, scheduleNow(), printScheduledRun)
			}
			if err := schedule.Launch(home, proveoExe(), args[0], "manual", j, nil); err != nil {
				return refuse(args[0], err)
			}
			ui.Okf("%s — started", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the session command, the sandbox plan and the typed line instead of starting the job")
	return cmd
}

func printScheduledRun(argv []string, dir string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "PROVEO_SCHEDULE=1")
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func scheduleRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <task>",
		Short: "Start a job again when its last run failed (failed or not-ready)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return refuse(args[0], err)
			}
			return refuse(args[0], retryJob(home, c, args[0]))
		},
	}
}

func scheduleWatchCmd() *cobra.Command {
	var entry, transcript string
	var typed bool
	cmd := &cobra.Command{
		Use:    "watch <job>",
		Short:  "Drive one launched job (started by tick/run)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return refuse(args[0], err)
			}
			j, err := jobNamed(c, args[0])
			if err != nil {
				return refuse(args[0], err)
			}
			if typed {
				schedule.Rewatch(home, args[0], entry, transcript, j, nil, notify)
				return nil
			}
			schedule.Watch(home, args[0], entry, transcript, j, nil, notify)
			return nil
		},
	}
	cmd.Flags().StringVar(&entry, "entry", "", "the schedule entry that fired")
	cmd.Flags().StringVar(&transcript, "transcript", "", "the transcript file")
	cmd.Flags().BoolVar(&typed, "typed", false, "the instruction is already in; only wait for the report or the budget")
	return cmd
}

func attachCmd() *cobra.Command {
	return newAttachCmd("attach [task]", "List running tasks, or open one by name (alias for `proveo schedule attach`)")
}

func scheduleAttachCmd() *cobra.Command {
	return newAttachCmd("attach [task]", "Open a running task's terminal by its task name (the key under jobs: in schedule.yml)")
}

func newAttachCmd(use, short string) *cobra.Command {
	var readOnly bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: `Open a running task's terminal. The task name is its key under jobs: in
schedule.yml in the proveo home (~/.proveo, or $PROVEO_HOME); attach opens
the tmux session proveo-sched-<task>.

With no task, a terminal asks which running task to open. Without a
terminal it prints the running tasks and stops. A task that is not
running: ` + "`proveo schedule`" + ` shows its last run.

Without -r your keys reach the agent. Detach with the tmux prefix, then d
(Ctrl-b d); the run keeps going. Inside tmux, attach switches your client
to the task's session; with -r it nests a client (Ctrl-b Ctrl-b d). In
zellij, press Ctrl-b twice, then d.`,
		Example: `proveo attach
proveo attach sample-task
proveo schedule attach sample-task -r`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listOrAttach(cmd, readOnly)
			}
			return attachSession(args[0], readOnly)
		},
	}
	cmd.Flags().BoolVarP(&readOnly, "read-only", "r", false, "watch without sending keys to the agent")
	return cmd
}

// listOrAttach opens the running task a terminal picks; without a terminal it lists the running tasks
// and, under them, the one command that opens one. Closing the picker is a ⚠, Reported.
func listOrAttach(cmd *cobra.Command, readOnly bool) error {
	tasks, err := currentRunning()
	if err != nil {
		return refuse("", err)
	}
	if !stdioInteractive(cmd) || len(tasks) == 0 {
		writeRunning(ui.New(cmd.OutOrStdout()), ui.New(cmd.ErrOrStderr()), tasks)
		if len(tasks) > 0 {
			ui.New(cmd.ErrOrStderr()).Detailf("`proveo attach <task>` opens one")
		}
		return nil
	}
	return attachPicked(tasks, readOnly)
}

// attachPicked opens the running task the picker returns; a picker closed with no choice is a ⚠, Reported.
func attachPicked(tasks []schedule.RunningTask, readOnly bool) error {
	choices := make([]menuChoice, len(tasks))
	for i, t := range tasks {
		choices[i] = menuChoice{Verb: "attach", Name: t.Job, Detail: t.Summary, Attach: true}
	}
	got, err := offer(choices, "attach> ", "no task selected")
	var closed pickerClosed
	if errors.As(err, &closed) {
		return reported(sandbox.Item{Warn: true, Text: "attach — " + closed.abort}, err)
	}
	if err != nil {
		return err
	}
	return attachSession(got.Name, readOnly)
}

// stdinTerminal reports whether proveo's stdin is a terminal. Tests replace it.
var stdinTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// attachSession hands the terminal to tmux on job's session. A session that is not up, or no terminal
// for tmux, is one × before tmux runs; a hand-off that fails ends on "⚠ attach — stopped at tmux".
func attachSession(job string, readOnly bool) error {
	sess := schedule.SessionName(job)
	if !tmuxHasSession(sess) {
		return notAttachable(job, sess)
	}
	args, nested := attachArgs(sess, readOnly, os.Getenv("TMUX") != "")
	if args[0] == "attach" && !stdinTerminal() {
		return reported(sandbox.Item{Fail: true, Text: job + " — not attached", Detail: []string{"tmux needs a terminal"}},
			fmt.Errorf("%s: tmux needs a terminal", job))
	}
	c := exec.Command("tmux", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if nested {
		c.Env = append(os.Environ(), "TMUX=")
		sandbox.PrintBlock(func() {}, nil, []sandbox.Item{{Host: true, Text: "tmux — nested read-only client",
			Detail: []string{"the prefix twice, then d, detaches"}}})
	}
	if err := c.Run(); err != nil {
		return reported(sandbox.Item{Warn: true, Text: "attach — stopped at tmux"}, err)
	}
	return nil
}

// notAttachable is the × for a task whose session is not up, worded from schedule.yml.
// SPEC: _spec/internal/schedule/schedule.puml (ATTACH)
func notAttachable(job, sess string) error {
	_, c, err := loadSchedule()
	if err == nil {
		_, err = jobNamed(c, job)
	}
	if err != nil {
		return refuse(job, err)
	}
	return reported(sandbox.Item{Fail: true, Text: job + " — not running", Detail: []string{"no tmux session " + sess}},
		fmt.Errorf("%s is not running", job))
}

// attachArgs is the tmux argv for attach; nested means run it with TMUX unset.
func attachArgs(sess string, readOnly, inTmux bool) (args []string, nested bool) {
	switch {
	case readOnly:
		return []string{"attach", "-r", "-t", sess}, inTmux
	case inTmux:
		return []string{"switch-client", "-t", sess}, false
	}
	return []string{"attach", "-t", sess}, false
}

func scheduleInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register the minutely tick (launchd on macOS, systemd --user on Linux)",
		Long: `Register the minutely tick with launchd on macOS or systemd --user on Linux.
On macOS it writes ~/Library/LaunchAgents/` + schedule.LaunchdLabel + `.plist, and the tick
logs to <proveo home>/logs/schedule/tick.log. The tick reads ~/.proveo/schedule.yml
whatever PROVEO_HOME says.`,
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			home, _, err := loadSchedule()
			if err != nil {
				return refuse("", err)
			}
			tick, err := schedule.Install(home, proveoExe(), serviceManager)
			if err != nil {
				return reported(installRefusal(tick, err), err)
			}
			installed(home, tick)
			return nil
		},
	}
}

// installRefusal is the × item for a tick Install did not register.
func installRefusal(tick schedule.Tick, err error) sandbox.Item {
	if errors.Is(err, schedule.ErrNoServiceManager) {
		it := sandbox.Item{Fail: true, Text: "tick — no service manager on " + tick.NoManager}
		if tick.NoManager != "windows" {
			it.Detail = []string{"run `proveo schedule tick` every minute yourself"}
		}
		return it
	}
	return sandbox.Item{Fail: true, Text: "tick — not installed", Detail: errLines(err)}
}

// installed says the tick is registered, and that it reads another home's schedule.yml when it does.
func installed(home string, tick schedule.Tick) {
	detail := []string{tick.Manager}
	if tick.Log != "" {
		detail = append(detail, "logs to "+ui.Tilde(tick.Log))
	}
	it := sandbox.Item{Ok: true, Text: "tick — installed", Detail: detail}
	if tick.Home != "" && filepath.Clean(tick.Home) != filepath.Clean(home) {
		it = sandbox.Item{Warn: true, Text: "tick — reads " + ui.Tilde(schedule.Path(tick.Home)) + ", not this home's", Detail: detail}
	}
	sandbox.PrintBlock(func() {}, nil, []sandbox.Item{it})
}

func scheduleUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the minutely tick",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			removed, err := schedule.Uninstall(serviceManager)
			switch {
			case err != nil:
				return reported(sandbox.Item{Fail: true, Text: "tick — not removed", Detail: errLines(err)}, err)
			case removed:
				ui.Okf("tick — removed")
			default:
				ui.Rolef(ui.RoleAsync, "tick — not installed")
			}
			return nil
		},
	}
}

func notify(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = exec.Command("osascript", "-e", script).Run()
	case "linux":
		_ = exec.Command("notify-send", title, body).Run()
	}
}
