package main

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
	d := defaultDecisions(linuxPlan(t, "/opt/sbx"), "", initOptions{skipLogin: true, skipSetup: true, skipGh: true, skipGit: true})
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
	for _, want := range []string{rowInstall, rowPrefix, rowPath, rowSignIn, rowWizard, rowGh, rowGit} {
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
	if !strings.Contains(install.Help[installTarball], "CDN") {
		t.Errorf("tarball help must say this is sbx, not a second proveo from the CDN, got %q", install.Help[installTarball])
	}

	signIn := rows[byLabel[rowSignIn]]
	if !signIn.Divider || signIn.Heading != rowNext {
		t.Errorf("sign-in must open the %q group (so its own label stays), got Divider=%v Heading=%q",
			rowNext, signIn.Divider, signIn.Heading)
	}
	if rows[byLabel[rowWizard]].Divider {
		t.Error("sbx setup is in the next-steps group, not a second heading")
	}
	if rows[byLabel[rowGh]].Divider || rows[byLabel[rowGit]].Divider {
		t.Error("gh and git identity are in the next-steps group, not new headings")
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

func TestSkipGhLocksTheGhRow(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	o := initOptions{skipGh: true}
	rows := initRows(plan, "", defaultDecisions(plan, "", o), o)
	for _, r := range rows {
		if r.Label != rowGh {
			continue
		}
		if !r.Locked {
			t.Error("--skip-gh must lock the gh row rather than remove it")
		}
		if !strings.Contains(r.Reason, "--skip-gh") {
			t.Errorf("the lock must name the flag that caused it, got %q", r.Reason)
		}
		return
	}
	t.Fatal("no gh row was drawn")
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
		AddPath: true, Login: true, Wizard: false, Gh: true, Git: false, Baseline: sbx.BaselineDenyAll,
	})
	for _, want := range []string{installTarball, "/opt/sbx", "PATH: yes", "sign in: yes", "sbx setup: no", "gh: yes", "git identity: no", sbx.BaselineDenyAll} {
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
	want := []string{def, prefixSystem}
	if diff := cmp.Diff(want, prefixOptions(def)); diff != "" {
		t.Errorf("prefixOptions(default) mismatch (-want +got):\n%s", diff)
	}
}

func TestInitRowsGateAPackagedInstallThisHostCannotRun(t *testing.T) {
	t.Parallel()
	plan, err := sbx.PlanFor(sbx.Host{OS: "darwin", Arch: "arm64"}, sbx.DefaultPrefix(homeDir()), "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	rows := initRows(plan, "", defaultDecisions(plan, "", initOptions{}), initOptions{})
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
	if install.Reason == "" {
		t.Error("a gated package must explain itself — dropping the option hid that brew exists")
	}
	if !strings.Contains(install.Reason, "brew") && !strings.Contains(install.Help[installPackaged], "brew") {
		t.Errorf("darwin's gated reason should name the brew path proveo cannot pin, got Reason=%q Help=%q",
			install.Reason, install.Help[installPackaged])
	}
	if install.Selected != indexOf(install.Options, installTarball) {
		t.Errorf("Selected = %d (%q), want the tarball", install.Selected, install.Options[install.Selected])
	}

	prefix := rows[byLabel[rowPrefix]]
	home := sbx.DefaultPrefix(homeDir())
	if diff := cmp.Diff([]string{home, prefixSystem}, prefix.Options); diff != "" {
		t.Errorf("prefix options mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(prefix.Help[home], "safer") {
		t.Errorf("home prefix help must say it is the safer default, got %q", prefix.Help[home])
	}
}

func TestInitHeaderSaysThisInstallsSbxNotProveo(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, "/opt/sbx")
	got := strings.Join(initHeader(plan, "", nil), "\n")
	if !strings.Contains(got, "sbx") || !strings.Contains(got, "CDN") {
		t.Errorf("header = %q, want it to say this installs sbx and that proveo came from the CDN", got)
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
