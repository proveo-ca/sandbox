// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
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
			if affects := c.Affects(); affects != "" {
				row.State = choiceui.CheckWarn
				row.Evidence = append(row.Evidence, fmt.Sprintf(outsideARunNote, affects))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// errInitCancel is how a page reports that the operator left init before it acted.
var errInitCancel = errors.New("cancelled")

// preflightPage draws page 1 and maps how it was left: on, out of the wizard, or refused. A page
// that cannot draw becomes a warning in the sbx block.
func preflightPage(host sbx.Host, rows []choiceui.CheckRow, stop []sbx.Prereq, at choiceui.Step, rep *initReport) (stepResult, error) {
	page := &choiceui.CheckPage{
		Banner: choiceui.Banner(),
		Title:  "init — preparing the " + describeHost(host) + " host",
		Rows:   rows,
		Step:   at,
		Next:   "setup",
		Glyphs: posture.GlyphModeFrom(os.Getenv),
	}
	if len(stop) > 0 {
		page.Blocked = blockedErr{stop}.Error()
	}
	out, err := page.Run()
	switch {
	case err != nil:
		rep.sbxItem(sandbox.Item{Warn: true, Text: "checks page — not shown", Detail: errLines(err)})
		return stepNone, nil
	case out == choiceui.Accepted:
		return stepNext, nil
	case out == choiceui.Quit:
		return stepQuit, nil
	case len(stop) > 0:
		return stepNext, blockedErr{stop}
	}
	return stepNext, fmt.Errorf("%w at the checks page", errInitCancel)
}

// The names of the one-shot actions, as the setup page lists them on enter.
const (
	actSignIn = "sign in to Docker (sbx login)"
	actWizard = "run sbx's first-run wizard (sbx setup)"
	actGit    = "set the sandbox git identity"
	actPrompt = "install the prompt kit"
	actGitHub = "connect GitHub"
)

// initRecord is what one `proveo init` ran, left for later and never reached, by the setup
// page's row labels; it is kept across pages and revisits.
type initRecord struct {
	ran    []string            // what ran, in order, once each
	later  map[string][]string // a row answered "later", and the one how-to it keeps when no terminal asked
	notRun []string            // what was chosen and never ran, in the order it would have
}

// did records that label ran; it is no longer left for later.
func (r *initRecord) did(label string) {
	if label == "" || slices.Contains(r.ran, label) {
		return
	}
	r.ran = append(r.ran, label)
	delete(r.later, label)
}

// leave records that label was answered "later", unless it already ran.
func (r *initRecord) leave(label string, howTo ...string) {
	if slices.Contains(r.ran, label) {
		return
	}
	if r.later == nil {
		r.later = map[string][]string{}
	}
	r.later[label] = howTo
}

// leaveUnchosen records every one-shot action d did not choose, and takes each one it chose out
// of "later".
func (r *initRecord) leaveUnchosen(d initDecisions) {
	for _, a := range []struct {
		chosen bool
		label  string
	}{{d.Login, rowSignIn}, {d.Wizard, rowWizard}, {d.Gh, rowGh}, {d.Git, rowGit}, {d.Prompt, rowPrompt}} {
		if a.chosen {
			delete(r.later, a.label)
		} else {
			r.leave(a.label)
		}
	}
}

// unreached records what a failure or a run blocker kept from running, unless it ran or waits
// as "later" already.
func (r *initRecord) unreached(labels ...string) {
	if r == nil {
		return
	}
	for _, l := range labels {
		if _, later := r.later[l]; l != "" && !later && !slices.Contains(r.ran, l) && !slices.Contains(r.notRun, l) {
			r.notRun = append(r.notRun, l)
		}
	}
}

// initPage is one page of `proveo init`, with the label the result gives it when a failure keeps
// init from reaching it; "" for a page the result never names.
type initPage struct {
	label string
	run   initStep
}

// pages are ps as steps; a page that fails records every page after it as never reached.
func (r *initRecord) pages(ps []initPage) []initStep {
	steps := make([]initStep, len(ps))
	for i, p := range ps {
		var after []string
		for _, q := range ps[i+1:] {
			after = append(after, q.label)
		}
		steps[i] = func(at choiceui.Step) (stepResult, error) {
			res, err := p.run(at)
			if failed(err) {
				r.unreached(after...)
			}
			return res, err
		}
	}
	return steps
}

// failed reports whether err ended init without the operator's choosing: not a quit, a cancel,
// or a prerequisite that blocks the install.
func failed(err error) bool {
	var blocked blockedErr
	return err != nil && !errors.Is(err, errInitLeft) && !errors.Is(err, errInitCancel) && !errors.As(err, &blocked)
}

// laterOrder is the setup page's row order, then the credentials page.
var laterOrder = []string{rowSignIn, rowWizard, rowGh, rowGit, rowPrompt, labelCredentials}

// lines are what was left for later, in the page's order, and what never ran.
func (r *initRecord) lines() (later, notRun []string) {
	for _, label := range laterOrder {
		if _, ok := r.later[label]; ok {
			later = append(later, label)
		}
	}
	return later, r.notRun
}

// facts are the result's "later" and "not run" rows, each a list stacked under its key.
func (r *initRecord) facts() []ui.Fact {
	var out []ui.Fact
	stack := func(key string, labels []string) {
		for i, l := range labels {
			f := ui.Fact{Role: ui.RoleHost, Value: l}
			if i == 0 {
				f.Key = key
			}
			if key == "later" {
				f.Detail = r.later[l]
			}
			out = append(out, f)
		}
	}
	later, notRun := r.lines()
	stack("later", later)
	stack("not run", notRun)
	return out
}
