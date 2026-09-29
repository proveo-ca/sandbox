// SPEC: _spec/internal/schedule/schedule.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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

Jobs live in ~/.proveo/schedule.yml:

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

With no subcommand it lists every job, soonest run first. A run whose last
outcome is failed or not-ready shows ✗ with its reason; on a terminal it
asks to retry, and ` + "`proveo schedule retry <job>`" + ` starts it again.
` + "`proveo schedule attach sample-task`" + ` opens a running task's terminal by its task name;
add -r to watch without typing into it.

Transcripts and results: ~/.proveo/logs/schedule/<job>/.`,
		RunE: func(cmd *cobra.Command, _ []string) error { return scheduleTable(cmd) },
	}
	cmd.AddCommand(scheduleLsCmd(), scheduleTickCmd(), scheduleRunCmd(), scheduleRetryCmd(), scheduleWatchCmd(),
		scheduleAttachCmd(), scheduleInstallCmd(), scheduleUninstallCmd())
	return cmd
}

func loadSchedule() (string, schedule.Config, error) {
	home := proveohome.Root(os.Getenv)
	c, err := schedule.Load(home)
	if os.IsNotExist(err) {
		return home, c, fmt.Errorf("no %s — write one first (see `proveo schedule --help`)", schedule.Path(home))
	}
	return home, c, err
}

func jobNamed(c schedule.Config, name string) (schedule.Job, error) {
	j, ok := c.Jobs[name]
	if !ok {
		names := make([]string, 0, len(c.Jobs))
		for n := range c.Jobs {
			names = append(names, n)
		}
		sort.Strings(names)
		return j, fmt.Errorf("no job %q (jobs: %s)", name, strings.Join(names, ", "))
	}
	return j, nil
}

func scheduleTable(cmd *cobra.Command) error {
	home, c, err := loadSchedule()
	if err != nil {
		return err
	}
	now := scheduleNow()
	running := func(job string) bool {
		return exec.Command("tmux", "has-session", "-t", schedule.SessionName(job)).Run() == nil
	}
	last := func(job string) *schedule.Result { return schedule.LastResult(home, job) }
	logDir := func(job string) string { return tildeHome(schedule.LogDir(home, job)) }
	rows := schedule.Rows(c, now, running, last)
	schedule.Render(ui.New(cmd.OutOrStdout()), rows, now, schedule.TickStatus(), logDir)
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}
	return offerRetries(schedule.FailedJobs(rows), cmd.InOrStdin(), cmd.OutOrStdout(), func(job string) error {
		return retryJob(home, c, job)
	})
}

// offerRetries asks once per failed job and relaunches each yes.
func offerRetries(failed []schedule.Row, in io.Reader, out io.Writer, retry func(job string) error) error {
	r := bufio.NewReader(in)
	for _, f := range failed {
		if !promptYesNo(fmt.Sprintf("retry %s (%s)?", f.Job, f.Failed.Entry), false, r, out) {
			continue
		}
		if err := retry(f.Job); err != nil {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("%s has not run yet — `proveo schedule run %s` starts it now", name, name)
	case !last.Failed():
		return fmt.Errorf("%s's last run %s, not failed — `proveo schedule run %s` starts it again", name, last.Outcome, name)
	}
	if err := schedule.Launch(home, proveoExe(), name, last.Entry, j, nil); err != nil {
		return err
	}
	ui.Appf("retrying %s (%s) — `proveo schedule attach %s` to watch", name, last.Entry, name)
	return nil
}

func tildeHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+string(os.PathSeparator)) {
		return "~" + p[len(h):]
	}
	return p
}

func scheduleLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Show every scheduled run, by job, soonest first: next time, target, model, mode, budget, status, last result",
		RunE:  func(cmd *cobra.Command, _ []string) error { return scheduleTable(cmd) },
	}
}

func scheduleTickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
		Short: "Start every job that is due (what launchd/systemd runs each minute)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return err
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
	return &cobra.Command{
		Use:   "run <task>",
		Short: "Start a job now, outside its schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return err
			}
			j, err := jobNamed(c, args[0])
			if err != nil {
				return err
			}
			if err := schedule.Launch(home, proveoExe(), args[0], "manual", j, nil); err != nil {
				return err
			}
			ui.Appf("started %s — `proveo schedule attach %s` to watch; transcripts in %s", args[0], args[0], schedule.LogDir(home, args[0]))
			return nil
		},
	}
}

func scheduleRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <task>",
		Short: "Start a job again when its last run failed (failed or not-ready)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return err
			}
			return retryJob(home, c, args[0])
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
				return err
			}
			j, err := jobNamed(c, args[0])
			if err != nil {
				return err
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

func scheduleAttachCmd() *cobra.Command {
	var readOnly bool
	cmd := &cobra.Command{
		Use:   "attach <task>",
		Short: "Open a running task's terminal by its task name (the key under jobs: in schedule.yml)",
		Long: `Open a running task's terminal. The task name is its key under jobs: in
~/.proveo/schedule.yml; attach opens the tmux session proveo-sched-<task>.

Without -r your keys reach the agent. Detach with the tmux prefix, then d
(Ctrl-b d); the run keeps going. Inside tmux, attach switches your client
to the task's session; with -r it nests a client (Ctrl-b Ctrl-b d). In
zellij, press Ctrl-b twice, then d.`,
		Example: `proveo schedule attach sample-task
proveo schedule attach sample-task -r`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sess := schedule.SessionName(args[0])
			if exec.Command("tmux", "has-session", "-t", sess).Run() != nil {
				return fmt.Errorf("%s is not running (no tmux session %s) — `proveo schedule` shows its last run", args[0], sess)
			}
			args, nested := attachArgs(sess, readOnly, os.Getenv("TMUX") != "")
			c := exec.Command("tmux", args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if nested {
				c.Env = append(os.Environ(), "TMUX=")
				ui.Notef("read-only inside tmux nests a client: press the prefix twice, then d, to detach")
			}
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&readOnly, "read-only", "r", false, "watch without sending keys to the agent")
	return cmd
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
		RunE: func(*cobra.Command, []string) error {
			home, _, err := loadSchedule()
			if err != nil {
				return err
			}
			return schedule.Install(home, proveoExe(), ui.Appf)
		},
	}
}

func scheduleUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the minutely tick",
		RunE:  func(*cobra.Command, []string) error { return schedule.Uninstall(ui.Appf) },
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
