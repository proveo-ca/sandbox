// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD)
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// initWalk is one `proveo init` past its probes: the host and its plan, what each page answered
// and ran, and the view the pages and their steps show on.
type initWalk struct {
	o          initOptions
	host       sbx.Host
	plan       sbx.Plan
	download   string
	installed  string
	prereqs    []sbx.Prereq
	stop       []sbx.Prereq
	rep        *initReport
	rec        *initRecord
	prompted   bool
	view       walkView
	card       *cardView // the session's view; nil off a terminal
	network    networkState
	prevSbx    *initDecisions // what page 1 settled on its last visit
	prevGit    *initDecisions // what page 2 settled on its last visit
	viaPackage bool
	typed      map[string]string
	done       [initPages]pageDone
}

// onCard walks the pages on one session: page 1 draws from the diagnose run here, the card holds
// the terminal until the last page closes it, and the result prints after.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (ONE SESSION PER WALK)
func (w *initWalk) onCard() error {
	if w.installed != "" {
		w.rep.heard(sbx.DiagnoseAt(resolveBin(w.plan)))
	}
	w.network = hostNetwork(w.installed != "")
	s, err := openCard()
	if err != nil {
		return w.rep.close(w.rec, fmt.Errorf("open the terminal: %w", err))
	}
	w.card = newCardView(s)
	w.view = w.card
	err = runInitSteps(true, w.pages())
	if cerr := s.Close(); err == nil && cerr != nil {
		err = cerr
	}
	return w.rep.close(w.rec, err)
}

// pages are the walk's four pages; a failure names the steps of the pages it kept from running.
func (w *initWalk) pages() []initStep {
	var gitRows []string
	for _, r := range []struct {
		label string
		skip  bool
	}{{rowGit, w.o.skipGit}, {rowGh, w.o.skipGh}, {rowPrompt, w.o.skipPrompt}} {
		if !r.skip {
			gitRows = append(gitRows, r.label)
		}
	}
	return w.rec.pages([]initPage{
		{run: w.sbxPage},
		{rows: gitRows, run: w.gitPage},
		{label: labelCredentials, run: w.credentialsPage},
		{label: labelOperator, run: w.operatorPage},
	})
}

// seen is what each card row's step left; nil off the card.
func (w *initWalk) seen() map[string]rowState {
	if w.card == nil {
		return nil
	}
	return w.card.seen
}

// doneRows are the done rows of the pages before page n.
func (w *initWalk) doneRows(n int) []choiceui.Row {
	var rows []choiceui.Row
	for i := range n - 1 {
		rows = append(rows, w.done[i].row(pageNames[i]))
	}
	return rows
}

// ctx is what page n's steps act on, with plan placed for applied.
func (w *initWalk) ctx(n int, applied sbx.Plan) applyCtx {
	return applyCtx{plan: w.plan, applied: applied, download: w.download, host: w.host, rep: w.rep, view: w.view,
		frame: subFrame{at: initStepAt(n), done: w.doneRows(n), ahead: aheadRows(n+1, setupAhead(w.o.skipSetup))}}
}

// sbxPage is page 1: the checks, then sbx install, PATH, Docker login and the network baseline,
// run in place on Enter.
func (w *initWalk) sbxPage(at choiceui.Step) (stepResult, error) {
	def := carried(defaultDecisions(w.plan, w.installed, w.o, w.prompted), w.prevSbx)
	d := def
	var idle []labelled
	if w.prompted {
		at = initStepAt(at.At)
		in := sbxPageIn{host: w.host, plan: w.plan, installed: w.installed, prereqs: w.prereqs, diag: w.rep.diag, diagErr: w.rep.diagErr,
			diagnosed: w.rep.diagnosed, auth: w.rep.auth, network: w.network, stop: w.stop}
		form := sbxForm(in, at, def, w.o, w.seen(), func(d initDecisions) int {
			return len(planRows(applySbx(d, w.planned(d))))
		})
		out, err := w.view.Ask(form)
		switch {
		case err != nil:
			return stepNext, err
		case len(w.stop) > 0:
			return stepNext, blockedErr{w.stop}
		case out != choiceui.Accepted:
			return stepQuit, nil
		}
		d = answered(form, def)
		idle = idleRows(form, planRows(applySbx(d, w.planned(d))), map[string]string{rowInstall: "kept " + w.installed},
			rowInstall, rowPath, rowLogin, rowBaseline)
	}
	if d.Prefix != w.plan.Prefix {
		plan, err := sbx.PlanFor(w.host, d.Prefix, w.download)
		if err != nil {
			return stepNext, err
		}
		w.plan = plan
		w.installed, _ = installedVersion(plan)
	}
	w.viaPackage = d.Install == installPackaged || (d.Install == installSkip && w.viaPackage)
	applied := appliedPlan(w.plan, w.viaPackage)
	w.rep.applied = applied
	w.rec.chose(rowLogin, d.Login, w.rep.mootRows()...)
	steps := applySbx(d, w.ctx(1, applied))
	if err := runApply(w.view, steps, w.rec.did, idle); err != nil {
		var failed stepErr
		if errors.As(err, &failed) {
			w.rec.unreached(failed.rest...)
		}
		return stepNext, err
	}
	d = settle(d, steps, applied)
	w.installed, _ = installedVersion(applied)
	w.prevSbx, w.o.force = &d, false
	w.done[0] = w.sbxDone()
	return stepNext, nil
}

// planned is what an apply of d acts on before it runs, for counting its rows.
func (w *initWalk) planned(d initDecisions) applyCtx {
	c := plannedCtx(w.plan, w.host, d, w.viaPackage)
	c.rep = w.rep
	return c
}

// sbxDone is page 1's done row: install, PATH, Docker login and network as they ran, and the
// checks that still need the operator.
func (w *initWalk) sbxDone() pageDone {
	seen := w.seen()
	var states []labelled
	for _, label := range []string{rowInstall, rowPath, rowLogin, rowBaseline} {
		if st, ok := seen[label]; ok {
			states = append(states, labelled{label, st})
		}
	}
	need := needYou(checkRows(w.prereqs, w.rep.diag, w.rep.diagErr))
	if need != "" {
		states = append(states, labelled{rowChecks, rowState{mark: choiceui.MarkNeeds}})
	}
	return doneOf(states, need, func(int) string {
		var brief []labelled
		for _, s := range states {
			if s.label == rowInstall || s.label == rowLogin {
				brief = append(brief, s)
			}
		}
		return doneOf(brief, need, nil).outcome
	})
}

// gitPage is page 2: git identity with its fields, GitHub and its sub-pages, the prompt kit, run
// in place on Enter.
func (w *initWalk) gitPage(at choiceui.Step) (stepResult, error) {
	def := carried(defaultDecisions(w.plan, w.installed, w.o, w.prompted), w.prevGit)
	d := def
	id := gitidentity.Resolve(os.Getenv, nil)
	_, lookErr := exec.LookPath("git")
	answer := gitAnswer{name: id.Name, email: id.Email, held: id.Complete() || lookErr != nil}
	var idle []labelled
	if w.prompted {
		at = initStepAt(at.At)
		form := gitForm(gitPageIn{id: id, gitOK: lookErr == nil}, at, def, w.o, w.doneRows(2), w.seen(), func(d initDecisions) int {
			return len(planRows(applyGit(d, w.planned(d), answer)))
		})
		out, err := w.view.Ask(form)
		switch {
		case err != nil:
			return stepNext, err
		case out == choiceui.Back:
			return stepBack, nil
		case out != choiceui.Accepted:
			return stepQuit, nil
		}
		d = answered(form, def)
		if !answer.held {
			answer.name, answer.email = form.FieldValue(rowName), form.FieldValue(rowEmail)
		}
		idle = idleRows(form, planRows(applyGit(d, w.planned(d), answer)), nil, rowGit, rowGh, rowPrompt)
	}
	w.rec.chose(rowGit, d.Git)
	w.rec.chose(rowGh, d.Gh)
	w.rec.chose(rowPrompt, d.Prompt)
	steps := applyGit(d, w.ctx(2, w.rep.applied), answer)
	if err := runApply(w.view, steps, w.rec.did, idle); err != nil {
		if errors.Is(err, errInitQuit) {
			return stepQuit, nil
		}
		var failed stepErr
		if errors.As(err, &failed) {
			w.rec.unreached(failed.rest...)
		}
		return stepNext, err
	}
	w.prevGit = &d
	w.done[1] = w.gitDone()
	return stepNext, nil
}

// gitDone is page 2's done row: the identity, GitHub and the prompt kit as they ran.
func (w *initWalk) gitDone() pageDone {
	seen := w.seen()
	var states []labelled
	for _, label := range []string{rowGit, rowGh, rowPrompt} {
		if st, ok := seen[label]; ok {
			states = append(states, labelled{label, st})
		}
	}
	return doneOf(states, "", func(later int) string {
		id := ""
		if len(states) > 0 && states[0].label == rowGit && states[0].mark != choiceui.MarkLater {
			id, _, _ = strings.Cut(states[0].doneText(), " · ")
		}
		if later == 0 {
			return id
		}
		return joinBrief(id, fmt.Sprintf("%d later", later))
	})
}

// joinBrief joins a count form's parts that say something.
func joinBrief(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " · ")
}

// credentialsPage is page 3: a sign-in per subscription and a field per key, run in place.
func (w *initWalk) credentialsPage(at choiceui.Step) (stepResult, error) {
	if w.prompted {
		at = initStepAt(at.At)
	}
	res, done, err := credentialStage(w.o, at, w.typed, w.rec, w.rep, cardPage{view: w.view, done: w.doneRows(3)})
	if res != stepBack {
		w.done[2] = done
	}
	return res, err
}

// operatorPage is page 4: the operator's name, then sbx setup as the probe, run before it
// draws, found it.
func (w *initWalk) operatorPage(at choiceui.Step) (stepResult, error) {
	var setup choiceui.Row
	if w.prompted {
		at = initStepAt(at.At)
		setup = setupRow(w.rep.probeSetup(), defaultDecisions(w.plan, w.installed, w.o, true).Wizard, w.o.skipSetup)
	}
	res, err := operatorStage(w.o, at, w.rep, cardPage{view: w.view, done: w.doneRows(4)}, setup)
	if res == stepNext {
		w.rec.chose(rowWizard, w.rep.wizard)
	}
	return res, err
}

// idleRows are the step rows of a page Enter leaves as they are, with what each reads: a status row
// as it stands, an answer as its cell names it, or override's words for it.
func idleRows(f *choiceui.Form, planned []string, override map[string]string, labels ...string) []labelled {
	var out []labelled
	for _, label := range labels {
		at := slices.IndexFunc(f.Rows, func(r choiceui.Row) bool { return r.Label == label })
		if at < 0 || slices.Contains(planned, label) {
			continue
		}
		r := f.Rows[at]
		if r.When != nil && f.Selection(r.When.Row) != r.When.Option {
			continue
		}
		st := labelled{label: label, rowState: rowState{mark: r.Mark, outcome: r.Outcome}}
		if r.Kind == choiceui.RowAnswer || r.Kind == choiceui.RowStatus && r.Mark == choiceui.MarkAhead {
			st.mark, st.outcome = choiceui.MarkLater, f.Selection(label)
			if r.Paint[st.outcome] != "" {
				st.outcome = r.Paint[st.outcome]
			}
			if text, ok := override[label]; ok && f.Selection(label) == installSkip {
				st.outcome = text
			}
		}
		out = append(out, st)
	}
	return out
}
