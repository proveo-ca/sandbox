// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/internal/choiceui/wireframe.puml, _spec/internal/sbx/host-readiness.puml
package main

import (
	"os"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/sbx"
)

type initDecisions struct {
	Install  string // installTarball | installPackaged | installSkip
	Prefix   string
	AddPath  bool
	Login    bool // run `sbx login` while sbx diagnose says Docker is signed out
	Wizard   bool // open `sbx setup` after the result, while the probe finds work
	Gh       bool
	Git      bool
	Prompt   bool   // install the shell-prompt kit: starship, a Nerd Font, the segment
	Baseline string // "" leaves the host-wide network policy alone
	NoPrompt bool
	pathDone string // the PATH dir an earlier visit's step settled
}

const (
	installTarball  = "rootless tarball"
	installPackaged = "distro package"
	installSkip     = "keep what is installed"

	rowInstall  = "sbx install"
	rowPrefix   = "location"
	rowPath     = "PATH"
	rowLogin    = "Docker login"
	rowWizard   = "sbx setup"
	rowGh       = "GitHub"
	rowGit      = "git identity"
	rowPrompt   = "prompt kit"
	rowBaseline = "network"
	rowName     = "user.name"
	rowEmail    = "user.email"

	baselineLeave = "leave as is"
	prefixSystem  = "/usr/local"

	pathAddRC = "add to shell rc"
	pathPrint = "print only"

	loginNow       = "now"
	loginIfOut     = "if signed out"
	loginSignedIn  = "signed in"
	loginUnchecked = "not checked"

	setupAfter = "after init"
	setupLater = "later"

	// cellRunes is a checklist cell's width: "keep as is" and "if signed out" whole.
	cellRunes = 20
)

// prefixOptions are the two the release's own installer documents. /usr/local
// is leftmost (the system-wide choice); the home prefix stays the default
// (Selected). An explicit --prefix is the only option there is.
func prefixOptions(def, explicit string) []string {
	if strings.TrimSpace(explicit) != "" {
		return []string{def}
	}
	return []string{prefixSystem, sbx.DefaultPrefix(homeDir())}
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// defaultDecisions are the answers nobody gave; terminal says whether one is there to hand sbx login and sbx setup.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func defaultDecisions(plan sbx.Plan, installed string, o initOptions, terminal bool) initDecisions {
	d := initDecisions{
		Install:  installTarball,
		Prefix:   plan.Prefix,
		AddPath:  plan.Bin != "",
		Login:    !o.skipLogin && !o.yes && terminal,
		Wizard:   !o.skipSetup && !o.yes && terminal,
		Gh:       !o.skipGh,
		Git:      !o.skipGit,
		Prompt:   !o.skipPrompt,
		Baseline: "",
		NoPrompt: o.yes,
	}
	if installed != "" && !o.force {
		d.Install = installSkip
	}
	return d
}

// carried opens a revisit on what is left: the host as it is now, PATH and sbx setup as answered,
// every one-shot action of the apply later.
func carried(def initDecisions, prev *initDecisions) initDecisions {
	if prev == nil {
		return def
	}
	out := def
	out.AddPath, out.pathDone, out.Wizard = prev.AddPath, prev.pathDone, prev.Wizard
	out.Login, out.Gh, out.Git, out.Prompt, out.Baseline = false, false, false, false, ""
	return out
}

// plannedCtx is what an apply of d would act on, for naming its steps before any of them runs.
func plannedCtx(plan sbx.Plan, host sbx.Host, d initDecisions, viaPackage bool) applyCtx {
	if d.Prefix != plan.Prefix {
		if alt, err := sbx.PlanFor(host, d.Prefix, ""); err == nil {
			plan = alt
		}
	}
	via := d.Install == installPackaged || (d.Install == installSkip && viaPackage)
	return applyCtx{plan: plan, applied: appliedPlan(plan, via), host: host}
}

// answered is def with every answer the form holds.
func answered(form *choiceui.Form, def initDecisions) initDecisions {
	out := def
	if v := form.Selection(rowInstall); v != "" {
		out.Install = v
	}
	if v := form.Selection(rowPrefix); v != "" {
		out.Prefix = v
	}
	if v := form.Selection(rowPath); v != "" {
		out.AddPath = v == pathAddRC
	}
	if v := form.Selection(rowLogin); v != "" {
		out.Login = v == loginNow
	}
	if v := form.Selection(rowWizard); v != "" {
		out.Wizard = v == setupAfter
	}
	if v := form.Selection(rowGh); v != "" {
		out.Gh = v == "now"
	}
	if v := form.Selection(rowGit); v != "" {
		out.Git = v == "now"
	}
	if v := form.Selection(rowPrompt); v != "" {
		out.Prompt = v == "now"
	}
	if v := form.Selection(rowBaseline); v != "" && v != baselineLeave {
		out.Baseline = v
	}
	return out
}

// networkState is sbx's host-wide policy as page 1 shows it: the facts row's words, and whether
// the network row asks to bind a baseline.
type networkState struct {
	fact     string
	allowAll bool
}

// hostNetwork reads sbx's policy; an absent sbx has none to read.
func hostNetwork(installed bool) networkState {
	if !installed {
		return networkState{}
	}
	var n networkState
	if allowed, known := sbx.NetworkAllowed(networkProbe); known {
		n.fact = "listed hosts only"
		if allowed {
			n.fact = "every host"
		}
	}
	current, known := sbx.PolicyBaseline()
	n.allowAll = known && current == sbx.BaselineAllowAll
	return n
}

// sbxPageIn is what page 1 draws from: the host, its plan and sbx, and what was checked.
type sbxPageIn struct {
	host      sbx.Host
	plan      sbx.Plan
	installed string
	prereqs   []sbx.Prereq
	diag      []sbx.Check
	diagErr   error
	diagnosed bool
	auth      sbx.Auth
	network   networkState
	stop      []sbx.Prereq
}

// sbxForm is page 1, "Prepare sbx on this <os>/<arch> host": the checks, then sbx install with
// location and PATH under the tarball, Docker login and the network baseline, then what lies
// ahead. count is how many rows Enter runs for an answer; seen holds the rows that already ran.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD)
func sbxForm(in sbxPageIn, at choiceui.Step, def initDecisions, o initOptions, seen map[string]rowState, count func(initDecisions) int) *choiceui.Form {
	header := []string{"found: no sbx on this host"}
	if in.installed != "" {
		header = []string{"found: sbx " + in.installed}
		if in.network.fact != "" {
			header = append(header, "network: "+in.network.fact)
		}
	}
	rows := checkRows(in.prereqs, in.diag, in.diagErr)
	install := installRow(in.plan, in.installed, def, o)
	if in.installed != "" {
		install.Help[installSkip] = "sbx " + in.installed + " stays; Enter opens page 2/4"
		if !o.skipLogin && (!in.diagnosed || in.auth == sbx.AuthOut) {
			install.Help[installSkip] = "sbx " + in.installed + " stays; Enter signs in to Docker, then checks again"
		}
	}
	sbxRows := []choiceui.Row{install}
	if in.plan.Prefix != "" {
		under := &choiceui.When{Row: rowInstall, Option: installTarball}
		if seen[rowPath].ran() {
			under = nil
		}
		popts := prefixOptions(def.Prefix, o.prefix)
		help := map[string]string{
			prefixSystem:                 "system-wide; the install step runs as root",
			sbx.DefaultPrefix(homeDir()): "per-user, no root — the safer default, and the release installer's own",
		}
		if strings.TrimSpace(o.prefix) != "" {
			help[def.Prefix] = "the prefix --prefix asked for"
		}
		pathOpts := []string{pathAddRC, pathPrint}
		sbxRows = append(sbxRows,
			choiceui.Row{Label: rowPrefix, Indent: 2, When: &choiceui.When{Row: rowInstall, Option: installTarball},
				Options: popts, Selected: indexOf(popts, def.Prefix), Cols: cols(len(popts)), CellRunes: cellRunes, Help: help},
			choiceui.Row{Label: rowPath, Indent: 2, When: under, Options: pathOpts, Selected: indexOf(pathOpts, pathChoice(def.AddPath)),
				Cols: cols(len(pathOpts)), CellRunes: cellRunes, Help: map[string]string{
					pathAddRC: "the prefix is off PATH by design; without this, `sbx` will not resolve",
					pathPrint: "nothing is written to your dotfiles — the export line is printed instead",
				}})
	}
	sbxRows = append(sbxRows, loginRow(in.installed != "" && in.diagnosed, in.auth, def.Login, o.skipLogin))
	if in.network.allowAll {
		sbxRows = append(sbxRows, baselineRow())
	}
	sbxRows[0].Divider, sbxRows[0].Heading = true, pageSbx
	rows = append(rows, lockRan(sbxRows, seen)...)
	rows = append(rows, aheadRows(2, setupAhead(o.skipSetup))...)
	f := initCard(at, "Prepare sbx on this "+in.host.OS+"/"+in.host.Arch+" host", header, rows)
	f.EnterWord = func(f *choiceui.Form) string { return runWord(count(answered(f, def))) }
	if len(in.stop) > 0 {
		f.Blocked = blockedErr{in.stop}.Error()
		if fix := in.stop[0].Fix; fix != "" {
			f.Blocked += " — " + fix
		}
	}
	return f
}

// cols places n options at the grid's first n columns.
func cols(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// gitPageIn is what page 2 draws from: the identity git resolves, and whether git is there.
type gitPageIn struct {
	id    gitidentity.Identity
	gitOK bool
}

// gitAnswer is the git identity page 2 sends to the git step: the two fields, and whether the host
// held both already.
type gitAnswer struct {
	name, email string
	held        bool
}

// gitForm is page 2, "Set up git and the shell": git identity with its fields under now, GitHub,
// then the prompt kit, with page 1 as its done row. count is how many rows Enter runs.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD)
func gitForm(in gitPageIn, at choiceui.Step, def initDecisions, o initOptions, done []choiceui.Row, seen map[string]rowState, count func(initDecisions) int) *choiceui.Form {
	why := "git's global config on this host gets user.name and user.email; each run passes both into its sandbox"
	var git []choiceui.Row
	switch {
	case o.skipGit:
		git = []choiceui.Row{flagRow(rowGit, "--skip-git")}
	case !in.gitOK:
		git = []choiceui.Row{{Kind: choiceui.RowStatus, Mark: choiceui.MarkNeeds, Label: rowGit, Outcome: "git is not on PATH"}}
	case in.id.Complete():
		git = []choiceui.Row{{Kind: choiceui.RowStatus, Mark: choiceui.MarkDone, Label: rowGit, Outcome: in.id.Name + " <" + in.id.Email + ">"}}
	default:
		under := &choiceui.When{Row: rowGit, Option: "now"}
		git = []choiceui.Row{
			nowLaterRow(rowGit, def.Git, map[string]string{
				"now":   "Enter writes both fields to git's global config; a commit inside a sandbox fails late without them",
				"later": "the agent hits 'Please tell me who you are' at its first commit",
			}),
			{Label: rowName, Indent: 2, When: under, Field: true, Value: in.id.Name, Reason: why},
			{Label: rowEmail, Indent: 2, When: under, Field: true, Value: in.id.Email, Reason: why},
		}
	}
	gh := flagRow(rowGh, "--skip-gh")
	if !o.skipGh {
		gh = nowLaterRow(rowGh, def.Gh, map[string]string{
			"now":   "gh signs in to GitHub's API, then sandbox Git access opens: SSH agent, HTTPS token, or later",
			"later": "runs stay on anonymous GitHub API limits; `git push` to github.com needs `sbx secret set github`",
		})
	}
	prompt := flagRow(rowPrompt, "--skip-prompt")
	if !o.skipPrompt {
		prompt = nowLaterRow(rowPrompt, def.Prompt, map[string]string{
			"now":   "starship, a Nerd Font and the proveo segment install; starship replaces this shell's prompt",
			"later": "the prompt kit (starship, a Nerd Font, the proveo segment) waits",
		})
	}
	git[0].Divider, git[0].Heading = true, "git"
	prompt.Divider, prompt.Heading = true, "shell"
	rows := lockRan(append(append(git, gh), prompt), seen)
	rows = append(append(append([]choiceui.Row(nil), done...), rows...), aheadRows(3, setupAhead(o.skipSetup))...)
	f := initCard(at, "Set up git and the shell", nil, rows)
	f.EnterWord = func(f *choiceui.Form) string { return runWord(count(answered(f, def))) }
	return f
}

func pathChoice(add bool) string {
	if add {
		return pathAddRC
	}
	return pathPrint
}

// loginRow is Docker login as sbx diagnose left it when the page drew: an answer only while it said
// signed out, or while no sbx was there to ask; checked says a diagnose of an installed sbx ran.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func loginRow(checked bool, auth sbx.Auth, def, skip bool) choiceui.Row {
	var r choiceui.Row
	switch {
	case skip:
		r = flagRow(rowLogin, "--skip-login")
	case checked && auth == sbx.AuthIn:
		r = choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkDone, Label: rowLogin, Outcome: loginSignedIn}
	case checked && auth == sbx.AuthUnknown:
		r = choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkLater, Label: rowLogin, Outcome: loginUnchecked + " · `sbx login` signs in"}
	case checked:
		r = nowLaterRow(rowLogin, def, loginHelp)
	default:
		r = nowLaterRow(rowLogin, def, map[string]string{
			loginNow: "after the install, `sbx login` runs only if sbx diagnose says Docker is signed out",
			"later":  loginHelp["later"],
		})
		r.Paint = map[string]string{loginNow: loginIfOut}
	}
	return r
}

var loginHelp = map[string]string{
	loginNow: "sbx diagnose says Docker is signed out: `sbx login` takes the terminal for Docker's browser sign-in; " +
		"proveo never sees the credential",
	"later": "the first `sbx run` starts `sbx login` itself",
}

// flagRow is a row a flag answered: later, and the flag that said so.
func flagRow(label, flag string) choiceui.Row {
	return choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkLater, Label: label, Outcome: "later · " + flag}
}

// setupRow is page 4's sbx setup row for what the probe found: an answer only while it found work.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (THE SBX STEPS RUN ONLY WITH WORK)
func setupRow(offer sbx.SetupOffer, def, skip bool) choiceui.Row {
	switch {
	case skip:
		return flagRow(rowWizard, "--skip-setup")
	case !offer.Known:
		return choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkLater, Label: rowWizard, Outcome: "not checked · `sbx setup` opens it"}
	case !offer.Offers():
		return choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkLater, Label: rowWizard, Outcome: "nothing to import"}
	}
	opts := []string{setupAfter, setupLater}
	r := choiceui.Row{Label: rowWizard, Options: opts, Cols: cols(2), CellRunes: cellRunes, Help: map[string]string{
		setupAfter: "sbx's own setup screen opens after the result: " + offer.Reason(),
		setupLater: "`sbx setup` on a terminal imports the env keys sbx's store lacks",
	}}
	if !def {
		r.Selected = 1
	}
	return r
}

func nowLaterRow(label string, def bool, help map[string]string) choiceui.Row {
	opts := []string{"now", "later"}
	sel := 0
	if !def {
		sel = 1
	}
	return choiceui.Row{Label: label, Options: opts, Selected: sel, Help: help, Cols: cols(2), CellRunes: cellRunes}
}

// installRow always draws the distro package so the alternative is visible,
// even on a host that cannot run one. Dropping it is what made "release
// tarball" look like the only way sbx arrives — and like a second copy of
// proveo, which came from the CDN and is already on PATH. Default first:
// the rootless tarball, then the root package, then keep what is here.
func installRow(plan sbx.Plan, installed string, def initDecisions, o initOptions) choiceui.Row {
	opts := []string{installTarball, installPackaged}
	digest := "no digest published"
	if plan.Provenance != nil {
		digest = "digest verified against the release's provenance"
	}
	help := map[string]string{
		installTarball:  plan.Asset + " · " + digest,
		installPackaged: "sbx via the host package manager, system-wide — needs root, and no published digest covers it",
	}
	off := make([]bool, len(opts))
	var reason string
	switch {
	case plan.Packaged != nil:
		help[installPackaged] = plan.Packaged.Why
	case strings.TrimSpace(plan.Alt) != "":
		off[indexOf(opts, installPackaged)] = true
		reason = installPackaged + ": not offered — proveo cannot pin a package-manager install"
		help[installPackaged] = reason + "; `proveo init --print` names " + plan.Alt
	default:
		off[indexOf(opts, installPackaged)] = true
		reason = installPackaged + ": not offered — no .deb/.rpm on this OS"
		help[installPackaged] = reason
	}
	if installed != "" {
		opts = append(opts, installSkip)
		off = append(off, false)
		help[installSkip] = "sbx " + installed + " stays; Enter checks it, and signs in only while Docker is signed out"
	}
	r := choiceui.Row{
		Label:     rowInstall,
		Options:   opts,
		Selected:  indexOf(opts, def.Install),
		Help:      help,
		Off:       off,
		Reason:    reason,
		Cols:      cols(len(opts)),
		CellRunes: cellRunes,
		Paint:     map[string]string{installSkip: "keep as is"},
	}
	if o.force {
		r.Selected = indexOf(opts, installTarball)
		r.Kind, r.Mark, r.Outcome = choiceui.RowStatus, choiceui.MarkAhead, installTarball+" · --force"
	}
	return r
}

// baselineRow offers a host-wide network baseline while sbx's policy allows every host.
func baselineRow() choiceui.Row {
	opts := []string{baselineLeave, sbx.BaselineBalanced, sbx.BaselineDenyAll}
	return choiceui.Row{
		Label: rowBaseline, Options: opts, Selected: 0, Cols: cols(len(opts)), CellRunes: cellRunes,
		Reason: "this host currently allows every host, so a run's Kit allowlist adds reach rather than limiting it",
		Help: map[string]string{
			baselineLeave:        "keep allow-all; proveo will say so on every run",
			sbx.BaselineBalanced: "`sbx policy init balanced` — a curated allowlist, host-wide",
			sbx.BaselineDenyAll:  "`sbx policy init deny-all` — nothing reaches out unless a Kit allows it",
		},
	}
}

func indexOf(opts []string, want string) int {
	for i, o := range opts {
		if o == want {
			return i
		}
	}
	return 0
}
