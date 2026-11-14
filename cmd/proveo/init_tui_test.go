package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func linuxPlan(t *testing.T, prefix string) sbx.Plan {
	t.Helper()
	plan, err := sbx.PlanFor(sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Like: "rhel"}, prefix, "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDefaultDecisionsOnAFreshHost(t *testing.T) {
	t.Parallel()
	d := defaultDecisions(linuxPlan(t, "/opt/sbx"), "", initOptions{}, true)

	if d.Install != installTarball {
		t.Errorf("Install = %q, want the rootless tarball", d.Install)
	}
	if d.Prefix != "/opt/sbx" {
		t.Errorf("Prefix = %q", d.Prefix)
	}
	if !d.AddPath || !d.Login || !d.Wizard || !d.Gh || !d.Git {
		t.Errorf("a fresh host should get PATH, login, the wizard, gh and git identity: %+v", d)
	}
	if d.Baseline != "" {
		t.Errorf("Baseline = %q, want the host's own policy left alone", d.Baseline)
	}
}

func TestDefaultDecisionsKeepACurrentInstall(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")

	if d := defaultDecisions(plan, sbx.Release, initOptions{}, true); d.Install != installSkip {
		t.Errorf("Install = %q with %s already present, want %q", d.Install, sbx.Release, installSkip)
	}
	// proveo installs sbx once and then leaves it alone: an sbx already on the
	// host is the operator's, whatever its version. Upgrading it unasked would
	// be managing their toolchain.
	if d := defaultDecisions(plan, "0.39.0", initOptions{}, true); d.Install != installSkip {
		t.Errorf("Install = %q with an older sbx present, want %q — init only fresh-installs", d.Install, installSkip)
	}
	if d := defaultDecisions(plan, sbx.Release, initOptions{force: true}, true); d.Install != installTarball {
		t.Errorf("--force must reinstall, got %q", d.Install)
	}
}

func TestFlagsSettleTheirDecisionsWithoutAsking(t *testing.T) {
	t.Parallel()
	d := defaultDecisions(linuxPlan(t, "/opt/sbx"), "", initOptions{skipLogin: true, skipSetup: true, skipGh: true, skipGit: true}, true)
	if d.Login {
		t.Error("--skip-login must not sign in")
	}
	if d.Wizard {
		t.Error("--skip-setup must not run the wizard")
	}
	if d.Gh {
		t.Error("--skip-gh must not ready GitHub CLI")
	}
	if d.Git {
		t.Error("--skip-git must not set git identity")
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: neither `sbx login` nor `sbx setup` gets the terminal under
// --yes or with no terminal. When the page draws, the rows decide.
func TestYesNeverHandsTheTerminalToSbx(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	host := sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Like: "rhel"}
	for _, c := range []struct {
		name          string
		o             initOptions
		terminal      bool
		wizard, login bool
	}{
		{"a terminal", initOptions{}, true, true, true},
		{"--yes on a terminal", initOptions{yes: true}, true, false, false},
		{"no terminal", initOptions{}, false, false, false},
		{"--skip-setup", initOptions{skipSetup: true}, true, false, true},
		{"--skip-login", initOptions{skipLogin: true}, true, true, false},
	} {
		d := defaultDecisions(plan, "", c.o, c.terminal)
		if d.Wizard != c.wizard || d.Login != c.login {
			t.Errorf("%s: Wizard = %v, Login = %v; want %v, %v", c.name, d.Wizard, d.Login, c.wizard, c.login)
		}
	}

	d := defaultDecisions(plan, "", initOptions{yes: true}, true)
	if steps := stepNames(applySbx(d, plannedCtx(plan, host, d, false))); d.Wizard || d.Login || slices.Contains(steps, actLogin) {
		t.Errorf("--yes hands sbx a stdin nobody answers: Wizard=%v Login=%v steps=%q", d.Wizard, d.Login, steps)
	}

	offer := sbx.SetupOffer{EnvOnly: 2, Known: true}
	def := defaultDecisions(plan, "", initOptions{}, true)
	for _, pick := range []string{setupAfter, setupLater} {
		row := setupRow(offer, def.Wizard, false)
		row.Selected = indexOf(row.Options, pick)
		if got := answered(&choiceui.Form{Rows: []choiceui.Row{row}}, def).Wizard; got != (pick == setupAfter) {
			t.Errorf("the row chose %q, Wizard = %v", pick, got)
		}
	}
	for _, c := range []struct {
		name  string
		offer sbx.SetupOffer
		skip  bool
		reads string
	}{
		{"--skip-setup", offer, true, "later · --skip-setup"},
		{"nothing to import", sbx.SetupOffer{Known: true}, false, "nothing to import"},
		{"not known", sbx.SetupOffer{}, false, "not checked · `sbx setup` opens it"},
	} {
		if row := setupRow(c.offer, true, c.skip); row.Kind != choiceui.RowStatus || row.Outcome != c.reads {
			t.Errorf("%s: row = %+v, want a status row reading %q", c.name, row, c.reads)
		}
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: the Docker login row is an answer only while sbx diagnose says
// signed out, or before sbx exists; otherwise a status row says what diagnose found.
func TestLoginRunsOnlyWhenSbxSaysSignedOut(t *testing.T) {
	t.Parallel()
	def := defaultDecisions(linuxPlan(t, "/opt/sbx"), "0.50.0", initOptions{}, true)
	for _, c := range []struct {
		name    string
		checked bool
		auth    sbx.Auth
		status  string
		cells   []string
		login   bool
	}{
		{"signed in", true, sbx.AuthIn, "signed in", nil, true},
		{"signed out", true, sbx.AuthOut, "", []string{"now", "later"}, true},
		{"diagnose gave no answer", true, sbx.AuthUnknown, "not checked · `sbx login` signs in", nil, true},
		{"no sbx yet", false, sbx.AuthUnknown, "", []string{"if signed out", "later"}, true},
	} {
		row := loginRow(c.checked, c.auth, def.Login, false)
		if c.status != "" {
			if row.Kind != choiceui.RowStatus || row.Outcome != c.status {
				t.Errorf("%s: row = %+v, want a status row reading %q", c.name, row, c.status)
			}
			continue
		}
		var cells []string
		for _, o := range row.Options {
			cell := o
			if p, ok := row.Paint[o]; ok {
				cell = p
			}
			cells = append(cells, cell)
		}
		if row.Kind != choiceui.RowAnswer || !slices.Equal(cells, c.cells) {
			t.Errorf("%s: kind %v, cells %q; want an answer with %q", c.name, row.Kind, cells, c.cells)
		}
		if row.CellRunes < len("if signed out") {
			t.Errorf("%s: a cell clips at %d runes", c.name, row.CellRunes)
		}
		for _, o := range row.Options {
			if row.Help[o] == "" {
				t.Errorf("%s: %q explains nothing", c.name, o)
			}
		}
		if got := answered(&choiceui.Form{Rows: []choiceui.Row{row}}, def).Login; got != c.login {
			t.Errorf("%s: Login = %v, want %v", c.name, got, c.login)
		}
	}
	if row := loginRow(true, sbx.AuthOut, true, true); row.Kind != choiceui.RowStatus || row.Outcome != "later · --skip-login" {
		t.Errorf("--skip-login row = %+v", row)
	}
}

// pageOneRows are page 1's rows for a host with no sbx yet.
func pageOneRows(plan sbx.Plan, installed string, o initOptions) []choiceui.Row {
	def := defaultDecisions(plan, installed, o, true)
	in := sbxPageIn{host: sbx.Host{OS: "linux", Arch: "amd64"}, plan: plan, installed: installed}
	return sbxForm(in, initStepAt(1), def, o, nil, func(initDecisions) int { return 0 }).Rows
}

// pageTwoRows are page 2's rows for a host git knows nobody on.
func pageTwoRows(o initOptions) []choiceui.Row {
	def := defaultDecisions(sbx.Plan{}, "", o, true)
	return gitForm(gitPageIn{gitOK: true}, initStepAt(2), def, o, nil, nil, func(initDecisions) int { return 0 }).Rows
}

// A row per pending decision on pages 1 and 2, and a status row where a flag already answered —
// drawn rather than dropped, so the operator can see the flag took effect.
func TestInitRowsCoverThePendingDecisions(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, sbx.DefaultPrefix(homeDir()))
	rows := append(pageOneRows(plan, "", initOptions{}), pageTwoRows(initOptions{})...)

	byLabel := map[string]int{}
	for i, r := range rows {
		byLabel[r.Label] = i
		if r.Kind != choiceui.RowAnswer || r.Field {
			continue
		}
		if len(r.Options) == 0 {
			t.Errorf("row %q offers nothing", r.Label)
		}
		for _, o := range r.Options {
			if r.Help[o] == "" {
				t.Errorf("row %q option %q explains nothing", r.Label, o)
			}
		}
	}
	for _, want := range []string{rowInstall, rowPrefix, rowPath, rowLogin, rowWizard, rowGh, rowGit, rowName, rowEmail, rowPrompt} {
		if _, ok := byLabel[want]; !ok {
			t.Errorf("no %q row; got %v", want, byLabel)
		}
	}

	// The RPM host must be offered the distro package, since the release ships
	// one for it — that is the choice between rootless and system-wide.
	install := rows[byLabel[rowInstall]]
	if diff := cmp.Diff([]string{installTarball, installPackaged}, install.Options); diff != "" {
		t.Errorf("install options mismatch (-want +got):\n%s", diff)
	}
	if hasOption(install.Options, installSkip) {
		t.Error("nothing is installed, so there is nothing to keep")
	}
	if install.Off[indexOf(install.Options, installPackaged)] {
		t.Error("an RPM host can run the distro package; it must not be gated")
	}
	if !install.Divider || install.Heading != pageSbx {
		t.Errorf("sbx install must open the %q section, got Divider=%v Heading=%q", pageSbx, install.Divider, install.Heading)
	}

	for _, label := range []string{rowPrefix, rowPath} {
		if r := rows[byLabel[label]]; r.Indent != 2 || r.When == nil || *r.When != (choiceui.When{Row: rowInstall, Option: installTarball}) {
			t.Errorf("%s must open under the tarball as a sub-row: %+v", label, r)
		}
	}
	path := rows[byLabel[rowPath]]
	if diff := cmp.Diff([]string{pathAddRC, pathPrint}, path.Options); diff != "" {
		t.Errorf("PATH options mismatch (-want +got):\n%s", diff)
	}
	if r := rows[byLabel[rowWizard]]; r.Kind != choiceui.RowStatus || r.Mark != choiceui.MarkAhead {
		t.Errorf("before page 4, sbx setup is an ahead row: %+v", r)
	}
	if !rows[byLabel[rowGit]].Divider || rows[byLabel[rowGh]].Divider || !rows[byLabel[rowPrompt]].Divider {
		t.Error("git identity opens git, GitHub sits under it, and the prompt kit opens shell")
	}
}

func TestAFlagLockedRowIsDrawnWithItsReason(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	for _, c := range []struct {
		o     initOptions
		label string
		rows  func(initOptions) []choiceui.Row
	}{
		{initOptions{skipLogin: true}, rowLogin, func(o initOptions) []choiceui.Row { return pageOneRows(plan, "", o) }},
		{initOptions{skipGh: true}, rowGh, pageTwoRows},
		{initOptions{skipGit: true}, rowGit, pageTwoRows},
	} {
		at := slices.IndexFunc(c.rows(c.o), func(r choiceui.Row) bool { return r.Label == c.label })
		if at < 0 {
			t.Fatalf("no %s row was drawn", c.label)
		}
		r := c.rows(c.o)[at]
		if r.Kind != choiceui.RowStatus || r.Mark != choiceui.MarkLater || !strings.Contains(r.Outcome, "later · --skip-") {
			t.Errorf("%s: a flag must turn the row into a status row naming it, got %+v", c.label, r)
		}
	}
}

// The MSI owns its own location and puts sbx on PATH itself, so a prefix and a
// PATH question there would be asking about something proveo does not control.
func TestWindowsIsNotAskedAboutAPrefix(t *testing.T) {
	t.Parallel()
	plan, err := sbx.PlanFor(sbx.Host{OS: "windows", Arch: "amd64"}, "", "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pageOneRows(plan, "", initOptions{}) {
		if r.Label == rowPrefix || r.Label == rowPath {
			t.Errorf("windows was asked about %q, which the MSI decides", r.Label)
		}
	}
}

func TestPrefixOptionsCollapseToAnExplicitChoice(t *testing.T) {
	t.Parallel()
	if got := prefixOptions("/opt/somewhere", "/opt/somewhere"); len(got) != 1 || got[0] != "/opt/somewhere" {
		t.Errorf("prefixOptions(--prefix) = %v, want only what was asked for", got)
	}
	def := sbx.DefaultPrefix(homeDir())
	want := []string{prefixSystem, def}
	if diff := cmp.Diff(want, prefixOptions(def, "")); diff != "" {
		t.Errorf("prefixOptions(default) mismatch (-want +got):\n%s", diff)
	}
	// A revisit after picking /usr/local still offers the home prefix.
	if diff := cmp.Diff(want, prefixOptions(prefixSystem, "")); diff != "" {
		t.Errorf("prefixOptions(revisit at /usr/local) mismatch (-want +got):\n%s", diff)
	}
}

// D15: a package manager proveo cannot pin draws gated, its reason one line with no command; the
// brew command is --print's.
func TestInitRowsGateAPackagedInstallThisHostCannotRun(t *testing.T) {
	t.Parallel()
	plan, err := sbx.PlanFor(sbx.Host{OS: "darwin", Arch: "arm64"}, sbx.DefaultPrefix(homeDir()), "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	rows := pageOneRows(plan, "", initOptions{})
	byLabel := map[string]int{}
	for i, r := range rows {
		byLabel[r.Label] = i
	}
	install := rows[byLabel[rowInstall]]
	if !hasOption(install.Options, installPackaged) {
		t.Fatalf("install options = %v, want distro package drawn so the alternative is visible", install.Options)
	}
	i := indexOf(install.Options, installPackaged)
	if i >= len(install.Off) || !install.Off[i] {
		t.Errorf("darwin has no .deb/.rpm; distro package must be gated, Off=%v", install.Off)
	}
	if install.Reason != "distro package: not offered — proveo cannot pin a package-manager install" {
		t.Errorf("the gated reason = %q, want one line with no command", install.Reason)
	}
	if plan.Alt != "" && !strings.Contains(install.OffWhy[installPackaged], "--print") {
		t.Errorf("the gated status must say where the command went, got %q", install.OffWhy[installPackaged])
	}
	if install.Selected != indexOf(install.Options, installTarball) {
		t.Errorf("Selected = %d (%q), want the tarball", install.Selected, install.Options[install.Selected])
	}

	prefix := rows[byLabel[rowPrefix]]
	home := sbx.DefaultPrefix(homeDir())
	if diff := cmp.Diff([]string{prefixSystem, home}, prefix.Options); diff != "" {
		t.Errorf("location options mismatch (-want +got):\n%s", diff)
	}
	if prefix.Selected != indexOf(prefix.Options, home) {
		t.Errorf("Selected = %d (%q), want the home prefix as default even though /usr/local is leftmost",
			prefix.Selected, prefix.Options[prefix.Selected])
	}
	if !strings.Contains(prefix.Help[home], "safer") {
		t.Errorf("home prefix help must say it is the safer default, got %q", prefix.Help[home])
	}
}

// Page 1's title says it prepares sbx, its facts row says which sbx this host has, and the tarball's
// help names the asset and digest that left the head.
func TestInitHeaderSaysThisInstallsSbxNotProveo(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	def := defaultDecisions(plan, "", initOptions{}, true)
	for _, c := range []struct {
		installed string
		network   networkState
		facts     []string
	}{
		{"", networkState{}, []string{"found: no sbx on this host"}},
		{"0.50.0", networkState{fact: "listed hosts only"}, []string{"found: sbx 0.50.0", "network: listed hosts only"}},
	} {
		in := sbxPageIn{host: sbx.Host{OS: "linux", Arch: "amd64"}, plan: plan, installed: c.installed, diagnosed: c.installed != "", network: c.network}
		f := sbxForm(in, initStepAt(1), def, initOptions{}, nil, func(initDecisions) int { return 0 })
		if !slices.Equal(f.Header, c.facts) || f.Title != "Prepare sbx on this linux/amd64 host" {
			t.Errorf("installed %q: head = %q %q, want %q", c.installed, f.Title, f.Header, c.facts)
		}
	}
	help := installRow(plan, "", def, initOptions{}).Help[installTarball]
	if !strings.Contains(help, plan.Asset) || !strings.Contains(help, "digest") {
		t.Errorf("tarball help = %q, want the asset and its digest that left the head", help)
	}
}

func hasOption(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

func TestInitAddsAPathLineOnlyForAPrefixHoldingSbx(t *testing.T) {
	t.Parallel()
	prefix := t.TempDir()
	plan := linuxPlan(t, prefix)
	if plan.Bin == "" {
		t.Skip("this plan places no binary")
	}
	if got := sbxPathDir(plan); got != "" {
		t.Errorf("sbxPathDir(empty prefix) = %q, want \"\" — a brew-installed sbx must not earn a dead PATH line", got)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := sbxPathDir(plan), filepath.Dir(plan.Bin); got != want {
		t.Errorf("sbxPathDir(prefix with sbx) = %q, want %q", got, want)
	}
}
