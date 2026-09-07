package main

import (
	"strings"
	"testing"

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
	d := defaultDecisions(linuxPlan(t, "/opt/sbx"), "", initOptions{})

	if d.Install != installTarball {
		t.Errorf("Install = %q, want the rootless tarball", d.Install)
	}
	if d.Prefix != "/opt/sbx" {
		t.Errorf("Prefix = %q", d.Prefix)
	}
	if !d.AddPath || !d.Login || !d.Wizard {
		t.Errorf("a fresh host should get PATH, login and the wizard: %+v", d)
	}
	if d.Baseline != "" {
		t.Errorf("Baseline = %q, want the host's own policy left alone", d.Baseline)
	}
}

func TestDefaultDecisionsKeepACurrentInstall(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")

	if d := defaultDecisions(plan, sbx.Release, initOptions{}); d.Install != installSkip {
		t.Errorf("Install = %q with %s already present, want %q", d.Install, sbx.Release, installSkip)
	}
	// proveo installs sbx once and then leaves it alone: an sbx already on the
	// host is the operator's, whatever its version. Upgrading it unasked would
	// be managing their toolchain.
	if d := defaultDecisions(plan, "0.39.0", initOptions{}); d.Install != installSkip {
		t.Errorf("Install = %q with an older sbx present, want %q — init only fresh-installs", d.Install, installSkip)
	}
	if d := defaultDecisions(plan, sbx.Release, initOptions{force: true}); d.Install != installTarball {
		t.Errorf("--force must reinstall, got %q", d.Install)
	}
}

func TestFlagsSettleTheirDecisionsWithoutAsking(t *testing.T) {
	t.Parallel()
	d := defaultDecisions(linuxPlan(t, "/opt/sbx"), "", initOptions{skipLogin: true, skipSetup: true})
	if d.Login {
		t.Error("--skip-login must not sign in")
	}
	if d.Wizard {
		t.Error("--skip-setup must not run the wizard")
	}
}

// A row per pending decision, and a locked row where a flag already answered —
// drawn rather than dropped, so the operator can see the flag took effect.
func TestInitRowsCoverThePendingDecisions(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, sbx.DefaultPrefix(homeDir()))
	def := defaultDecisions(plan, "", initOptions{})
	rows := initRows(plan, "", def, initOptions{})

	byLabel := map[string]int{}
	for i, r := range rows {
		byLabel[r.Label] = i
		if len(r.Options) == 0 {
			t.Errorf("row %q offers nothing", r.Label)
		}
		for _, o := range r.Options {
			if r.Help[o] == "" {
				t.Errorf("row %q option %q explains nothing", r.Label, o)
			}
		}
	}
	for _, want := range []string{rowInstall, rowPrefix, rowPath, rowSignIn, rowWizard} {
		if _, ok := byLabel[want]; !ok {
			t.Errorf("no %q row; got %v", want, byLabel)
		}
	}

	// The RPM host must be offered the distro package, since the release ships
	// one for it — that is the choice between rootless and system-wide.
	install := rows[byLabel[rowInstall]]
	if !hasOption(install.Options, installPackaged) {
		t.Errorf("install row = %v, want the distro package offered on an RPM host", install.Options)
	}
	if hasOption(install.Options, installSkip) {
		t.Error("nothing is installed, so there is nothing to keep")
	}
}

func TestAFlagLockedRowIsDrawnWithItsReason(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	o := initOptions{skipLogin: true}
	rows := initRows(plan, "", defaultDecisions(plan, "", o), o)

	for _, r := range rows {
		if r.Label != rowSignIn {
			continue
		}
		if !r.Locked {
			t.Error("--skip-login must lock the sign-in row rather than remove it")
		}
		if !strings.Contains(r.Reason, "--skip-login") {
			t.Errorf("the lock must name the flag that caused it, got %q", r.Reason)
		}
		return
	}
	t.Fatal("no sign-in row was drawn")
}

// The MSI owns its own location and puts sbx on PATH itself, so a prefix and a
// PATH question there would be asking about something proveo does not control.
func TestWindowsIsNotAskedAboutAPrefix(t *testing.T) {
	t.Parallel()
	plan, err := sbx.PlanFor(sbx.Host{OS: "windows", Arch: "amd64"}, "", "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	rows := initRows(plan, "", defaultDecisions(plan, "", initOptions{}), initOptions{})
	for _, r := range rows {
		if r.Label == rowPrefix || r.Label == rowPath {
			t.Errorf("windows was asked about %q, which the MSI decides", r.Label)
		}
	}
}

// The promptless path has to SAY what it chose. An installer that silently
// picked for you and printed nothing is the shape nobody can debug.
func TestDescribeDecisionsNamesEveryChoiceItMade(t *testing.T) {
	t.Parallel()
	got := describeDecisions(initDecisions{
		Install: installTarball, Prefix: "/opt/sbx",
		AddPath: true, Login: true, Wizard: false, Baseline: sbx.BaselineDenyAll,
	})
	for _, want := range []string{installTarball, "/opt/sbx", "PATH: yes", "sign in: yes", "sbx setup: no", sbx.BaselineDenyAll} {
		if !strings.Contains(got, want) {
			t.Errorf("describeDecisions() = %q, missing %q", got, want)
		}
	}
	// A baseline nobody chose must not be reported as a change.
	if got := describeDecisions(initDecisions{Install: installSkip}); strings.Contains(got, "baseline") {
		t.Errorf("an untouched baseline was announced: %q", got)
	}
}

func TestPrefixOptionsCollapseToAnExplicitChoice(t *testing.T) {
	t.Parallel()
	if got := prefixOptions("/opt/somewhere"); len(got) != 1 || got[0] != "/opt/somewhere" {
		t.Errorf("prefixOptions(--prefix) = %v, want only what was asked for", got)
	}
	def := sbx.DefaultPrefix(homeDir())
	if got := prefixOptions(def); len(got) != 2 || got[0] != def {
		t.Errorf("prefixOptions(default) = %v, want the default first", got)
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
