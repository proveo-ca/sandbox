// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/schedule/schedule.puml
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/ui"
)

// tmuxHasSession reports whether a tmux session exists. Tests replace it.
var tmuxHasSession = func(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// pickIndex is the mise-style selector. Tests replace it.
var pickIndex = func(prompt string, labels []string) (int, error) {
	return fuzzyfinder.Find(labels, func(i int) string { return labels[i] },
		fuzzyfinder.WithPromptString(prompt))
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

func currentRunning() ([]schedule.RunningTask, error) {
	home, c, err := loadSchedule()
	if err != nil {
		return nil, err
	}
	return schedule.RunningTasks(c, scheduleNow(), func(job string) *schedule.Live {
		if !tmuxHasSession(schedule.SessionName(job)) {
			return nil
		}
		return &schedule.Live{Since: schedule.CurrentRun(home, job)}
	}), nil
}

func writeRunning(w io.Writer, tasks []schedule.RunningTask) {
	p := ui.New(w)
	p.Section(ui.SectionRunning)
	if len(tasks) == 0 {
		p.Notef("nothing running")
		return
	}
	width := 0
	for _, t := range tasks {
		width = max(width, len(t.Job))
	}
	for _, t := range tasks {
		p.Appf("%-*s  %s", width, t.Job, t.Line)
	}
}

func writeTargets(w io.Writer, targets map[string]string) {
	p := ui.New(w)
	p.Section(ui.SectionTargets)
	names := sortedKeys(targets)
	width := 0
	for _, n := range names {
		width = max(width, len(n))
	}
	for _, name := range names {
		p.Appf("%-*s  %s", width, name, targets[name])
	}
}

func writeRunLists(w io.Writer, tasks []schedule.RunningTask, targets map[string]string) {
	writeRunning(w, tasks)
	writeTargets(w, targets)
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

func offer(choices []menuChoice, prompt, abort string) (menuChoice, error) {
	idx, err := pickIndex(prompt, choiceLabels(choices))
	if errors.Is(err, fuzzyfinder.ErrAbort) {
		return menuChoice{}, fmt.Errorf("%s", abort)
	}
	if err != nil {
		return menuChoice{}, err
	}
	if idx < 0 || idx >= len(choices) {
		return menuChoice{}, fmt.Errorf("%s", abort)
	}
	return choices[idx], nil
}
