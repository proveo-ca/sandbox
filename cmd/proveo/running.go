// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/schedule/schedule.puml
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/ui"
)

// tmuxHasSession reports whether a tmux session exists. Tests replace it.
var tmuxHasSession = func(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// pickIndex asks for one row of labels on the terminal. Tests replace it.
var pickIndex = func(prompt string, labels []string) (int, error) {
	picked, err := (&choiceui.FilterList{Prompt: prompt, Items: labels}).Pick()
	if err != nil {
		return -1, err
	}
	return picked[0], nil
}

// menuChoice is one row in a run or attach picker.
type menuChoice struct {
	Verb, Name, Detail string
	Attach             bool
}

func stdioInteractive(cmd *cobra.Command) bool {
	in, okIn := cmd.InOrStdin().(*os.File)
	out, okOut := cmd.OutOrStdout().(*os.File)
	if !okIn || !okOut {
		return false
	}
	return term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func runningOrNone() []schedule.RunningTask {
	tasks, err := currentRunning()
	if err != nil {
		return nil
	}
	return tasks
}

// currentRunning lists the scheduled tasks whose tmux session is up; none when no schedule.yml exists.
func currentRunning() ([]schedule.RunningTask, error) {
	home, c, err := loadSchedule()
	var none errNoSchedule
	if errors.As(err, &none) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return schedule.RunningTasks(c, scheduleNow(), liveRun(home)), nil
}

// liveRun is a job's run in progress, read from its tmux session and its newest transcript.
func liveRun(home string) func(job string) *schedule.Live {
	return func(job string) *schedule.Live {
		if !tmuxHasSession(schedule.SessionName(job)) {
			return nil
		}
		return &schedule.Live{Since: schedule.CurrentRun(home, job)}
	}
}

// writeRunning prints the running tasks on rows as one Facts block keyed by task, each valued as
// `proveo schedule` values it, or "nothing running" on none when there are none.
func writeRunning(rows, none *ui.Printer, tasks []schedule.RunningTask) {
	if len(tasks) == 0 {
		none.Asyncf("nothing running")
		return
	}
	facts := make([]ui.Fact, len(tasks))
	for i, t := range tasks {
		facts[i] = ui.Fact{Role: ui.RoleAsync, Key: t.Job, Value: t.Value}
	}
	rows.Facts(facts...)
}

// writeTargets prints each harness target as one aligned fact valued with its image.
func writeTargets(p *ui.Printer, targets map[string]string) {
	names := sortedKeys(targets)
	facts := make([]ui.Fact, len(names))
	for i, name := range names {
		facts[i] = ui.Fact{Role: ui.RoleApp, Key: name, Value: targets[name]}
	}
	p.Facts(facts...)
}

// writeRunLists prints `proveo run`'s two lists, each under its heading: the running tasks, then the targets.
func writeRunLists(w io.Writer, tasks []schedule.RunningTask, targets map[string]string) {
	p := ui.New(w)
	p.Section(ui.SectionRunning)
	writeRunning(p, p, tasks)
	p.Section(ui.SectionTargets)
	writeTargets(p, targets)
}

func runChoices(tasks []schedule.RunningTask, targets map[string]string) []menuChoice {
	out := make([]menuChoice, 0, len(tasks)+len(targets))
	for _, t := range tasks {
		out = append(out, menuChoice{Verb: "attach", Name: t.Job, Detail: t.Summary, Attach: true})
	}
	for _, name := range sortedKeys(targets) {
		out = append(out, menuChoice{Verb: "run", Name: name, Detail: targets[name]})
	}
	return out
}

func aliasChoices(target string, tasks []schedule.RunningTask) []menuChoice {
	out := make([]menuChoice, 0, len(tasks)+1)
	for _, t := range tasks {
		out = append(out, menuChoice{Verb: "attach", Name: t.Job, Detail: t.Summary, Attach: true})
	}
	out = append(out, menuChoice{Verb: "start", Name: target, Detail: "proveo run " + target})
	return out
}

func choiceLabels(choices []menuChoice) []string {
	vw, nw := 0, 0
	for _, c := range choices {
		vw = max(vw, len(c.Verb))
		nw = max(nw, len(c.Name))
	}
	out := make([]string, len(choices))
	for i, c := range choices {
		out[i] = fmt.Sprintf("%-*s  %-*s  %s", vw, c.Verb, nw, c.Name, c.Detail)
	}
	return out
}

// pickerClosed is a picker the operator closed without a choice; it reads as the caller's abort words.
type pickerClosed struct{ abort string }

func (e pickerClosed) Error() string { return e.abort }

func offer(choices []menuChoice, prompt, abort string) (menuChoice, error) {
	idx, err := pickIndex(prompt, choiceLabels(choices))
	if errors.Is(err, choiceui.ErrListClosed) {
		return menuChoice{}, pickerClosed{abort}
	}
	if err != nil {
		return menuChoice{}, err
	}
	if idx < 0 || idx >= len(choices) {
		return menuChoice{}, pickerClosed{abort}
	}
	return choices[idx], nil
}
