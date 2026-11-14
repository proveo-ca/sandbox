// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// errInitQuit is how a sub-page reports a confirmed quit through the step that asked it.
var errInitQuit = errors.New("quit")

// checkRows are page 1's checks section: what installing needs, then sbx's own verdict on the host
// when sbx is there to give one. Each row keeps its evidence, fix and note apart; the Docker
// sign-in draws as its own row, never among the checks.
// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml (PAGE 1 CHECKS)
func checkRows(checks []sbx.Prereq, diag []sbx.Check, diagErr error) []choiceui.Row {
	var rows []choiceui.Row
	for _, c := range checks {
		row := choiceui.Row{Kind: choiceui.RowCheck, Mark: choiceui.MarkNeeds, Label: c.Name, Outcome: c.Detail, Fix: c.Fix}
		switch {
		case c.OK:
			row.Mark, row.Fix = choiceui.MarkDone, ""
		case c.Blocks == sbx.BlocksInstall:
			row.Mark = choiceui.MarkFailed
		}
		rows = append(rows, row)
	}
	if diagErr != nil {
		rows = append(rows, choiceui.Row{Kind: choiceui.RowCheck, Mark: choiceui.MarkNeeds, Label: "sbx diagnose",
			Outcome: "gave no answer: " + strings.SplitN(diagErr.Error(), "\n", 2)[0],
			Fix:     "reinstall the pinned sbx with `proveo init --force`"})
	}
	for _, c := range diag {
		if c.Name == sbx.AuthenticationCheck {
			continue
		}
		row := choiceui.Row{Kind: choiceui.RowCheck, Mark: choiceui.MarkLater, Label: c.Name, Outcome: c.Summary()}
		row.Fix, row.Evidence = standAlone(c.Hint, c.Evidence())
		switch c.Status {
		case "pass":
			row.Mark, row.Fix, row.Evidence = choiceui.MarkDone, "", nil
		case "warn":
			row.Mark = choiceui.MarkNeeds
		case "fail":
			row.Mark = choiceui.MarkFailed
			if affects := c.Affects(); affects != "" {
				row.Mark, row.Note = choiceui.MarkNeeds, fmt.Sprintf(outsideARunNote, affects)
			}
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		rows[0].Divider, rows[0].Heading = true, rowChecks
	}
	return rows
}

// linkAbove is how sbx's hint points at the evidence line that holds a link.
const linkAbove = " from the link above"

// standAlone is a fix that never points at another line: a hint that names "the link above" takes
// the link from the evidence in its place, and the evidence drops it.
func standAlone(hint string, evidence []string) (fix string, rest []string) {
	at := slices.IndexFunc(evidence, func(e string) bool { return strings.HasPrefix(e, "http://") || strings.HasPrefix(e, "https://") })
	if !strings.Contains(hint, linkAbove) || at < 0 {
		return hint, evidence
	}
	return strings.Replace(hint, linkAbove, ": "+evidence[at], 1), slices.Delete(slices.Clone(evidence), at, at+1)
}

// The names of the one-shot actions, as the setup page lists them on enter.
const (
	actLogin  = "sign in to Docker if signed out (sbx login)"
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

// leaveUnchosen records every one-shot action d did not choose, and takes each one it chose,
// and each moot one, out of "later".
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func (r *initRecord) leaveUnchosen(d initDecisions, moot ...string) {
	r.chose(rowLogin, d.Login, moot...)
	r.chose(rowWizard, d.Wizard, moot...)
	r.chose(rowGh, d.Gh, moot...)
	r.chose(rowGit, d.Git, moot...)
	r.chose(rowPrompt, d.Prompt, moot...)
}

// chose records one row's answer: now, or a moot row, takes it out of "later"; anything else leaves it.
func (r *initRecord) chose(label string, now bool, moot ...string) {
	if now || slices.Contains(moot, label) {
		delete(r.later, label)
		return
	}
	r.leave(label)
}

// moot takes each label out of "later": the host left its row nothing to do.
func (r *initRecord) moot(labels ...string) {
	for _, l := range labels {
		delete(r.later, l)
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

// initPage is one page of `proveo init`, with the labels the result gives it when a failure keeps
// init from reaching it: rows, the steps it would run, then label; "" for a page the result never names.
type initPage struct {
	label string
	rows  []string
	run   initStep
}

// pages are ps as steps; a page that fails records every page after it as never reached.
func (r *initRecord) pages(ps []initPage) []initStep {
	steps := make([]initStep, len(ps))
	for i, p := range ps {
		var after []string
		for _, q := range ps[i+1:] {
			after = append(append(after, q.rows...), q.label)
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

// failed reports whether err ended init without the operator's choosing: not a quit, or a
// prerequisite that blocks the install.
func failed(err error) bool {
	var blocked blockedErr
	return err != nil && !errors.Is(err, errInitLeft) && !errors.As(err, &blocked)
}

// laterOrder is the setup page's row order, then the credentials page.
var laterOrder = []string{rowLogin, rowWizard, rowGh, rowGit, rowPrompt, labelCredentials}

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
