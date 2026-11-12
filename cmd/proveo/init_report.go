// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml, _spec/_plans/output-readability.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
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

// Labels the result gives what has no row on the setup page.
const (
	labelCredentials = "credentials"
	labelOperator    = "operator name"
	labelVerify      = "sbx diagnose"
)

// networkProbe is a host no baseline allows, so sbx's answer for it is its global policy.
const networkProbe = "proveo-egress-probe.invalid"

// initReport is what `proveo init` prints, by subject. Each block prints once, after the step
// that makes it true; close prints what an early end left unprinted, then the result.
type initReport struct {
	host    sbx.Host
	prereqs []sbx.Prereq
	asked   askedHow
	print   bool // --print: host paths stay absolute

	applied  sbx.Plan       // the plan the latest visit of setup acts on
	sbxItems []sandbox.Item // what this visit's sbx steps found
	sbxOpen  bool           // a visit declared "sbx" and has not printed its block
	sbxSaid  bool           // an sbx block printed in this init

	git *sandbox.Block // the identity and GitHub; nil until a git step runs

	credsSet  bool           // the credentials page passed, or nothing asked: its block prints at the end
	credFacts []ui.Fact      // what sbx's store holds after the latest pass
	credItems []sandbox.Item // its warnings
	operator  *ui.Fact       // the name agents use; nil until the operator page set or read it
	opItems   []sandbox.Item

	readiness []sandbox.Item // the latest verify's items
	ready     bool           // the latest verify found nothing a run needs failing

	said map[string]bool // each fact and item a subject printed, so a revisit prints only what changed
}

func newInitReport(o initOptions, host sbx.Host, prereqs []sbx.Prereq, plan sbx.Plan) *initReport {
	return &initReport{host: host, prereqs: prereqs, asked: askedFrom(o), print: o.printOnly, applied: plan}
}

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

// openSbx declares the sbx heading above a visit's sbx steps.
func (r *initReport) openSbx() {
	r.sbxOpen = true
	ui.Section(ui.SectionSbx)
}

// sbxItem adds what an sbx step found to the visit's sbx block; a zero item adds nothing.
func (r *initReport) sbxItem(it sandbox.Item) {
	if it.Text == "" {
		return
	}
	if r == nil {
		sandbox.PrintBlock(sandbox.SbxHeading, nil, []sandbox.Item{it})
		return
	}
	r.sbxItems = append(r.sbxItems, it)
}

// printSbx prints the sbx block: the host and the sbx it runs as facts, then the unmet
// prerequisites, the version's warnings and what the visit's steps found. A revisit prints only
// what changed.
func (r *initReport) printSbx() {
	version, _ := installedVersion(r.applied)
	items := slices.Concat(prereqItems(r.prereqs), versionItems(version), r.shadowItems(), r.sbxItems)
	r.printFresh(sandbox.SbxHeading, ui.SectionSbx, r.sbxFacts(version), items)
	r.sbxItems, r.sbxOpen, r.sbxSaid = nil, false, true
}

// printFresh prints a subject's block without what it printed before: a keyed fact with the rows
// stacked under it, and an item, each counts as one.
func (r *initReport) printFresh(heading func(), subject string, facts []ui.Fact, items []sandbox.Item) {
	if r == nil {
		sandbox.PrintBlock(heading, facts, items)
		return
	}
	if r.said == nil {
		r.said = map[string]bool{}
	}
	fresh := func(v any) bool {
		k := subject + "\x00" + fmt.Sprintf("%#v", v)
		if r.said[k] {
			return false
		}
		r.said[k] = true
		return true
	}
	var fs []ui.Fact
	for i := 0; i < len(facts); {
		j := i + 1
		for j < len(facts) && facts[j].Key == "" {
			j++
		}
		if fresh(facts[i:j]) {
			fs = append(fs, facts[i:j]...)
		}
		i = j
	}
	var its []sandbox.Item
	for _, it := range items {
		if fresh(it) {
			its = append(its, it)
		}
	}
	if len(fs) == 0 && len(its) == 0 {
		ui.WithdrawSection()
		return
	}
	sandbox.PrintBlock(heading, fs, its)
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

func gitHeading() { ui.Section(ui.SectionGit) }

func credentialsHeading() { ui.Section(ui.SectionCredentials) }

func operatorHeading() { ui.Section(ui.SectionOperator) }

func promptHeading() { ui.Section(ui.SectionPrompt) }

// openGit starts the visit's git block and declares its heading above the git steps.
func (r *initReport) openGit() {
	r.git = sandbox.NewBlock(gitHeading)
	ui.Section(ui.SectionGit)
}

// printGit prints the visit's git block, without what an earlier visit printed.
func (r *initReport) printGit() {
	facts, items := r.git.Take()
	r.printFresh(gitHeading, ui.SectionGit, facts, items)
}

// gitBlock is the visit's git block; nil prints each row at once.
func (r *initReport) gitBlock() *sandbox.Block {
	if r == nil {
		return nil
	}
	return r.git
}

// setCredentials replaces the credentials block with what the store holds now.
func (r *initReport) setCredentials(facts []ui.Fact, items []sandbox.Item) {
	if r == nil {
		return
	}
	r.credsSet, r.credFacts, r.credItems = true, facts, items
}

// setOperator records the name agents use, with the how-to an unasked init keeps.
func (r *initReport) setOperator(name string, howTo []string) {
	if r == nil {
		return
	}
	r.operator = &ui.Fact{Role: ui.RoleHost, Key: "name", Value: name, Detail: howTo}
}

// operatorItem adds a warning to the operator block.
func (r *initReport) operatorItem(it sandbox.Item) {
	if r == nil {
		sandbox.PrintBlock(operatorHeading, nil, []sandbox.Item{it})
		return
	}
	r.opItems = append(r.opItems, it)
}

// printCredentials prints the credentials block and then the operator block, once each.
func (r *initReport) printCredentials() {
	if r.credsSet {
		sandbox.PrintBlock(credentialsHeading, r.credFacts, r.credItems)
		r.credsSet = false
	}
	var facts []ui.Fact
	if r.operator != nil {
		facts = []ui.Fact{*r.operator}
	}
	sandbox.PrintBlock(operatorHeading, facts, r.opItems)
	r.operator, r.opItems = nil, nil
}

// close prints what init has not printed yet, subject by subject, then the result block: what
// waits, what never ran, the readiness items and one closing line. It returns nil on success,
// and otherwise an error main prints nothing for.
func (r *initReport) close(rec *initRecord, err error) error {
	var blocked blockedErr
	if errors.As(err, &blocked) {
		if r.sbxOpen || !r.sbxSaid {
			r.printSbx()
		}
		return backend.Reported{Err: err}
	}
	if r.sbxOpen || !r.sbxSaid {
		r.printSbx()
	}
	r.printGit()
	r.printCredentials()
	items := slices.Clone(r.readiness)
	closing, ret := r.ending(err)
	if closing != nil {
		items = append(items, *closing)
	}
	var facts []ui.Fact
	if rec != nil {
		facts = rec.facts()
	}
	sandbox.PrintBlock(sandbox.ResultsHeading, facts, items)
	return ret
}

// ending is the result's closing line for err, and what doInit returns. Every ending but success
// keeps the verdict a finished verify reached as its last dim line.
func (r *initReport) ending(err error) (*sandbox.Item, error) {
	var ready []string
	if r.ready {
		ready = []string{"host ready to run"}
	}
	var notReady hostNotReady
	var hand handoffErr
	var step stepErr
	switch {
	case err == nil && r.ready:
		return &sandbox.Item{Ok: true, Text: "host — ready to run"}, nil
	case err == nil:
		return nil, nil
	case errors.As(err, &notReady):
		return &sandbox.Item{Fail: true, Text: "host — not ready to run"}, backend.Reported{Err: err}
	case errors.Is(err, errInitLeft), errors.Is(err, errInitCancel):
		return &sandbox.Item{Warn: true, Text: "init — " + err.Error(), Detail: ready}, backend.Reported{Err: err}
	case errors.As(err, &step) && errors.As(err, &hand):
		it := &sandbox.Item{Warn: true, Text: "init — stopped at " + cmpLabel(step.label), Detail: ready}
		if hand.interrupted {
			it.Detail = append([]string{"Ctrl+C ended it"}, ready...)
			return it, reraised{backend.Stopped{Signal: syscall.SIGINT}}
		}
		return it, backend.Reported{Err: err}
	case errors.As(err, &step):
		return &sandbox.Item{Fail: true, Text: cmpLabel(step.label) + " — failed", Detail: append(errLines(step.err), ready...)}, backend.Reported{Err: err}
	}
	return &sandbox.Item{Fail: true, Text: "init — stopped", Detail: append(errLines(err), ready...)}, backend.Reported{Err: err}
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
	for _, line := range strings.Split(strings.TrimSpace(err.Error()), "\n") {
		for _, part := range strings.Split(line, " — ") {
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
	if hand.interrupted && agentio.IsWriterTTY(os.Stderr) {
		_, _ = os.Stderr.WriteString("\n") // ends the line the terminal's ^C echo left open
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
