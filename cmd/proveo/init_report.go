// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// askedHow is how init got its answers.
type askedHow int

const (
	askedOnPages    askedHow = iota // its pages asked the operator
	askedByFlag                     // --yes or --print: the operator took the defaults
	askedNoTerminal                 // no terminal to ask through
)

// askedFrom is how init gets its answers under o.
func askedFrom(o initOptions) askedHow {
	switch {
	case o.yes || o.printOnly:
		return askedByFlag
	case !interactiveTerminal():
		return askedNoTerminal
	}
	return askedOnPages
}

// howTo is line as the one dim how-to a question init could not ask keeps; nil when the operator answered it.
func (a askedHow) howTo(line string) []string {
	if a != askedNoTerminal {
		return nil
	}
	return []string{line}
}

// Labels the result gives what has no row on pages 1 and 2.
const (
	labelCredentials = "credentials"
	labelOperator    = "operator"
	labelVerify      = "sbx diagnose"
)

// networkProbe is a host no baseline allows, so sbx's answer for it is its global policy.
const networkProbe = "proveo-egress-probe.invalid"

// initReport is what `proveo init` learns as it walks: what each step said of its row, what sbx
// holds, the operator's name and the host's readiness. close prints it once, as the closed checklist.
type initReport struct {
	host    sbx.Host
	prereqs []sbx.Prereq
	asked   askedHow
	print   bool              // --print: host paths stay absolute
	flags   map[string]string // the flag that left each row for later

	applied    sbx.Plan                  // the plan the latest visit of page 1 acts on
	installed  string                    // the sbx version on the host after the latest step; "" when none
	unverified bool                      // a release this init installed had no published digest
	about      map[string][]sandbox.Item // what each row's steps said of it, by the row's label
	blocks     map[string]*sandbox.Block // the git identity's and GitHub's items, by the row's label

	creds    *credsHeld     // what page 3 left; nil until it set or read the store
	operator *operatorSaid  // the name agents use; nil until page 4 set or read it
	opItems  []sandbox.Item // what reading or saving the name raised

	readiness []sandbox.Item // the latest verify's items
	ready     bool           // the latest verify found nothing a run needs failing

	auth        sbx.Auth    // what the latest sbx diagnose said of the Docker sign-in
	diag        []sbx.Check // the latest sbx diagnose's rows
	diagErr     error       // why the latest sbx diagnose gave no rows
	diagnosed   bool        // an sbx diagnose ran in this init
	stale       bool        // a step since the latest diagnose changed what sbx would say: an install, a daemon restart
	judged      bool        // a verify set the readiness
	loginWarned bool        // the login step said that Docker is still signed out
	wizard      bool        // page 4 answered sbx setup "after init"
	handed      bool        // a hand-off on the card printed above the result
	skipSetup   bool        // --skip-setup
	probe       func() (sbx.SetupOffer, error)
	offer       *sbx.SetupOffer // what the probe found before page 4; nil until it ran
}

func newInitReport(o initOptions, host sbx.Host, prereqs []sbx.Prereq, plan sbx.Plan) *initReport {
	r := &initReport{host: host, prereqs: prereqs, asked: askedFrom(o), print: o.printOnly, applied: plan, skipSetup: o.skipSetup,
		flags: map[string]string{}}
	for label, set := range map[string]struct {
		on   bool
		flag string
	}{rowLogin: {o.skipLogin, "--skip-login"}, rowGh: {o.skipGh, "--skip-gh"}, rowGit: {o.skipGit, "--skip-git"},
		rowPrompt: {o.skipPrompt, "--skip-prompt"}, rowWizard: {o.skipSetup, "--skip-setup"}} {
		if set.on {
			r.flags[label] = set.flag
		}
	}
	r.probe = func() (sbx.SetupOffer, error) { return sbx.SetupOfferAt(resolveBin(r.applied), os.Getenv) }
	return r
}

// heard keeps what an sbx diagnose said: its rows, and the Docker sign-in.
func (r *initReport) heard(checks []sbx.Check, err error) {
	if r != nil {
		r.auth, r.diagnosed, r.stale = sbx.DockerAuth(checks, err), true, false
		r.diag, r.diagErr = checks, err
	}
}

// installedNow records that a step changed the sbx a diagnose would read.
func (r *initReport) installedNow() {
	if r != nil {
		r.stale = true
	}
}

// diagnose asks the sbx at bin for its verdict, keeps it, and judges the host again by it once a
// verify judged it.
func (r *initReport) diagnose(bin string) sbx.Auth {
	checks, err := sbx.DiagnoseAt(bin)
	r.heard(checks, err)
	if r != nil && r.judged {
		_ = verifyWith(bin, r, checks, err)
	}
	return sbx.DockerAuth(checks, err)
}

// probeSetup runs sbx setup's probe and keeps what it found for page 4 and the result.
func (r *initReport) probeSetup() sbx.SetupOffer {
	offer, _ := r.probe()
	r.offer = &offer
	return offer
}

// mootRows are the rows the latest diagnose left nothing to do for.
func (r *initReport) mootRows() []string {
	if r != nil && r.auth == sbx.AuthIn {
		return []string{rowLogin}
	}
	return nil
}

// warnLogin records that the login step said Docker is still signed out.
func (r *initReport) warnLogin() {
	if r != nil {
		r.loginWarned = true
	}
}

// namesLogin reports whether it is a Docker login item.
func namesLogin(it sandbox.Item) bool { return strings.HasPrefix(it.Text, rowLogin+" — ") }

// path writes a host path for the screen: home-relative, or absolute under --print.
func (r *initReport) path(p string) string {
	if r != nil && r.print {
		return p
	}
	return ui.Tilde(p)
}

// howTo is the one dim how-to a question init could not ask keeps; nil on a nil report.
func (r *initReport) howTo(line string) []string {
	if r == nil {
		return nil
	}
	return r.asked.howTo(line)
}

// noTerminal reports whether init had no terminal to ask through.
func (r *initReport) noTerminal() bool { return r != nil && r.asked == askedNoTerminal }

// say keeps what a step found about row label; a zero item says nothing.
func (r *initReport) say(label string, items ...sandbox.Item) {
	if r == nil {
		return
	}
	if r.about == nil {
		r.about = map[string][]sandbox.Item{}
	}
	for _, it := range items {
		if it.Text != "" {
			r.about[label] = append(r.about[label], it)
		}
	}
}

// block is row label's block, for a step that gathers facts and items; nil on a nil report.
func (r *initReport) block(label string) *sandbox.Block {
	if r == nil {
		return nil
	}
	if r.blocks == nil {
		r.blocks = map[string]*sandbox.Block{}
	}
	if r.blocks[label] == nil {
		r.blocks[label] = sandbox.NewBlock(func() {})
	}
	return r.blocks[label]
}

// itemsAbout is what row label's steps said of it, its block's items included.
func (r *initReport) itemsAbout(label string) []sandbox.Item {
	if b := r.blocks[label]; b != nil {
		_, its := b.Take()
		r.say(label, its...)
	}
	return slices.Clone(r.about[label])
}

// printSbx prints --print's sbx block: the host and the sbx it runs as facts, then the unmet
// prerequisites and the version's warnings.
func (r *initReport) printSbx() {
	version, _ := installedVersion(r.applied)
	items := slices.Concat(prereqItems(r.prereqs), versionItems(version), r.shadowItems())
	sandbox.PrintBlock(sandbox.SbxHeading, r.sbxFacts(version), items)
}

// sbxFacts are the host with the prerequisites it meets, the sbx it runs and where, and sbx's network policy.
func (r *initReport) sbxFacts(version string) []ui.Fact {
	host := ui.Fact{Role: ui.RoleHost, Key: "host", Value: describeHost(r.host)}
	if met := metPrereqs(r.prereqs); len(met) > 0 {
		host.Detail = []string{"prerequisites met: " + strings.Join(met, ", ")}
	}
	facts := []ui.Fact{host}
	if version != "" {
		f := ui.Fact{Role: ui.RoleApp, Key: "sbx", Value: version}
		if at := sbxAt(r.applied); at != "" {
			f.Detail = []string{r.path(at)}
		}
		facts = append(facts, f)
	}
	if !r.print {
		switch allowed, known := sbx.NetworkAllowed(networkProbe); {
		case known && allowed:
			facts = append(facts, ui.Fact{Role: ui.RoleCloud, Key: "network", Value: "sbx's global policy allows every host"})
		case known:
			facts = append(facts, ui.Fact{Role: ui.RoleCloud, Key: "network", Value: "sbx's global policy allows listed hosts only"})
		}
	}
	return facts
}

// sbxAt is where the sbx plan resolves to lives; "" when nothing resolves.
func sbxAt(plan sbx.Plan) string {
	if bin := resolveBin(plan); bin != sbx.Binary {
		return bin
	}
	at, err := exec.LookPath(sbx.Binary)
	if err != nil {
		return ""
	}
	return at
}

// shadowItems warn when this shell's PATH finds an sbx other than the prefix's, which a run here uses.
func (r *initReport) shadowItems() []sandbox.Item {
	at := resolveBin(r.applied)
	if at == sbx.Binary {
		return nil
	}
	first, err := exec.LookPath(sbx.Binary)
	if err != nil || sameFile(first, at) {
		return nil
	}
	it := sandbox.Item{Warn: true, Text: "sbx — this shell's PATH finds " + r.path(first)}
	if v, err := sbx.VersionAt(first); err == nil {
		it.Detail = append(it.Detail, "sbx "+v)
	}
	it.Detail = append(it.Detail, "a run in this shell uses it")
	return []sandbox.Item{it}
}

// sameFile reports whether a and b name one file.
func sameFile(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// metPrereqs names the prerequisites the host meets.
func metPrereqs(checks []sbx.Prereq) []string {
	var out []string
	for _, c := range checks {
		if c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// prereqItems is one item per unmet prerequisite: × when it blocks the install, ⚠ otherwise,
// with what it looked for and its fix dim.
func prereqItems(checks []sbx.Prereq) []sandbox.Item {
	var out []sandbox.Item
	for _, c := range checks {
		if c.OK {
			continue
		}
		blocks := c.Blocks == sbx.BlocksInstall
		it := sandbox.Item{Fail: blocks, Warn: !blocks, Text: c.Name + " — not met"}
		if head, _, _ := strings.Cut(c.Detail, " — "); head != "" {
			it.Detail = append(it.Detail, head)
		}
		if c.Fix != "" {
			it.Detail = append(it.Detail, c.Fix)
		}
		out = append(out, it)
	}
	return out
}

// versionItems warn about an installed sbx proveo has not verified, or one a run refuses.
func versionItems(version string) []sandbox.Item {
	var out []sandbox.Item
	if sbx.UnverifiedNote(version) != "" {
		out = append(out, sandbox.Item{Warn: true, Text: "sbx " + version + " — not a version proveo has verified",
			Detail: []string{"verified series: " + strings.Join(sbx.Verified, ", ")}})
	}
	if version != "" && sbx.Older(version, sbx.MinVersion) {
		out = append(out, sandbox.Item{Warn: true, Text: "sbx " + version + " — older than a run accepts",
			Detail: []string{"a run needs " + sbx.MinVersion + " or later", "`proveo init --force` installs the pinned " + sbx.Release}})
	}
	return out
}

func credentialsHeading() { ui.Section(ui.SectionCredentials) }

func operatorHeading() { ui.Section(ui.SectionOperator) }

// setCredentials keeps what page 3 left, replacing an earlier pass.
func (r *initReport) setCredentials(c credsHeld) {
	if r != nil {
		r.creds = &c
	}
}

// operatorSaid is the name agents use, whether page 4 asked for it, and whether it is the default
// nothing stored overrides.
type operatorSaid struct {
	name      string
	asked     bool
	defaulted bool
	howTo     []string
}

// row is the operator's row in the result: ✓ once page 4 asked, – when the name stood as it was.
func (o operatorSaid) row() closedRow {
	row := closedRow{mark: choiceui.MarkLater, label: labelOperator, outcome: o.name, detail: o.howTo}
	if o.asked {
		row.mark = choiceui.MarkDone
	}
	if o.defaulted {
		row.outcome += " (default)"
	}
	return row
}

// setOperator records the name agents use.
func (r *initReport) setOperator(o operatorSaid) {
	if r != nil {
		r.operator = &o
	}
}

// operatorItem adds what reading or saving the name raised.
func (r *initReport) operatorItem(it sandbox.Item) {
	if r != nil {
		r.opItems = append(r.opItems, it)
	}
}

// printCredentials prints --print's credentials block, then its operator block.
func (r *initReport) printCredentials() {
	if r.creds != nil {
		facts, _ := credentialFacts(r.creds.slots)
		sandbox.PrintBlock(credentialsHeading, facts, r.creds.items)
	}
	var facts []ui.Fact
	if r.operator != nil {
		facts = []ui.Fact{{Role: ui.RoleHost, Key: "name", Value: r.operator.name, Detail: r.operator.howTo}}
	}
	sandbox.PrintBlock(operatorHeading, facts, r.opItems)
}

// close prints the closed checklist once: its heading, a row per step in run order, the items that
// need the operator, sbx setup when the probe found work for it, then one closing line. It returns
// nil on success, and otherwise an error main prints nothing for.
// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml (THE CLOSED CHECKLIST)
func (r *initReport) close(rec *initRecord, err error) error {
	var blocked blockedErr
	if errors.As(err, &blocked) {
		r.heading()
		printNeeds(sandbox.WithPaths(prereqItems(r.prereqs), ui.TildeIn))
		return sandbox.Reported{Err: err}
	}
	if r.stale && r.judged && err == nil {
		_ = verify(resolveBin(r.applied), r)
	}
	if rec == nil {
		rec = &initRecord{}
	}
	rec.moot(r.mootRows()...)
	if slices.ContainsFunc(r.readiness, namesLogin) {
		rec.moot(rowLogin)
	}
	turn := r.planSetup(rec, err)
	rows, needs := r.checklist(rec, turn)
	closing, ret := r.ending(err, rows)
	r.heading()
	for _, row := range rows {
		printRow(row)
	}
	printNeeds(needs)
	if turn.runs && interrupted(r.runSetup(turn.offer, rec)) {
		ret = reraised{sandbox.Stopped{Signal: syscall.SIGINT}}
	}
	if closing != nil {
		ui.Blank()
		printEnding(*closing)
	}
	return ret
}

// heading is the closed checklist's first line, "── proveo init ── <os>/<arch> · sbx <v> ──…", then a
// blank row; a blank row sits above it only under what init printed before it.
func (r *initReport) heading() {
	parts := []string{r.host.OS + "/" + r.host.Arch}
	if r.installed != "" {
		parts = append(parts, "sbx "+r.installed)
	}
	if r.handed {
		ui.Frame(initBorder, parts...)
	} else {
		ui.Lead(initBorder, parts...)
	}
	ui.Blank()
}

// setupTurn is sbx setup's turn after the result: what the probe found, whether setup runs, and
// the result's row when it does not.
type setupTurn struct {
	offer sbx.SetupOffer
	runs  bool
	row   *closedRow
}

// planSetup probes what sbx setup would offer, unless a Ctrl+C ended init, and gives it its turn:
// it runs only on a terminal, after a walk that completed (err nil), when the page answered
// "after init" and the probe found work. rec lists it as later only when the probe found work.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func (r *initReport) planSetup(rec *initRecord, err error) setupTurn {
	completed := err == nil
	switch {
	case r.print || rec == nil:
		return setupTurn{}
	case r.skipSetup && completed:
		return setupTurn{row: &closedRow{mark: choiceui.MarkLater, label: rowWizard, outcome: "later · --skip-setup"}}
	case r.skipSetup:
		return setupTurn{}
	}
	var offer sbx.SetupOffer
	switch {
	case r.offer != nil:
		offer = *r.offer
	case !interrupted(err):
		offer, _ = r.probe()
	}
	switch {
	case offer.Known && offer.Offers() && completed && r.wizard && r.asked == askedOnPages:
		rec.moot(rowWizard)
		return setupTurn{offer: offer, runs: true}
	case offer.Known && offer.Offers():
		why := offer.Reason() + " · `" + sbx.Binary + " setup` on a terminal"
		rec.leave(rowWizard, why)
		return setupTurn{offer: offer, row: &closedRow{mark: choiceui.MarkLater, label: rowWizard, outcome: "later · " + why}}
	}
	rec.moot(rowWizard)
	switch {
	case !completed:
		return setupTurn{offer: offer}
	case offer.Known:
		return setupTurn{offer: offer, row: &closedRow{mark: choiceui.MarkLater, label: rowWizard, outcome: "nothing to import"}}
	}
	return setupTurn{offer: offer, row: &closedRow{mark: choiceui.MarkLater, label: rowWizard, outcome: "not checked",
		detail: []string{"`" + sbx.Binary + " setup` opens it"}}}
}

// runSetup hands `sbx setup` the terminal under its frame, then prints its outcome row.
func (r *initReport) runSetup(offer sbx.SetupOffer, rec *initRecord) error {
	err := handFramed(command(resolveBin(r.applied), sbx.SetupArgs()...), offer.Reason(), "sbx's screen", "init ends when it exits")
	row := closedRow{mark: choiceui.MarkDone, label: rowWizard, outcome: "finished"}
	switch {
	case err == nil:
		rec.did(rowWizard)
	case interrupted(err):
		row = closedRow{mark: choiceui.MarkNeeds, label: rowWizard, outcome: "stopped · Ctrl+C ended it"}
	default:
		row = closedRow{mark: choiceui.MarkNeeds, label: rowWizard, outcome: "did not complete · `" + sbx.Binary + " setup` by hand",
			detail: errDetail(err)}
	}
	printRow(row)
	return err
}

// ending is the result's closing line for err, and what doInit returns. A failure a row of rows
// already names keeps its error off the closing line. Every ending but success keeps the verdict a
// finished verify reached as its last dim line.
// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml (THE CLOSED CHECKLIST)
func (r *initReport) ending(err error, rows []closedRow) (*closedRow, error) {
	var ready []string
	if r.ready {
		ready = []string{"host ready to run"}
	}
	named := func(label string) bool {
		return slices.ContainsFunc(rows, func(row closedRow) bool { return row.label == label && row.mark == choiceui.MarkFailed })
	}
	var notReady hostNotReady
	var hand handoffErr
	var step stepErr
	switch {
	case err == nil && r.ready:
		return &closedRow{mark: choiceui.MarkDone, label: "host — ready to run"}, nil
	case err == nil:
		return &closedRow{mark: choiceui.MarkNeeds, label: "host — readiness not checked"}, nil
	case errors.As(err, &notReady):
		return &closedRow{mark: choiceui.MarkFailed, label: "host — not ready to run"}, sandbox.Reported{Err: err}
	case errors.Is(err, errInitLeft):
		return &closedRow{mark: choiceui.MarkNeeds, label: "init — " + err.Error(), detail: ready}, sandbox.Reported{Err: err}
	case errors.As(err, &step) && errors.As(err, &hand):
		it := &closedRow{mark: choiceui.MarkNeeds, label: "init — stopped at " + cmpLabel(step.label), detail: ready}
		if hand.interrupted {
			it.detail = append([]string{"Ctrl+C ended it"}, ready...)
			return it, reraised{sandbox.Stopped{Signal: syscall.SIGINT}}
		}
		return it, sandbox.Reported{Err: err}
	case errors.As(err, &step):
		it := &closedRow{mark: choiceui.MarkFailed, label: "init — stopped at " + cmpLabel(step.label), detail: ready}
		if !named(step.label) {
			it.detail = append(errLines(step.err), ready...)
		}
		return it, sandbox.Reported{Err: err}
	}
	return &closedRow{mark: choiceui.MarkFailed, label: "init — stopped", detail: append(errLines(err), ready...)}, sandbox.Reported{Err: err}
}

// cmpLabel is a step's label, or what stands for an unlabelled one.
func cmpLabel(label string) string {
	if label == "" {
		return "setup"
	}
	return label
}

// errLines is err as dim detail: each line, split at " — ", with home paths as "~/".
func errLines(err error) []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(err.Error()), "\n") {
		for part := range strings.SplitSeq(line, " — ") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, ui.TildeIn(p))
			}
		}
	}
	return out
}

// errDetail is errLines for an error proveo found; nil for a program that printed its own.
func errDetail(err error) []string {
	var hand handoffErr
	if errors.As(err, &hand) {
		return nil
	}
	return errLines(err)
}

// blockedErr refuses an install on a host that fails a prerequisite.
type blockedErr struct{ stop []sbx.Prereq }

func (e blockedErr) Error() string {
	return "nothing good follows an install here: " + strings.Join(sbx.Names(e.stop), ", ")
}

// hostNotReady is verify's verdict when a check a run needs fails.
type hostNotReady struct{ names []string }

func (e hostNotReady) Error() string { return "host not ready to run: " + strings.Join(e.names, ", ") }

// handoffErr is a program init gave the terminal to that ended in an error it printed itself.
type handoffErr struct {
	err         error
	interrupted bool // a Ctrl+C ended it
}

func (e handoffErr) Error() string { return e.err.Error() }

func (e handoffErr) Unwrap() error { return e.err }

// interrupted reports whether a Ctrl+C ended the program err came from.
func interrupted(err error) bool {
	var hand handoffErr
	return errors.As(err, &hand) && hand.interrupted
}

// stepErr is the apply step that stopped init, and the chosen steps after it that never ran.
type stepErr struct {
	label string
	rest  []string
	err   error
}

func (e stepErr) Error() string { return e.err.Error() }

func (e stepErr) Unwrap() error { return e.err }

// handOff gives the terminal to c until it exits. A Ctrl+C reaches c and not init; when c then
// fails, the error says so, and init stops after it.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
func handOff(c *exec.Cmd) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	err := c.Run()
	signal.Stop(sigs)
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) {
		return err
	}
	hand := handoffErr{err: err, interrupted: len(sigs) > 0 || endedByInterrupt(exit)}
	if tty, ok := c.Stderr.(*os.File); ok && hand.interrupted && agentio.IsWriterTTY(tty) {
		_, _ = tty.WriteString("\n") // ends the line the terminal's ^C echo left open
	}
	return hand
}

// endedByInterrupt reports whether SIGINT killed the program, or it exited with the shell's 130 for one.
func endedByInterrupt(exit *exec.ExitError) bool {
	if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGINT {
		return true
	}
	return exit.ExitCode() == 128+int(syscall.SIGINT)
}

// handFramed gives the terminal to c under its frame line, and leaves a blank row under what c printed.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func handFramed(c *exec.Cmd, parts ...string) error { return handFramedAs(commandLine(c), c, parts...) }

// handFramedAs is handFramed under a frame line that names shown.
func handFramedAs(shown string, c *exec.Cmd, parts ...string) error {
	ui.Frame(shown, parts...)
	err := handOff(c)
	ui.Blank()
	return err
}

// command is argv with the terminal: init's stdin, and stderr for both of its outputs.
func command(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	return c
}

// stopAt wraps a page's failure as the step that stopped init.
func stopAt(label string, err error) error {
	if err == nil {
		return nil
	}
	return stepErr{label: label, err: err}
}
