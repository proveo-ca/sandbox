// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// errInitBack and errInitQuit are how the setup page reports a step back and a confirmed quit.
var (
	errInitBack = errors.New("step back")
	errInitQuit = errors.New("quit")
)

const (
	checksToInstall = "to install sbx"
	checksFromSbx   = "sbx diagnose"
)

// preflightRows are the checks page 1 shows: what installing needs, then sbx's
// own verdict on the host when sbx is there to give one.
func preflightRows(checks []sbx.Prereq, diag []sbx.Check, diagErr error) []choiceui.CheckRow {
	var rows []choiceui.CheckRow
	for _, c := range checks {
		row := choiceui.CheckRow{Group: checksToInstall, State: choiceui.CheckWarn, Name: c.Name, Detail: c.Detail, Hint: c.Fix}
		switch {
		case c.OK:
			row.State = choiceui.CheckOK
		case c.Blocks == sbx.BlocksInstall:
			row.State = choiceui.CheckFail
		}
		rows = append(rows, row)
	}
	if diagErr != nil {
		rows = append(rows, choiceui.CheckRow{
			Group: checksFromSbx, State: choiceui.CheckWarn, Name: "sbx diagnose",
			Detail: "gave no answer: " + strings.SplitN(diagErr.Error(), "\n", 2)[0],
			Hint:   "reinstall the pinned sbx with `proveo init --force`",
		})
	}
	for _, c := range diag {
		row := choiceui.CheckRow{Group: checksFromSbx, State: choiceui.CheckSkip, Name: c.Name, Detail: c.Summary(), Evidence: c.Evidence(), Hint: c.Hint}
		switch c.Status {
		case "pass":
			row.State = choiceui.CheckOK
		case "warn":
			row.State = choiceui.CheckWarn
		case "fail":
			row.State = choiceui.CheckFail
		}
		rows = append(rows, row)
	}
	return rows
}

// errBlocked is the refusal to install on a host that fails a prerequisite.
func errBlocked(stop []sbx.Prereq) error {
	return fmt.Errorf("nothing good follows an install here: %s", strings.Join(sbx.Names(stop), ", "))
}

// preflightPage draws page 1 and maps how it was left: on, out of the wizard, or refused.
func preflightPage(host sbx.Host, rows []choiceui.CheckRow, stop []sbx.Prereq, at choiceui.Step) (stepResult, error) {
	page := &choiceui.CheckPage{
		Banner: choiceui.Banner(),
		Title:  "init — preparing the " + describeHost(host) + " host",
		Rows:   rows,
		Step:   at,
		Next:   "setup",
		Glyphs: posture.GlyphModeFrom(os.Getenv),
	}
	if len(stop) > 0 {
		page.Blocked = errBlocked(stop).Error()
	}
	out, err := page.Run()
	switch {
	case err != nil:
		ui.Warnf("%v — continuing without the checks page", err)
		return stepNone, nil
	case out == choiceui.Accepted:
		return stepNext, nil
	case out == choiceui.Quit:
		return stepQuit, nil
	case len(stop) > 0:
		return stepNext, errBlocked(stop)
	}
	return stepNext, fmt.Errorf("cancelled at the init prompt")
}

// The names of the one-shot actions, as a step and as a line of the result.
const (
	actSignIn = "sign in to Docker (sbx login)"
	actWizard = "run sbx's first-run wizard (sbx setup)"
	actGit    = "set the sandbox git identity"
	actPrompt = "install the prompt kit"
	actGitHub = "connect GitHub"
	actCreds  = "provision credentials"
)

// initRecord is what one `proveo init` did and what it left for later, kept across pages and revisits.
type initRecord struct {
	ran   []string          // what ran, in order, once each
	later map[string]string // an action answered "later", and the command that does it
}

// did records that name ran; it is no longer left for later.
func (r *initRecord) did(name string) {
	if name == "" || slices.Contains(r.ran, name) {
		return
	}
	r.ran = append(r.ran, name)
	delete(r.later, name)
}

// leave records that action was answered "later", unless it already ran.
func (r *initRecord) leave(action, command string) {
	if slices.Contains(r.ran, action) {
		return
	}
	if r.later == nil {
		r.later = map[string]string{}
	}
	r.later[action] = command
}

// leaveUnchosen records every one-shot action d did not choose.
func (r *initRecord) leaveUnchosen(d initDecisions) {
	for _, a := range []struct {
		chosen          bool
		action, command string
	}{
		{d.Login, actSignIn, sbx.Binary + " login"},
		{d.Wizard, actWizard, sbx.Binary + " setup"},
		{d.Git, actGit, "proveo init"},
		{d.Prompt, actPrompt, "proveo init"},
		{d.Gh, actGitHub, "proveo init"},
	} {
		if !a.chosen {
			r.leave(a.action, a.command)
		}
	}
}

// laterOrder is the order the result lists what was left.
var laterOrder = []string{actSignIn, actWizard, actGit, actPrompt, actGitHub, actCreds}

// lines is the result: what ran, what was left with its command, and the next command to type.
func (r *initRecord) lines() (ran, later []string, next string) {
	for _, action := range laterOrder {
		if cmd, ok := r.later[action]; ok {
			later = append(later, fmt.Sprintf("%s — `%s`", action, cmd))
		}
	}
	return r.ran, later, "proveo run"
}

// report prints the result where it stays on screen once init has returned.
func (r *initRecord) report() {
	ran, later, next := r.lines()
	ui.Section(ui.SectionResults)
	for _, name := range ran {
		ui.Okf("done: %s", name)
	}
	for _, line := range later {
		ui.Notef("later: %s", line)
	}
	ui.Hostf("next: `%s` — it lists the harnesses this host can start", next)
}
