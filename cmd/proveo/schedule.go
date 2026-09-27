// SPEC: _spec/internal/schedule/schedule.puml
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
		Short: "Run harnesses unattended at set times (a minutely tick started by launchd/systemd)",
	}
	cmd.AddCommand(scheduleLsCmd(), scheduleTickCmd(), scheduleRunCmd(), scheduleWatchCmd(),
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

func scheduleLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List jobs, their next run and their last result",
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, c, err := loadSchedule()
			if err != nil {
				return err
			}
			now := scheduleNow()
			names := make([]string, 0, len(c.Jobs))
			for n := range c.Jobs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				j := c.Jobs[n]
				loc, _ := j.Location()
				var next time.Time
				for _, e := range j.At {
					if t, err := schedule.NextOccurrence(e, now, loc); err == nil && (next.IsZero() || t.Before(next)) {
						next = t
					}
				}
				last := "never"
				if r := schedule.LastResult(home, n); r != nil {
					last = r.Started.In(loc).Format("Mon 15:04") + " " + r.Outcome
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-16s %-8s %-5s next %-22s last %s\n", n, j.Target, j.ModeOrDefault(),
					next.Format("Mon Jan 2 15:04 MST"), last)
			}
			return nil
		},
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
		Use:   "run <job>",
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

func scheduleWatchCmd() *cobra.Command {
	var entry, transcript string
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
			schedule.Watch(home, args[0], entry, transcript, j, nil, notify)
			return nil
		},
	}
	cmd.Flags().StringVar(&entry, "entry", "", "the schedule entry that fired")
	cmd.Flags().StringVar(&transcript, "transcript", "", "the transcript file")
	return cmd
}

func scheduleAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <job>",
		Short: "Watch a running job's terminal (detach with the tmux prefix, then d)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c := exec.Command("tmux", "attach", "-t", schedule.SessionName(args[0]))
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
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
