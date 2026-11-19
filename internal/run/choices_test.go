package run

import (
	"slices"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

func TestEveryHarnessOffersHostChrome(t *testing.T) {
	t.Parallel()
	manifests, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, man := range manifests {
		for target := range man.Images {
			p := Params{Target: target, AddonsSet: true, Addons: []string{"chrome"}}
			if err := p.resolveAddonFlags(man); err != nil || !slices.Equal(p.Addons, []string{addonHostCDP}) {
				t.Errorf("%s: chrome = %v, %v", target, p.Addons, err)
			}
		}
	}
}

// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/internal/choiceui/wireframe.puml, _spec/internal/credentials/credential-decisions.puml, _spec/internal/sbx/credential-resolution.puml
func TestCredentialsRowIsAReadOnlyInventory(t *testing.T) {
	t.Parallel()
	row := credentialsRow(credentialInventory{APIKeys: []string{"github", "openai"}, OAuth: []string{"anthropic"}, SSH: []string{"SHA256:key (ED25519)"},
		Env: []string{"ARCHITECT_MODEL", "db_url", "OPENAI_API_KEY"}, EnvFile: "/work/.env"})
	if !row.Locked || !row.Multi || !slices.Equal(row.On, []bool{true, true, true, true}) || !slices.Equal(row.Off, []bool{true, true, true, true}) {
		t.Fatalf("inventory must be checked and disabled: %#v", row)
	}
	if !slices.Equal(row.Options, []string{"API keys", "OAuth", "SSH", ".env"}) || !slices.Equal(row.Cols, []int{0, 1, 2, 3}) {
		t.Fatalf("inventory cells: %#v", row)
	}
	for i := range row.Options {
		if m := row.Lists[i]; m == nil || !m.ReadOnly || len(m.Options) == 0 {
			t.Fatalf("cell %d has no read-only list", i)
		}
	}
	env := row.Lists[3]
	if !env.Filter || !env.Bare || !slices.Equal(env.Options, []string{"ARCHITECT_MODEL", "db_url", "OPENAI_API_KEY"}) {
		t.Fatalf(".env list = %#v, want a bare filterable list of the names", *env)
	}
	if got, want := env.Help["db_url"], "set in /work/.env; this list shows names only"; got != want {
		t.Errorf(".env entry help = %q, want %q", got, want)
	}
	if got, want := row.Help[".env"], "3 name(s) in /work/.env; read-only; this list shows names only"; got != want {
		t.Errorf(".env cell help = %q, want %q", got, want)
	}
	if _, ok := row.Help["db_url"]; ok {
		t.Errorf("a .env name wrote the row's help, where cells and other lists read theirs: %v", row.Help)
	}
	if got := row.Lists[0].Help["openai"]; got != "configured on the host; read-only" {
		t.Errorf("API keys entry help = %q", got)
	}
}

func TestTheDevicesRowPacksItsOptionsWithoutGaps(t *testing.T) {
	t.Parallel()
	p := Params{Addons: []string{addonAndroid}}
	man := manifest.Manifest{}
	if rows := p.addonRow(man, rowInterface); len(rows) != 0 {
		t.Fatalf("devices without mobile support = %v, want no row", rows)
	}
	man.Capabilities.HostDevices = []string{manifest.HostDeviceIOS, manifest.HostDeviceAndroid}
	rows := p.addonRow(man, rowInterface)
	if len(rows) != 1 {
		t.Fatalf("devices with mobile support = %v, want one row", rows)
	}
	r := rows[0]
	if !slices.Equal(r.Options, []string{"android", "ios"}) || !slices.Equal(r.Cols, []int{0, 1}) {
		t.Errorf("devices = %v cols %v, want android, ios in columns 0, 1", r.Options, r.Cols)
	}
	if !slices.Equal(r.On, []bool{true, false}) {
		t.Errorf("device ticks = %v, want android selected", r.On)
	}
}

func TestTUIAndDesktopAreOneRadioChoice(t *testing.T) {
	t.Parallel()
	p := Params{}
	man := manifest.Manifest{}
	man.Capabilities.HostDevices = []string{manifest.HostDeviceDesktop}
	rows := p.addonRow(man, rowSession)
	if len(rows) != 1 {
		t.Fatalf("desktop session rows = %v, want one row", rows)
	}
	r := rows[0]
	if !slices.Equal(r.RadioSet, []string{addonTUI, addonDesktop}) {
		t.Fatalf("radio set = %v, want tui and desktop", r.RadioSet)
	}
	on := func(opt string) bool { return r.On[slices.Index(r.Options, opt)] }
	if !on(addonTUI) || on(addonDesktop) {
		t.Fatalf("no answer yet: tui %v desktop %v, want tui", on(addonTUI), on(addonDesktop))
	}
	r.On[slices.Index(r.Options, addonDesktop)] = true
	if !pairTUIDesktop(&r, false) || on(addonTUI) {
		t.Errorf("ticking desktop must untick tui: tui %v desktop %v", on(addonTUI), on(addonDesktop))
	}
	r.On[slices.Index(r.Options, addonTUI)] = true
	if pairTUIDesktop(&r, true) || on(addonDesktop) {
		t.Errorf("ticking tui must untick desktop: tui %v desktop %v", on(addonTUI), on(addonDesktop))
	}
	r.On[slices.Index(r.Options, addonTUI)] = false
	if pairTUIDesktop(&r, false); !on(addonTUI) {
		t.Error("unticking the only choice must leave it on — a radio is never empty")
	}
	f := &choiceui.Form{Rows: []choiceui.Row{r}}
	gateAddons(f)
	if f.Rows[0].Off[slices.Index(r.Options, addonTUI)] {
		t.Error("tui is still locked where desktop is offered")
	}
	if slices.Contains(selectedAddons(f), addonTUI) {
		t.Error("tui leaked into the add-ons; it is the default, not an add-on")
	}
}

func TestExecutionOutputsAndPortsUseTheColumnGrid(t *testing.T) {
	t.Parallel()
	exec := layoutExecution(choiceui.Row{Options: executionOptions()})
	if !slices.Equal(exec.Cols, []int{0, 1, 2}) || exec.Paint[addonSandbox] != "sandbox" {
		t.Errorf("execution cols %v paint %v, want host at 0, sandbox at 1, cloud at 2", exec.Cols, exec.Paint)
	}
	out := evidenceRow("")
	if !slices.Equal(out.Cols, []int{0, 1}) {
		t.Errorf("outputs cols = %v, want filtered at 0 and verbose at 1", out.Cols)
	}
	ports, ok := portsRow(foundPorts, nil, allFree)
	if !ok || !slices.Equal(ports.Cols, []int{0, 1}) {
		t.Errorf("ports cols = %v ok=%v, want 0, 1", ports.Cols, ok)
	}
}

func TestFormSelectedBrokerSurvivesAForwardOnlyManifestOnSbx(t *testing.T) {
	t.Parallel()
	p := Params{Target: "cursor", Credentials: "broker"}
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	if err := p.applyCapabilities(man); err != nil {
		t.Fatalf("form-selected broker was refused: %v", err)
	}
	if p.Credentials != "broker" {
		t.Errorf("credentials = %q, want broker kept", p.Credentials)
	}
}

// cursor's manifest declares forward; sbx's proxy brokers it natively, so broker wins
// whether it came from the default or the operator (decided 2026-09-29).
func TestAnEmptyDefaultOrAnExplicitBrokerBrokersOnSbx(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "cursor",
		Capabilities: manifest.Capabilities{Credentials: []string{"forward"}},
	}
	p := Params{Target: "cursor"}
	if err := p.applyCapabilities(man); err != nil || p.Credentials != "" {
		t.Errorf("empty default: credentials = %q, %v; want the broker default kept", p.Credentials, err)
	}
	p = Params{Target: "cursor", Credentials: "broker", CredsSet: true}
	if err := p.applyCapabilities(man); err != nil || p.Credentials != "broker" {
		t.Errorf("--credentials broker = %q, %v; sbx's proxy brokers cursor natively", p.Credentials, err)
	}
}

func TestHeaderOmitsKeysPresentOnlyInLookup(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")
	man := manifest.Manifest{
		Name: "opencode",
		Env:  []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
	lookup := func(k string) string {
		if k == "OPENCODE_API_KEY" {
			return "from-dotenv"
		}
		return ""
	}
	h := buildHeader(man, lookup, provider.Roles{}, t.TempDir(), t.TempDir(), "")
	if joined := strings.Join(h, "\n"); strings.Contains(joined, "OPENCODE_API_KEY") {
		t.Errorf("header listed a key that is not in the process env:\n%s", joined)
	}
}

func TestEmptyCredentialsHaveNoTicksOrDropdowns(t *testing.T) {
	t.Parallel()
	r := credentialsRow(credentialInventory{})
	if slices.Contains(r.On, true) || len(r.Lists) != 0 || !r.Locked {
		t.Fatalf("empty inventory = %#v", r)
	}
}

func TestBrowserRowNamesAndSelections(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "hermes"}
	man.Capabilities.HostBrowser = manifest.HostBrowserCDP
	for _, tt := range []struct {
		label   string
		addon   string
		profile string
	}{
		{label: "off"},
		{label: "isolated", addon: addonBrowser},
		{label: "named", addon: addonBrowser, profile: "work"},
		{label: "host", addon: addonHostCDP},
	} {
		p := Params{BrowserProfile: tt.profile}
		if tt.addon != "" {
			p.Addons = []string{tt.addon}
		}
		rows := p.browserRows(man)
		if got := rows[0].Options; !slices.Equal(got, []string{"off", "isolated", "named", "host"}) {
			t.Fatalf("browser options = %v", got)
		}
		f := &choiceui.Form{Rows: rows}
		if got := f.Selection(rowBrowser); got != tt.label {
			t.Errorf("browser selection = %q, want %q", got, tt.label)
		}
		if got := selectedAddons(f); !slices.Equal(got, p.Addons) {
			t.Errorf("%s selected add-ons = %v, want %v", tt.label, got, p.Addons)
		}
		if got := rows[1].When; got == nil || got.Row != rowBrowser || got.Option != "named" {
			t.Errorf("profile name condition = %+v, want browser = named", got)
		}
	}
}

func TestIsolatedBrowserKeepsTheSelectedImageAndVariant(t *testing.T) {
	man := manifest.Manifest{
		Name: "claudecode",
		Images: map[string]string{
			"claudecode":          "base",
			"claudecode-browser":  "browser",
			"claudecode-solidity": "solidity",
		},
	}
	p := Params{Target: man.Name, Addons: []string{addonBrowser}}
	if got := p.isolatedImage(man); got != "browser" {
		t.Fatalf("normal harness image selection = %q", got)
	}
	p.ImageSet = true
	if got := p.isolatedImage(man); got != "" {
		t.Fatalf("explicit image was replaced by %q", got)
	}
	p.Target, p.ImageSet = "claudecode-solidity", false
	if got := p.isolatedImage(man); got != "" {
		t.Fatalf("Solidity variant was replaced by %q", got)
	}
}
