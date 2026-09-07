// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/internal/choiceui/wireframe.puml
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
	Baseline string // "" leaves the host-wide network policy alone
}

const (
	installTarball  = "release tarball"
	installPackaged = "distro package"
	installSkip     = "keep what is installed"

	rowInstall  = "install"
	rowPrefix   = "prefix"
	rowPath     = "PATH"
	rowSignIn   = "sign in"
	rowWizard   = "sbx setup"
	rowBaseline = "network baseline"

	baselineLeave = "leave as is"
)

// prefixOptions are the two the release's own installer documents: its default
// per-user prefix, and the system-wide one it names in the same breath.
func prefixOptions(def string) []string {
	if def != sbx.DefaultPrefix(homeDir()) {
		return []string{def} // an explicit --prefix is the only option there is
	}
	return []string{def, "/usr/local"}
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
		Baseline: "",
	}
	if installed != "" && !sbx.Older(installed, sbx.Release) && !o.force {
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
	if v := form.Selection(rowBaseline); v != "" && v != baselineLeave {
		out.Baseline = v
	}
	return out, nil
}

func initHeader(plan sbx.Plan, installed string, checks []sbx.Prereq) []string {
	out := []string{"asset   " + plan.Asset}
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

	// What to install. The packaged option is drawn only where the release
	// ships one, and the skip option only where there is something to keep.
	opts := []string{installTarball}
	help := map[string]string{
		installTarball: "from " + plan.Asset + ", into a prefix you own — no root",
	}
	if plan.Packaged != nil {
		opts = append(opts, installPackaged)
		help[installPackaged] = plan.Packaged.Why
	}
	if installed != "" {
		opts = append(opts, installSkip)
		help[installSkip] = "sbx " + installed + " stays; init still checks and signs in"
	}
	install := choiceui.Row{
		Label: rowInstall, Options: opts, Selected: indexOf(opts, def.Install), Help: help,
	}
	if o.force {
		install.Locked, install.Reason = true, "--force was passed: a reinstall was asked for"
		install.Hover = indexOf(opts, installTarball)
		install.Selected = install.Hover
	}
	rows = append(rows, install)

	// Where it lands. Only meaningful for the prefix-based plans; the MSI owns
	// its own location and there is nothing to choose.
	if plan.Prefix != "" {
		popts := prefixOptions(def.Prefix)
		rows = append(rows, choiceui.Row{
			Label: rowPrefix, Options: popts, Selected: indexOf(popts, def.Prefix),
			Help: map[string]string{
				def.Prefix: "the release installer's own default; needs no root",
				"/usr/local": "system-wide, and the install step will need to be run as root — " +
					"choose the default unless you know you want this",
			},
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

	rows = append(rows, nowLaterRow(rowSignIn, def.Login, o.skipLogin, "--skip-login was passed", map[string]string{
		"now":   "`sbx login` owns the credential; proveo never sees it",
		"later": "pulls inside a run will fail until you have signed in",
	}))

	rows = append(rows, nowLaterRow(rowWizard, def.Wizard, o.skipSetup, "--skip-setup was passed", map[string]string{
		"now":   "`sbx setup` — 0.42 stopped opening this on its own",
		"later": "a host nobody sets up stays half-configured, with no prompt to say so",
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
	r := choiceui.Row{Label: label, Options: opts, Selected: sel, Help: help, Divider: label == rowSignIn}
	if locked {
		r.Locked, r.Reason, r.Hover = true, why, 1
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
		"sbx setup: "+yesNo(d.Wizard))
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
