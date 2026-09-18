// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/internal/choiceui/wireframe.puml, _spec/internal/sbx/host-readiness.puml
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/sbx"
)

type initDecisions struct {
	Install  string // installTarball | installPackaged | installSkip
	Prefix   string
	AddPath  bool
	Login    bool
	Wizard   bool
	Gh       bool
	Git      bool
	Baseline string // "" leaves the host-wide network policy alone
}

const (
	installTarball  = "rootless tarball"
	installPackaged = "distro package"
	installSkip     = "keep what is installed"

	rowInstall  = "install"
	rowPrefix   = "prefix"
	rowPath     = "PATH"
	rowSignIn   = "sign in"
	rowWizard   = "sbx setup"
	rowGh       = "gh"
	rowGit      = "git identity"
	rowBaseline = "network baseline"
	rowNext     = "next steps"

	baselineLeave = "leave as is"
	prefixSystem  = "/usr/local"
)

// prefixOptions are the two the release's own installer documents: its
// default per-user prefix first, then the system-wide one. An explicit
// --prefix is the only option there is.
func prefixOptions(def string) []string {
	if def != sbx.DefaultPrefix(homeDir()) {
		return []string{def}
	}
	return []string{def, prefixSystem}
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func defaultDecisions(plan sbx.Plan, installed string, o initOptions) initDecisions {
	d := initDecisions{
		Install:  installTarball,
		Prefix:   plan.Prefix,
		AddPath:  plan.Bin != "",
		Login:    !o.skipLogin,
		Wizard:   !o.skipSetup,
		Gh:       !o.skipGh,
		Git:      !o.skipGit,
		Baseline: "",
	}
	// A one-time install: proveo puts sbx on a host that has none and then
	// stops. An sbx that is already here is the operator's, whatever its
	// version — replacing it would be managing their toolchain, so an old one
	// is reported with instructions instead. `--force` is the explicit ask.
	if installed != "" && !o.force {
		d.Install = installSkip
	}
	return d
}

func askDecisions(plan sbx.Plan, host sbx.Host, installed string, checks []sbx.Prereq, o initOptions) (initDecisions, error) {
	def := defaultDecisions(plan, installed, o)

	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("init — ready %s for sbx %s", describeHost(host), sbx.Release),
		Header: initHeader(plan, installed, checks),
		Glyphs: posture.GlyphModeFrom(os.Getenv),
		NoAxis: true,
		Rows:   initRows(plan, installed, def, o),
	}

	ok, err := form.Run()
	if err != nil {
		return initDecisions{}, err
	}
	if !ok {
		return initDecisions{}, fmt.Errorf("cancelled at the init prompt")
	}

	out := def
	if v := form.Selection(rowInstall); v != "" {
		out.Install = v
	}
	if v := form.Selection(rowPrefix); v != "" {
		out.Prefix = v
	}
	if v := form.Selection(rowPath); v != "" {
		out.AddPath = v == "add to my shell rc"
	}
	if v := form.Selection(rowSignIn); v != "" {
		out.Login = v == "now"
	}
	if v := form.Selection(rowWizard); v != "" {
		out.Wizard = v == "now"
	}
	if v := form.Selection(rowGh); v != "" {
		out.Gh = v == "now"
	}
	if v := form.Selection(rowGit); v != "" {
		out.Git = v == "now"
	}
	if v := form.Selection(rowBaseline); v != "" && v != baselineLeave {
		out.Baseline = v
	}
	return out, nil
}

func initHeader(plan sbx.Plan, installed string, checks []sbx.Prereq) []string {
	out := []string{
		"sbx     the backend proveo runs on — proveo itself is already on PATH from the CDN",
		"asset   " + plan.Asset,
	}
	if plan.Provenance != nil {
		out = append(out, "digest  verified against the release's provenance")
	} else {
		out = append(out, "digest  none published for this asset — reported, not verified")
	}
	if installed != "" {
		out = append(out, "found   sbx "+installed+" already on this host")
	}
	for _, c := range checks {
		if c.OK {
			continue
		}
		out = append(out, fmt.Sprintf("%-7s %s — %s", string(c.Blocks)+"!", c.Name, c.Detail))
	}
	return out
}

func initRows(plan sbx.Plan, installed string, def initDecisions, o initOptions) []choiceui.Row {
	var rows []choiceui.Row
	rows = append(rows, installRow(plan, installed, def, o))

	// Where it lands. Only meaningful for the prefix-based plans; the MSI owns
	// its own location and there is nothing to choose.
	if plan.Prefix != "" {
		popts := prefixOptions(def.Prefix)
		home := sbx.DefaultPrefix(homeDir())
		help := map[string]string{
			prefixSystem: "system-wide, and the install step will need to be run as root — " +
				"choose the home prefix unless you know you want this",
		}
		if def.Prefix == home {
			help[home] = "per-user, no root — the safer default, and the release installer's own"
		} else {
			help[def.Prefix] = "the prefix --prefix asked for"
		}
		rows = append(rows, choiceui.Row{
			Label: rowPrefix, Options: popts, Selected: indexOf(popts, def.Prefix), Help: help,
		})

		pathOpts := []string{"add to my shell rc", "print the line, change nothing"}
		rows = append(rows, choiceui.Row{
			Label: rowPath, Options: pathOpts, Selected: indexOf(pathOpts, pathChoice(def.AddPath)),
			Help: map[string]string{
				"add to my shell rc":             "the prefix is off PATH by design; without this, `sbx` will not resolve",
				"print the line, change nothing": "nothing is written to your dotfiles",
			},
		})
	}

	signIn := nowLaterRow(rowSignIn, def.Login, o.skipLogin, "--skip-login was passed", map[string]string{
		"now":   "next: `sbx login` — Docker Hub OAuth so image pulls work; proveo never sees the credential",
		"later": "skip for now; pulls inside a run fail until you have signed in",
	})
	signIn.Divider, signIn.Heading = true, rowNext
	rows = append(rows, signIn)

	rows = append(rows, nowLaterRow(rowWizard, def.Wizard, o.skipSetup, "--skip-setup was passed", map[string]string{
		"now":   "next: `sbx setup` — the first-run wizard (SSH, daemon). 0.42 stopped opening it on its own",
		"later": "a host nobody sets up stays half-configured, with no prompt to say so",
	}))

	rows = append(rows, nowLaterRow(rowGh, def.Gh, o.skipGh, "--skip-gh was passed", map[string]string{
		"now":   "verify `gh`, install it if missing, `gh auth login` if needed — then store the token as sbx's github service so git push to github.com works",
		"later": "runs stay on anonymous GitHub API limits; `git push` to github.com needs `sbx secret set github`",
	}))

	rows = append(rows, nowLaterRow(rowGit, def.Git, o.skipGit, "--skip-git was passed", map[string]string{
		"now":   "set `user.name` / `user.email` if this host has none — a commit inside a sandbox fails late without them",
		"later": "the agent will hit 'Please tell me who you are' at the first commit",
	}))

	// The host-wide network baseline. Offered because proveo otherwise only
	// NAGS about it, once per run, forever.
	if row, ok := baselineRow(); ok {
		rows = append(rows, row)
	}
	return rows
}

func pathChoice(add bool) string {
	if add {
		return "add to my shell rc"
	}
	return "print the line, change nothing"
}

func nowLaterRow(label string, def, locked bool, why string, help map[string]string) choiceui.Row {
	opts := []string{"now", "later"}
	sel := 0
	if !def {
		sel = 1
	}
	r := choiceui.Row{Label: label, Options: opts, Selected: sel, Help: help}
	if locked {
		r.Locked, r.Reason, r.Hover = true, why, 1
	}
	return r
}

// installRow always draws the distro package so the alternative is visible,
// even on a host that cannot run one. Dropping it is what made "release
// tarball" look like the only way sbx arrives — and like a second copy of
// proveo, which came from the CDN and is already on PATH. Default first:
// the rootless tarball, then the root package, then keep what is here.
func installRow(plan sbx.Plan, installed string, def initDecisions, o initOptions) choiceui.Row {
	opts := []string{installTarball, installPackaged}
	help := map[string]string{
		installTarball: "sbx from " + plan.Asset + ", into a prefix you own — no root. " +
			"Proveo is already on PATH from the CDN; this is the backend it runs on",
		installPackaged: "sbx via the host package manager, system-wide — needs root, and no published digest covers it",
	}
	off := make([]bool, len(opts))
	var reason string
	switch {
	case plan.Packaged != nil:
		help[installPackaged] = plan.Packaged.Why
	case strings.TrimSpace(plan.Alt) != "":
		off[indexOf(opts, installPackaged)] = true
		reason = "not offered: proveo cannot pin a package-manager install — " + plan.Alt
		help[installPackaged] = reason
	default:
		off[indexOf(opts, installPackaged)] = true
		reason = "no .deb/.rpm on this OS — the tarball is what the pinned release publishes here"
		help[installPackaged] = reason
	}
	if installed != "" {
		opts = append(opts, installSkip)
		off = append(off, false)
		help[installSkip] = "sbx " + installed + " stays; init still checks and signs in"
	}
	r := choiceui.Row{
		Label:    rowInstall,
		Options:  opts,
		Selected: indexOf(opts, def.Install),
		Help:     help,
		Off:      off,
		Reason:   reason,
	}
	if o.force {
		r.Locked, r.Reason = true, "--force was passed: a reinstall was asked for"
		r.Hover = indexOf(opts, installTarball)
		r.Selected = r.Hover
	}
	return r
}

func baselineRow() (choiceui.Row, bool) {
	current, known := sbx.PolicyBaseline()
	if !known || current != sbx.BaselineAllowAll {
		return choiceui.Row{}, false
	}
	opts := []string{baselineLeave, sbx.BaselineBalanced, sbx.BaselineDenyAll}
	return choiceui.Row{
		Label: rowBaseline, Options: opts, Selected: 0, Divider: true,
		Reason: "this host currently allows every host, so a run's Kit allowlist adds reach rather than limiting it",
		Help: map[string]string{
			baselineLeave:        "keep allow-all; proveo will say so on every run",
			sbx.BaselineBalanced: "`sbx policy init balanced` — a curated allowlist, host-wide",
			sbx.BaselineDenyAll:  "`sbx policy init deny-all` — nothing reaches out unless a Kit allows it",
		},
	}, true
}

func indexOf(opts []string, want string) int {
	for i, o := range opts {
		if o == want {
			return i
		}
	}
	return 0
}

// describeDecisions is what init prints when it did NOT ask — the non-TTY path,
// where the operator finds out afterwards what was chosen for them.
func describeDecisions(d initDecisions) string {
	parts := []string{"install: " + d.Install}
	if d.Prefix != "" {
		parts = append(parts, "prefix: "+d.Prefix)
	}
	parts = append(parts,
		"PATH: "+yesNo(d.AddPath),
		"sign in: "+yesNo(d.Login),
		"sbx setup: "+yesNo(d.Wizard),
		"gh: "+yesNo(d.Gh),
		"git identity: "+yesNo(d.Git))
	if d.Baseline != "" {
		parts = append(parts, "baseline: "+d.Baseline)
	}
	return strings.Join(parts, " · ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
