// SPEC: _spec/internal/ui/step-lines.puml, _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// stepKind is how a step shows while it runs.
type stepKind int

const (
	stepLive   stepKind = iota // a live line; plain output keeps its line
	stepHand                   // its line stays, then another program or a prompt has the terminal
	stepQuiet                  // a live line alone: its caller prints what it found
	stepSilent                 // nothing: its own heading and block follow
)

// applyStep is one thing a page's Enter runs. A step with no name runs unlisted.
type applyStep struct {
	name    string // what the step lines name
	line    string // what stays on screen: a live step's plain line, a hand step's start line
	label   string // the page's row it answers, as the result names it
	row     string // the card row it runs on, when that is not label
	doing   string // what the card's running row says
	help    string // what the help under the card says while it runs
	kind    stepKind
	run     func() error
	outcome func() (choiceui.Mark, string) // the card row's outcome once its last step ran; nil reads done
}

// cardRow is the card row a listed step runs on; "" for an unlisted one.
func (s applyStep) cardRow() string {
	if s.name == "" {
		return ""
	}
	return cmp.Or(s.row, s.label)
}

// applyCtx is what one apply acts on.
type applyCtx struct {
	plan     sbx.Plan // the release plan: what to download and how to place it
	applied  sbx.Plan // the plan setup acts on once sbx is in place
	download string
	host     sbx.Host
	rep      *initReport // where the steps' facts and items gather; nil when only the names are read
	view     walkView    // where the steps show; nil is the step lines
	frame    subFrame    // the page a sub-page stands over
}

// v is the view the steps show on.
func (c applyCtx) v() walkView { return viewOr(c.view) }

// ctx is the walk's context: Ctrl+C on the card cancels it.
func (c applyCtx) ctx() context.Context { return c.v().Context() }

// applySbx lists what page 1's Enter runs for d, in row order: sbx install, PATH, the checks,
// Docker login, the network baseline; the sbx block prints after them.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD)
func applySbx(d initDecisions, c applyCtx) []applyStep {
	var steps []applyStep
	bin := func() string { return resolveBin(c.applied) }
	var sum string
	var verified bool
	installed := "installed " + sbx.Release
	switch d.Install {
	case installPackaged:
		if p := c.plan.Packaged; p != nil {
			steps = append(steps,
				applyStep{name: "download the distro package", line: "download the distro package", label: rowInstall,
					doing: "downloading " + p.Asset,
					run:   func() (err error) { sum, err = fetch(c.ctx(), p.URL, filepath.Join(c.download, p.Asset)); return err }},
				applyStep{name: "install the distro package", line: "distro package — installing sbx " + sbx.Release, label: rowInstall, kind: stepHand,
					doing: "installing sbx " + sbx.Release, help: "sudo may ask for your password",
					run:     func() error { return runPackage(*p, c.download, sum, c) },
					outcome: func() (choiceui.Mark, string) { return choiceui.MarkDone, installed }})
		} else {
			steps = append(steps, applyStep{label: rowInstall,
				run: func() error { return fmt.Errorf("this release ships no distro package for %s", describeHost(c.host)) }})
		}
	case installTarball:
		steps = append(steps,
			applyStep{name: "download sbx " + sbx.Release, line: "download sbx " + sbx.Release, label: rowInstall,
				doing: "downloading " + sbx.Release,
				run:   func() (err error) { sum, verified, err = fetchRelease(c.ctx(), c.plan, c.download); return err }},
			applyStep{name: "install sbx " + installPlace(c.plan, false), line: "sbx " + sbx.Release + " — installing " + installPlace(c.plan, true),
				label: rowInstall, kind: stepHand, doing: "installing " + installPlace(c.plan, true),
				run:     func() error { return placeRelease(c.plan, c.download, sum, verified, c) },
				outcome: func() (choiceui.Mark, string) { return choiceui.MarkDone, installed }})
	}
	if dir := filepath.Dir(c.applied.Bin); c.applied.Bin != "" && !onPath(dir) && (d.Install == installTarball || sbxPathDir(c.applied) != "") &&
		(dir != d.pathDone || d.AddPath && !rcAdds(dir)) {
		var said sandbox.Item
		s := applyStep{label: rowPath, run: func() error {
			if dir := sbxPathDir(c.applied); dir != "" {
				said = ensureSbxOnPath(dir, d.AddPath)
				c.rep.sbxItem(said)
			}
			return nil
		}, outcome: func() (choiceui.Mark, string) { return itemMark(said) }}
		if d.AddPath {
			s.name, s.line, s.doing = "add "+dir+" to PATH", "add "+ui.Tilde(dir)+" to PATH", "adding "+ui.Tilde(dir)
		}
		steps = append(steps, s)
	}
	work := len(steps) > 0
	steps = append(steps, checksStep(d, c, bin))
	if d.Login && (c.rep == nil || !c.rep.diagnosed || c.rep.auth == sbx.AuthOut || d.Install != installSkip && c.rep.auth != sbx.AuthIn) {
		steps = append(steps, loginStep(d, c, bin))
	}
	if d.Baseline != "" {
		args := sbx.PolicyInitArgs(d.Baseline)
		mark, text := choiceui.MarkDone, d.Baseline
		steps = append(steps, applyStep{name: "bind the network baseline: " + d.Baseline,
			line: "sbx policy init — binding the " + d.Baseline + " network baseline", label: rowBaseline, kind: stepHand,
			doing: "sbx policy init " + d.Baseline,
			run: func() error {
				err := c.v().Hand(command(bin(), args...), []string{"sbx's screen"}, said(choiceui.MarkDone, d.Baseline))
				if interrupted(err) {
					return err
				}
				if err != nil {
					mark, text = choiceui.MarkNeeds, "not changed"
					c.rep.sbxItem(sandbox.Item{Warn: true, Text: rowBaseline + " — not changed",
						Detail: append(errDetail(err), "`"+sbx.Binary+" "+strings.Join(args, " ")+"` by hand")})
				}
				return nil
			}, outcome: func() (choiceui.Mark, string) { return mark, text }})
	}
	if c.rep != nil && (work || len(steps) > 1 || !c.rep.sbxSaid) {
		steps = slices.Concat([]applyStep{{run: func() error { c.rep.openSbx(); return nil }}}, steps,
			[]applyStep{{run: func() error { c.rep.printSbx(); return nil }}})
	}
	return steps
}

// checksStep judges the host by sbx diagnose: again after an install, else by the diagnose page 1
// drew from. On the card the check rows run while it asks and redraw from what it said.
func checksStep(d initDecisions, c applyCtx, bin func() string) applyStep {
	fresh := c.rep == nil || !c.rep.diagnosed || d.Install != installSkip
	s := applyStep{label: labelVerify, row: rowChecks, kind: stepQuiet, doing: "sbx diagnose", run: func() error {
		if !fresh {
			return verifyWith(bin(), c.rep, c.rep.diag, c.rep.diagErr)
		}
		c.v().Edit(func(f *choiceui.Form) { markChecks(f, choiceui.MarkRunning) })
		err := verify(bin(), c.rep)
		c.redrawChecks()
		return err
	}}
	if fresh {
		s.name = "verify sbx (sbx diagnose)"
	}
	return s
}

// redrawChecks puts the latest diagnose's rows in place of the card's check rows.
func (c applyCtx) redrawChecks() {
	if c.rep == nil {
		return
	}
	rows := checkRows(c.rep.prereqs, c.rep.diag, c.rep.diagErr)
	c.v().Edit(func(f *choiceui.Form) { replaceChecks(f, rows) })
}

// loginStep hands `sbx login` the terminal while sbx diagnose says Docker is signed out; a second
// diagnose, as the hand-off ends, decides its outcome and redraws the check rows.
func loginStep(d initDecisions, c applyCtx, bin func() string) applyStep {
	var confirmed *sbx.Auth
	confirm := func() sbx.Auth {
		if confirmed == nil {
			a := c.rep.diagnose(bin())
			confirmed = &a
		}
		return *confirmed
	}
	return applyStep{name: actLogin, label: rowLogin, kind: stepSilent, doing: "sbx login · Docker not signed in",
		help: fmt.Sprintf("sbx login takes the terminal for Docker's browser sign-in; page 1/%d returns when it exits", initPages),
		run: func() error {
			defer func() {
				if confirmed != nil {
					c.redrawChecks()
				}
			}()
			return applyLogin(loginStage{
				auth: func() sbx.Auth {
					if c.rep != nil && c.rep.diagnosed && !c.rep.stale {
						return c.rep.auth
					}
					return c.rep.diagnose(bin())
				},
				confirm: confirm,
				login: func() error {
					return c.v().Hand(command(bin(), sbx.LoginArgs()...), []string{"Docker not signed in", "sbx's screen"},
						func(err error) (choiceui.Mark, string) {
							if err != nil {
								return failedMark(err)
							}
							return authMark(confirm())
						})
				},
				tty: c.v().card(),
				rep: c.rep,
			})
		},
		outcome: func() (choiceui.Mark, string) {
			if c.rep == nil {
				return choiceui.MarkDone, ""
			}
			return authMark(c.rep.auth)
		}}
}

// authMark is the Docker login row for what sbx diagnose said of the sign-in.
func authMark(a sbx.Auth) (choiceui.Mark, string) {
	switch a {
	case sbx.AuthIn:
		return choiceui.MarkDone, loginSignedIn
	case sbx.AuthOut:
		return choiceui.MarkNeeds, "not signed in"
	}
	return choiceui.MarkLater, loginUnchecked
}

// said is a hand-off's outcome when its program succeeds: m and text.
func said(m choiceui.Mark, text string) func(error) (choiceui.Mark, string) {
	return func(error) (choiceui.Mark, string) { return m, text }
}

// itemMark is a step's card outcome from the item it said: ✓ or ! and the text after its subject.
func itemMark(it sandbox.Item) (choiceui.Mark, string) {
	_, text, ok := strings.Cut(it.Text, " — ")
	if !ok {
		text = it.Text
	}
	switch {
	case it.Text == "":
		return choiceui.MarkDone, "on PATH"
	case it.Fail:
		return choiceui.MarkFailed, text
	case it.Warn:
		return choiceui.MarkNeeds, text
	}
	return choiceui.MarkDone, text
}

// failedMark is a failed step's card outcome: a Ctrl+C stopped it, or why it failed.
func failedMark(err error) (choiceui.Mark, string) {
	var hand handoffErr
	switch {
	case interrupted(err):
		return choiceui.MarkNeeds, "stopped · Ctrl+C ended it"
	case errors.As(err, &hand):
		return choiceui.MarkNeeds, "stopped · " + hand.err.Error()
	}
	first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return choiceui.MarkFailed, ui.TildeIn(first)
}

// markChecks marks every check row of f m.
func markChecks(f *choiceui.Form, m choiceui.Mark) {
	for i := range f.Rows {
		if f.Rows[i].Kind == choiceui.RowCheck {
			f.Rows[i].Mark = m
		}
	}
}

// replaceChecks puts rows in place of f's check rows.
func replaceChecks(f *choiceui.Form, rows []choiceui.Row) {
	first := slices.IndexFunc(f.Rows, func(r choiceui.Row) bool { return r.Kind == choiceui.RowCheck })
	if first < 0 {
		return
	}
	end := first
	for end < len(f.Rows) && f.Rows[end].Kind == choiceui.RowCheck {
		end++
	}
	f.Rows = slices.Concat(f.Rows[:first:first], rows, f.Rows[end:])
}

// applyGit lists what page 2's Enter runs for d, in row order: git identity, GitHub, the prompt
// kit; the git block prints after GitHub. A git identity the host already holds runs unlisted.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD)
func applyGit(d initDecisions, c applyCtx, id gitAnswer) []applyStep {
	var steps []applyStep
	bin := func() string { return resolveBin(c.applied) }
	if c.rep != nil && (d.Git || d.Gh) {
		steps = append(steps, applyStep{run: func() error { c.rep.openGit(); return nil }})
	}
	if d.Git {
		mark, text := choiceui.MarkDone, ""
		s := applyStep{label: rowGit, kind: stepSilent, doing: "git config --global", run: func() error {
			mark, text = applyGitIdentity(hostGitStage(c.v().card(), c.rep.gitBlock(), c.rep.noTerminal(), id))
			return nil
		}, outcome: func() (choiceui.Mark, string) { return mark, text }}
		if !id.held {
			s.name = actGit
		}
		steps = append(steps, s)
	}
	if d.Gh {
		gh := ghSaid{mark: choiceui.MarkDone, text: "signed in"}
		steps = append(steps, applyStep{name: actGitHub, label: rowGh, kind: stepSilent, doing: "gh auth status", run: func() error {
			tty := c.v().card()
			auth := hostGhAuth()
			auth.login = func() error {
				return c.v().Hand(command("gh", "auth", "login"), []string{"gh's screen"}, said(choiceui.MarkDone, "signed in to GitHub"))
			}
			return applyGitHub(ghStage{
				auth: auth,
				host: c.host,
				run: func(argv []string) error {
					return c.v().Hand(command(argv[0], argv[1:]...), []string{whose(argv)}, said(choiceui.MarkDone, "gh installed"))
				},
				secretSet:  func(name, value string) error { return secretSetAt(bin(), name, value) },
				tty:        tty,
				out:        c.rep.gitBlock(),
				noTerminal: c.rep.noTerminal(),
				view:       c.view,
				said:       &gh,
				access: func() error {
					return applyGitHubAccess(accessStage{bin: bin(), prompt: tty, out: c.rep.gitBlock(), noTerminal: c.rep.noTerminal(),
						ask: c.sub, said: &gh, restarted: c.rep.installedNow})
				},
			})
		}, outcome: func() (choiceui.Mark, string) { return gh.mark, gh.text }})
	}
	if c.rep != nil && (d.Git || d.Gh) {
		steps = append(steps, applyStep{run: func() error { c.rep.printGit(); return nil }})
	}
	if d.Prompt {
		mark, text := choiceui.MarkDone, "installed"
		steps = append(steps, applyStep{name: actPrompt, label: rowPrompt, kind: stepSilent, doing: "starship, a Nerd Font, the segment",
			run: func() error {
				var draws func() (bool, bool)
				if c.v().card() {
					draws = c.drawsGlyphs
				}
				mark, text = applyPromptKit(draws, c.rep)
				return nil
			}, outcome: func() (choiceui.Mark, string) { return mark, text }})
	}
	return steps
}

// subFrame is what a sub-page shares with the page it stands over: its place and the page's done
// and ahead rows.
type subFrame struct {
	at          choiceui.Step
	done, ahead []choiceui.Row
}

// sub asks a sub-page over the running page, framed as that page: title, header and rows are the
// sub-page's own, enter says what its Enter does. It returns the form as answered.
func (c applyCtx) sub(title string, header []string, rows []choiceui.Row, enter func(*choiceui.Form) string) (*choiceui.Form, choiceui.Outcome, error) {
	f := subPage(c.frame.at, title, header, rows, c.frame.done, c.frame.ahead)
	f.EnterWord = enter
	out, err := c.v().Sub(f)
	return f, out, err
}

// drawsGlyphs asks the glyph question as a dialog over the card.
func (c applyCtx) drawsGlyphs() (icon, asked bool) {
	answer, err := c.v().Dialog(choiceui.DrawsGlyphsDialog())
	return err == nil && answer == 1, err == nil
}

// whose names the screen a hand-off lends: "brew's screen".
func whose(argv []string) string {
	name := filepath.Base(argv[0])
	if name == "sudo" && len(argv) > 1 {
		name = filepath.Base(argv[1])
	}
	return name + "'s screen"
}

// loginStage is what the Docker login step reads and runs.
type loginStage struct {
	auth    func() sbx.Auth // what sbx diagnose says of the sign-in before the login
	confirm func() sbx.Auth // what it says after
	login   func() error    // `sbx login`, framed
	tty     bool
	rep     *initReport
}

// applyLogin hands `sbx login` the terminal only while sbx diagnose says Docker is signed out,
// then says in the sbx block what a second diagnose found.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func applyLogin(l loginStage) error {
	switch l.auth() {
	case sbx.AuthIn:
		return nil
	case sbx.AuthUnknown:
		l.rep.sbxItem(sandbox.Item{Text: rowLogin + " — not checked", Detail: []string{"sbx diagnose gave no answer; `" + sbx.Binary + " login` signs in"}})
		return nil
	}
	if !l.tty {
		return nil
	}
	if err := l.login(); err != nil {
		return err
	}
	it := sandbox.Item{Ok: true, Text: rowLogin + " — signed in"}
	if l.confirm() != sbx.AuthIn {
		it = sandbox.Item{Warn: true, Text: rowLogin + " — not signed in", Detail: []string{loginFix}}
		l.rep.warnLogin()
	}
	l.rep.sbxItem(it)
	return nil
}

// installPlace says where the release lands: into its prefix, or wherever its own installer
// puts it when there is none.
func installPlace(plan sbx.Plan, screen bool) string {
	switch {
	case plan.Prefix != "" && screen:
		return "into " + ui.Tilde(plan.Prefix)
	case plan.Prefix != "":
		return "into " + plan.Prefix
	case len(plan.Steps) > 0 && len(plan.Steps[0].Argv) > 0:
		return "with " + filepath.Base(plan.Steps[0].Argv[0])
	}
	return "with its installer"
}

// stepNames are the listed steps of plan, in order.
func stepNames(plan []applyStep) []string {
	var out []string
	for _, s := range plan {
		if s.name != "" {
			out = append(out, s.name)
		}
	}
	return out
}

// runApply runs plan in order and stops at the first step that fails; ran hears the label of
// each step that did not fail. On the card the planned rows run in place: idle names the rows
// Enter leaves as they are, and a Ctrl+C stops the run after the row it came on. Off the card a
// line that stays on screen carries no place: a live step keeps its line in plain output, a hand
// step prints it before it gives the terminal away.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (RUN IN PLACE)
func runApply(view walkView, plan []applyStep, ran func(label string), idle []labelled) error {
	view = viewOr(view)
	if !view.card() {
		return runSteps(plan, ran)
	}
	view.Plan(planRows(plan))
	for _, s := range idle {
		view.Status(s.label, s.mark, s.outcome)
	}
	started, last := map[string]bool{}, ""
	for i, s := range plan {
		row := s.cardRow()
		if row != "" && last != "" && last != row && view.Context().Err() != nil {
			return stopped(view, plan, i, last)
		}
		if row != "" && (!started[row] || s.doing != "") {
			started[row], last = true, row
			view.Status(row, choiceui.MarkRunning, s.doing)
			if s.help != "" {
				help := s.help
				view.Edit(func(f *choiceui.Form) {
					if f.Progress != nil {
						f.Progress.Help = help
					}
				})
			}
		}
		if err := s.run(); err != nil {
			if view.Context().Err() != nil && !interrupted(err) {
				err = handoffErr{err: err, interrupted: true}
			}
			if row != "" {
				mark, text := failedMark(err)
				view.Status(row, mark, text)
			}
			notRun(view, plan[i+1:], row)
			return stepErr{label: s.label, rest: labelsAfter(plan, i), err: err}
		}
		if row != "" && !slices.ContainsFunc(plan[i+1:], func(n applyStep) bool { return n.cardRow() == row }) {
			mark, text := choiceui.MarkDone, ""
			if s.outcome != nil {
				mark, text = s.outcome()
			}
			view.Status(row, mark, text)
		}
		if ran != nil {
			ran(s.label)
		}
	}
	if last != "" && view.Context().Err() != nil {
		return stopped(view, plan, len(plan), last)
	}
	return nil
}

// stopped ends a run Ctrl+C stopped after row at: the rows from step i on read not run.
func stopped(view walkView, plan []applyStep, i int, at string) error {
	notRun(view, plan[i:], at)
	label := at
	for _, s := range plan[:i] {
		if s.cardRow() == at {
			label = s.label
		}
	}
	return stepErr{label: label, rest: labelsAfter(plan, i-1), err: handoffErr{err: context.Canceled, interrupted: true}}
}

// notRun marks each listed row of rest but skip as not run.
func notRun(view walkView, rest []applyStep, skip string) {
	for _, r := range planRows(rest) {
		if r != skip {
			view.Status(r, choiceui.MarkLater, "not run")
		}
	}
}

// labelsAfter are the labels of the steps after step i, without step i's own.
func labelsAfter(plan []applyStep, i int) []string {
	own := ""
	if i >= 0 && i < len(plan) {
		own = plan[i].label
	}
	var rest []string
	for _, r := range plan[i+1:] {
		if r.label != "" && r.label != own && !slices.Contains(rest, r.label) {
			rest = append(rest, r.label)
		}
	}
	return rest
}

// runSteps is runApply off the card: each step's line as its kind shows it.
func runSteps(plan []applyStep, ran func(label string)) error {
	steps := ui.NewSteps(len(stepNames(plan)))
	for i, s := range plan {
		var err error
		switch {
		case s.name == "":
			err = s.run()
		case s.kind == stepHand:
			err = steps.HandAs(s.name, s.line, s.run)
		case s.kind == stepQuiet:
			err = steps.Quiet(s.name, s.run)
		case s.kind == stepSilent:
			err = steps.Silent(s.name, s.run)
		default:
			err = steps.DoAs(s.name, cmp.Or(s.line, s.name), s.run)
		}
		if err != nil {
			return stepErr{label: s.label, rest: labelsAfter(plan, i), err: err}
		}
		if ran != nil {
			ran(s.label)
		}
	}
	return nil
}

// runPackage hands the downloaded package to the distro's package manager.
func runPackage(p sbx.Packaged, download, sum string, c applyCtx) error {
	if !c.v().card() {
		ui.Detailf("sudo may ask for your password")
	}
	cmd := command(p.Argv[0], p.Argv[1:]...)
	cmd.Dir = download
	if err := c.v().Hand(cmd, []string{whose(p.Argv)}, said(choiceui.MarkDone, "installed "+sbx.Release)); err != nil {
		return err
	}
	c.rep.installedNow()
	c.rep.sbxItem(sandbox.Item{Ok: true, Text: "distro package — installed",
		Detail: []string{"sha256 " + sum, "no published digest covers this asset"}})
	return nil
}

// fetchRelease downloads the release asset and holds it to its published digest; verified says
// whether one covered it.
func fetchRelease(ctx context.Context, plan sbx.Plan, download string) (sum string, verified bool, err error) {
	if err := prefixWritable(plan.Prefix); err != nil {
		return "", false, err
	}
	if sum, err = fetch(ctx, plan.URL, filepath.Join(download, plan.Asset)); err != nil {
		return "", false, err
	}
	if plan.Provenance == nil {
		return sum, false, nil
	}
	want, err := publishedDigest(ctx, *plan.Provenance)
	if err != nil {
		return "", false, err
	}
	if sum != want {
		return "", false, fmt.Errorf("%s does not match the digest published for %s: got %s, want %s",
			plan.Asset, plan.Provenance.Subject, sum, want)
	}
	return sum, true, nil
}

// placeRelease runs the release's own install steps with their output kept, checks sbx landed,
// and says so in the sbx block. Where the "sbx:" fact names the version and the bin, the ✓ names
// neither; it names the version a prefix held before. Windows' installer keeps the terminal.
func placeRelease(plan sbx.Plan, download, sum string, verified bool, c applyCtx) error {
	var replaced string
	if _, err := os.Stat(plan.Bin); plan.Bin != "" && err == nil {
		replaced, _ = sbx.VersionAt(plan.Bin)
	}
	for _, s := range plan.Steps {
		cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
		cmd.Dir = cmp.Or(s.Dir, download)
		cmd.Env = append(os.Environ(), s.Env...)
		run := runCaptured
		if runtime.GOOS == "windows" {
			run = func(cmd *exec.Cmd) error {
				hand := command(cmd.Args[0], cmd.Args[1:]...)
				hand.Dir, hand.Env, hand.Stdin = cmd.Dir, cmd.Env, nil
				return c.v().Hand(hand, []string{whose(cmd.Args)}, nil)
			}
		}
		if err := run(cmd); err != nil {
			return fmt.Errorf("%s (%s): %w", s.What, strings.Join(s.Argv, " "), err)
		}
	}
	it := sandbox.Item{Ok: true, Text: "sbx — installed"}
	if plan.Bin != "" {
		if _, err := os.Stat(plan.Bin); err != nil {
			return fmt.Errorf("the installer reported success, but %s is not there: %w", plan.Bin, err)
		}
	} else {
		it.Text = "sbx " + sbx.Release + " — installed"
		if note, _, _ := strings.Cut(plan.Note, ";"); note != "" {
			it.Detail = append(it.Detail, note)
		}
	}
	digest := "no published digest covers this asset"
	if verified {
		digest = "matches the release's provenance"
	}
	it.Detail = append(it.Detail, "sha256 "+sum, digest)
	if replaced != "" {
		it.Detail = append(it.Detail, "replaced "+replaced)
	}
	c.rep.installedNow()
	c.rep.sbxItem(it)
	return nil
}
