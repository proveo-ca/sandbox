// SPEC: _spec/_paradigms/retire-dind.puml
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestExecutionRowOffersTheHostAndTheSandbox(t *testing.T) {
	t.Parallel()
	opts := addonOptions(manifest.Manifest{Name: "h", Images: map[string]string{"h": "proveo/h:latest"}}, rowExecution)
	if !slices.Equal(opts, []string{addonHost, addonSandbox}) {
		t.Errorf("execution row = %v, want the host and %q", opts, addonSandbox)
	}
}

func TestEachPlaneOffersOnlyItsOwnKindOfChoice(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "claudecode", Images: map[string]string{"claudecode": "i", "claudecode-browser": "i"},
		Capabilities: manifest.Capabilities{HostBrowser: manifest.HostBrowserCDP},
	}
	exec := addonOptions(man, rowExecution)
	iface := addonOptions(man, rowInterface)
	if !slices.Equal(exec, []string{addonHost, addonSandbox}) {
		t.Errorf("execution = %v, want the excluded host then the declared daemon", exec)
	}
	if !slices.Equal(iface, []string{addonTUI, addonBrowser, addonHostCDP}) {
		t.Errorf("interface = %v, want the tui then each browser it can drive", iface)
	}
	for _, o := range append(append([]string{}, exec...), iface...) {
		if addonHelp[o] == "" {
			t.Errorf("%q is offered with no description — the gap this grouping exists to close", o)
		}
	}
}

// The fixed boxes state a fact: greyed, left in the state they state, and absent
// from the row's inline reason, which is reserved for constraints that MOVE.
func TestFixedBoxesAreGreyedInTheStateTheyState(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowExecution, Options: []string{addonHost, addonSandbox}, Multi: true, On: []bool{false, true}},
		{Label: rowInterface, Options: []string{addonTUI, addonBrowser}, Multi: true, On: []bool{false, true}},
	}}
	gateAddons(f)

	exec, iface := f.Rows[0], f.Rows[1]
	if !exec.Off[0] || exec.On[0] {
		t.Error("host must be greyed and unticked — proveo never runs an agent there")
	}
	if !iface.Off[0] || !iface.On[0] {
		t.Error("the tui must be greyed and TICKED: it is compulsory, not unavailable")
	}
	if exec.Reason != "" || iface.Reason != "" {
		t.Errorf("a box greyed on every run must not fill the inline reason: %q / %q", exec.Reason, iface.Reason)
	}
	for _, o := range []string{addonHost, addonTUI} {
		if exec.OffWhy[o] == "" && iface.OffWhy[o] == "" {
			t.Errorf("%q is greyed with no explanation", o)
		}
	}
	if !exec.Off[1] || !exec.On[1] {
		t.Error("an available sandbox must be greyed and TICKED: it is compulsory, not optional")
	}
	if got := selectedAddons(f); !slices.Equal(got, []string{addonSandbox, addonBrowser}) {
		t.Errorf("selectedAddons = %v, want the real toggles plus the compulsory sandbox", got)
	}
}

func TestEvidenceRowDefaultsToVerbose(t *testing.T) {
	t.Parallel()
	r := evidenceRow((Params{}).evidenceOrDefault())
	if r.Label != evidenceLabel || r.Multi {
		t.Fatalf("row = %+v, want a RADIO row labelled %q", r, evidenceLabel)
	}
	if len(r.Options) != 2 || r.Options[0] != evidenceFilter || r.Options[1] != EvidenceVerbose {
		t.Fatalf("options = %v, want [%s %s]", r.Options, evidenceFilter, EvidenceVerbose)
	}
	if r.Options[r.Selected] != EvidenceVerbose {
		t.Errorf("selected = %q, want verbose to be the default answer", r.Options[r.Selected])
	}
}

func TestEvidenceIsOneAnswerNotTwoBoxes(t *testing.T) {
	t.Parallel()
	r := evidenceRow(EvidenceVerbose)
	if r.Multi {
		t.Error("evidence is one-of, so it must not be a checkbox row")
	}
	if r.Options[r.Selected] != EvidenceVerbose {
		t.Errorf("a remembered verbose must come back selected, got %q", r.Options[r.Selected])
	}
	if got := evidenceRow(EvidenceDefault); got.Options[got.Selected] != evidenceFilter {
		t.Errorf("a remembered default must come back as %q, got %q", evidenceFilter, got.Options[got.Selected])
	}
	// Cycling reaches the other level and nothing else: there is no third state.
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonBrowser}, Multi: true, On: []bool{true}},
		evidenceRow(EvidenceDefault),
	}}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[evidenceOf(f.Selection(evidenceLabel))] = true
		f.Rows[1].Selected = (f.Rows[1].Selected + 1) % len(f.Rows[1].Options)
	}
	if len(seen) != 2 || !seen[EvidenceDefault] || !seen[EvidenceVerbose] {
		t.Errorf("the row reaches %v, want exactly the two levels", seen)
	}
	if !f.Rows[0].On[0] {
		t.Error("the evidence row must not reach into the add-ons row")
	}
}

func TestOutputsRowKeepsTheEvidenceValues(t *testing.T) {
	t.Parallel()
	if evidenceLabel != "outputs" {
		t.Errorf("row label = %q, want %q", evidenceLabel, "outputs")
	}
	for answer, want := range map[string]string{
		evidenceFilter:  EvidenceDefault,
		EvidenceVerbose: EvidenceVerbose,
	} {
		if got := evidenceOf(answer); got != want {
			t.Errorf("evidenceOf(%q) = %q, want %q — PROVEO_AGENT_EVIDENCE and the choice cache keep their values", answer, got, want)
		}
	}
}

// Anything that is not an explicit opt-out resolves to verbose: a typo in
// PROVEO_AGENT_EVIDENCE must not quietly buy a black-box run.
func TestEvidenceOrDefaultOnlyOptsOutOnDefault(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":              EvidenceVerbose,
		EvidenceVerbose: EvidenceVerbose,
		"bogus":         EvidenceVerbose,
		EvidenceDefault: EvidenceDefault,
	} {
		if got := (Params{Evidence: in}).evidenceOrDefault(); got != want {
			t.Errorf("evidence %q resolved to %q, want %q", in, got, want)
		}
	}
}

func TestSandboxSpecSeparatesSecretsFromEnv(t *testing.T) {
	lookup := func(k string) string {
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
			"ANTHROPIC_API_KEY":       "sk-value",
			"ANTHROPIC_BASE_URL":      "https://api.anthropic.com",
		}[k]
	}
	in := sandbox.Input{
		Target:   "claudecode",
		Image:    "proveo/claudecode:latest",
		Extra:    []string{"--verbose"},
		Evidence: EvidenceDefault,
		Forwards: false,
		Man: manifest.Manifest{
			Name: "claudecode",
			Capabilities: manifest.Capabilities{
				Hosts: []string{"api.anthropic.com", "statsig.anthropic.com"},
			},
			Env: []manifest.EnvVar{
				{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
				{Name: "ANTHROPIC_BASE_URL"},
			},
		},
		Sid:    "proveo-1-2",
		Lookup: lookup,
		Detected: func() []string {
			if _, ok := provider.Lookup("anthropic"); ok {
				return []string{"anthropic"}
			}
			return nil
		}(),
		GitEnv:  []string{"GIT_AUTHOR_NAME=Executor"},
		HomeEnv: []string{"PROVEO_HOME=/proveo-home"},
	}

	cfg, kit, secrets := sandbox.Spec(in)

	wantSecrets := map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": false}
	if len(secrets) != len(wantSecrets) {
		t.Fatalf("secrets = %v, want exactly the declared plan credential %v — an API key "+
			"in sbx's anthropic entry outranks the subscription", secrets, wantSecrets)
	}
	for _, kv := range secrets {
		if _, tracked := wantSecrets[kv[0]]; !tracked {
			t.Errorf("unexpected secret %q", kv[0])
		}
		if kv[1] == "" {
			t.Errorf("secret %q lost its value", kv[0])
		}
	}
	for _, e := range cfg.Env {
		name := strings.SplitN(e, "=", 2)[0]
		if name == "CLAUDE_CODE_OAUTH_TOKEN" || name == "ANTHROPIC_API_KEY" {
			t.Errorf("secret %q must travel via sbx secret, not env (%q)", name, e)
		}
	}

	var sawBaseURL bool
	for _, e := range cfg.Env {
		if e == "ANTHROPIC_BASE_URL=https://api.anthropic.com" {
			sawBaseURL = true
		}
	}
	if !sawBaseURL {
		t.Errorf("non-secret env missing resolved ANTHROPIC_BASE_URL in %v", cfg.Env)
	}
	for _, want := range []string{"PROVEO_AGENT_EVIDENCE=default", "GIT_AUTHOR_NAME=Executor", "PROVEO_HOME=/proveo-home"} {
		found := false
		for _, e := range cfg.Env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("env missing %q in %v", want, cfg.Env)
		}
	}

	up, _, _ := sbx.UpstreamKitFor("claudecode", sbx.UpstreamOptions{})
	if got, want := kit.Allow(), (sandbox.Workload{Descriptor: sbx.WorkloadV3{Capabilities: up.Capabilities}}).Allow(); !slices.Equal(got, want) {
		t.Errorf("runtime allow = %v, want the published claude kit's own list and no tier-driven hosts", got)
	}
	if len(kit.Hooks()) == 0 {
		t.Error("the workload must carry the seed step, or nothing composes subagents under sbx")
	}

	if cfg.Name != sbx.SandboxName("claudecode", "") || kit.Recipe.From != "proveo/claudecode:latest" {
		t.Errorf("run config name/FROM = %q/%q, want the def-keyed sandbox name", cfg.Name, kit.Recipe.From)
	}
	if strings.Contains(cfg.Name, "proveo-1-2") {
		t.Errorf("sandbox name %q carries the session id — every run would build its own", cfg.Name)
	}
	if want := []string{"--dangerously-skip-permissions", "--verbose"}; !slices.Equal(cfg.Command, want) {
		t.Errorf("command = %v, want %v: agent args after the launch's own (sbx replaces argv[1:])", cfg.Command, want)
	}
}

func TestTheSandboxStoresCredentialsUnlessForwardIsChosen(t *testing.T) {
	lookup := func(k string) string {
		return map[string]string{"CURSOR_API_KEY": "key-value"}[k]
	}
	in := func(forwards bool) sandbox.Input {
		return sandbox.Input{
			Target:   "cursor",
			Image:    "proveo/cursor:latest",
			Evidence: EvidenceDefault,
			Forwards: forwards,
			Man: manifest.Manifest{
				Name: "cursor",
				Env:  []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
				Capabilities: manifest.Capabilities{
					Hosts:       []string{"api2.cursor.sh"},
					Credentials: []string{"forward", "broker"},
				},
			},
			Sid:    "proveo-cursor-1",
			Lookup: lookup,
		}
	}

	// Brokered: sbx's store holds it and the agent's environment carries none.
	cfg, _, secrets := sandbox.Spec(in(false))
	if len(secrets) != 1 || secrets[0][0] != "CURSOR_API_KEY" {
		t.Errorf("secrets = %v, want the key routed to sbx's store", secrets)
	}
	for _, e := range cfg.Env {
		if e == "CURSOR_API_KEY" || strings.HasPrefix(e, "CURSOR_API_KEY=") {
			t.Errorf("brokered, yet the key reached the agent's environment as %q", e)
		}
	}

	// Forward: the operator chose the complete route, and the backend must honour
	// it — brokering covers only the hosts sbx's proxy sees, and an agent that
	// talks to others has nothing of its own without this.
	cfg, _, secrets = sandbox.Spec(in(true))
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none: forward was chosen", secrets)
	}
	var bare bool
	for _, e := range cfg.Env {
		if e == "CURSOR_API_KEY" {
			bare = true
		}
		if strings.HasPrefix(e, "CURSOR_API_KEY=") {
			t.Errorf("forwarded key must stay a bare -e name, got %q (value would ride argv)", e)
		}
	}
	if !bare {
		t.Errorf("cfg.Env = %v, want CURSOR_API_KEY forwarded from the host", cfg.Env)
	}
}

func TestAProviderSbxCannotInjectIsStillForwarded(t *testing.T) {
	lookup := func(k string) string {
		return map[string]string{"AWS_ACCESS_KEY_ID": "AKIA-value"}[k]
	}
	in := sandbox.Input{
		Target:   "cecli",
		Image:    "proveo/cecli:latest",
		Evidence: EvidenceDefault,
		Man: manifest.Manifest{
			Name: "cecli",
			Env:  []manifest.EnvVar{{Name: "AWS_ACCESS_KEY_ID", Secret: true}},
			Capabilities: manifest.Capabilities{
				Credentials: []string{"broker"},
			},
		},
		Sid:    "proveo-cecli-1",
		Lookup: lookup,
	}

	cfg, _, secrets := sandbox.Spec(in)

	for _, kv := range secrets {
		if kv[0] == "AWS_ACCESS_KEY_ID" {
			t.Error("stored a credential sbx's proxy has no host or header to attach")
		}
	}
	var bare bool
	for _, e := range cfg.Env {
		if e == "AWS_ACCESS_KEY_ID" {
			bare = true
		}
		if strings.HasPrefix(e, "AWS_ACCESS_KEY_ID=") {
			t.Errorf("forwarded key must stay a bare -e name, got %q (value would ride argv)", e)
		}
	}
	if !bare {
		t.Errorf("cfg.Env = %v, want AWS_ACCESS_KEY_ID forwarded: nothing else can carry it", cfg.Env)
	}
}

func TestSandboxSpecBrokeredCredentialsStayHostSide(t *testing.T) {
	lookup := func(k string) string {
		return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value"}[k]
	}
	in := sandbox.Input{
		Target:   "claudecode",
		Image:    "proveo/claudecode:latest",
		Evidence: EvidenceDefault,
		Forwards: false,
		Man: manifest.Manifest{
			Name: "claudecode",
			Env:  []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		},
		Sid:    "proveo-cc-1",
		Lookup: lookup,
	}

	_, _, secrets := sandbox.Spec(in)

	if len(secrets) == 0 {
		t.Fatal("secrets = none, want the declared secret injected host-side outside forward mode")
	}
	var named bool
	for _, kv := range secrets {
		if kv[0] == "CLAUDE_CODE_OAUTH_TOKEN" {
			named = true
		}
	}
	if !named {
		t.Errorf("secrets = %v, want the declared credential injected host-side", secrets)
	}
}

func TestAddonOptionsOffersTheHostBrowserOnlyToAHarnessWithAClient(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Images:       map[string]string{"claudecode": "proveo/claudecode:latest", "claudecode-browser": "proveo/claudecode-browser:latest"},
		Capabilities: manifest.Capabilities{HostBrowser: manifest.HostBrowserCDP},
	}
	want := []string{addonTUI, addonBrowser, addonHostCDP}
	if diff := cmp.Diff(want, addonOptions(man, rowInterface)); diff != "" {
		t.Errorf("interface options mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "cursor"}, rowInterface); slices.Contains(opts, addonHostCDP) {
		t.Errorf("no hostBrowser capability, yet offered %q: %v", addonHostCDP, opts)
	}
	// Off by default: the picker never pre-ticks a box that hands the agent the
	// operator's logged-in browser.
	p := &Params{}
	if on := p.addonDefaults(want); on[2] {
		t.Errorf("%q must start unticked, got %v", addonHostCDP, on)
	}
	if on := (&Params{Addons: []string{addonHostCDP}, AddonsAnswered: true}).addonDefaults(want); !on[2] {
		t.Errorf("a remembered %q must come back ticked, got %v", addonHostCDP, on)
	}
}

func TestGateAddonsIsStableOnTheFirstPass(t *testing.T) {
	t.Parallel()
	form := func() *choiceui.Form {
		return &choiceui.Form{Rows: []choiceui.Row{
			// On=false is what a cache answered before the lock looks like.
			{Label: rowExecution, Options: []string{addonSandbox}, Multi: true, On: []bool{false}},
			{Label: rowInterface, Options: []string{addonIOS}, Multi: true, On: []bool{true}},
		}}
	}
	first := form()
	gateAddons(first)
	second := form()
	gateAddons(second)
	gateAddons(second)
	for _, tc := range []struct {
		name string
		f    *choiceui.Form
	}{{"first pass", first}, {"second pass", second}} {
		if sb := tc.f.Rows[0]; !sb.Off[0] || !sb.On[0] {
			t.Errorf("%s: the sandbox must be greyed and ticked, got off=%v on=%v", tc.name, sb.Off, sb.On)
		}
		if c := tc.f.Rows[1]; !c.Off[0] || c.On[0] || c.OffWhy[addonIOS] != hostadb.IOSWhy {
			t.Errorf("%s: ios must stay greyed with its reason, got off=%v on=%v why=%q",
				tc.name, c.Off, c.On, c.OffWhy[addonIOS])
		}
	}
}

func TestGateAddonsKeepsTheSandboxCompulsory(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: rowExecution, Options: []string{addonHost, addonSandbox}, Multi: true, On: []bool{false, true},
	}}}
	gateAddons(f)
	r := f.Rows[0]
	if !r.Off[1] || !r.On[1] {
		t.Errorf("the sandbox is compulsory: greyed and ticked, got off=%v on=%v", r.Off, r.On)
	}
	if r.Reason != "" {
		t.Errorf("a box greyed on every run must stay out of the inline reason, got %q", r.Reason)
	}
	if !strings.Contains(r.OffWhy[addonSandbox], "Mount the current git project") {
		t.Errorf("the compulsory sandbox must say why, got %q", r.OffWhy[addonSandbox])
	}
	if got := selectedAddons(f); !slices.Equal(got, []string{addonSandbox}) {
		t.Errorf("a compulsory sandbox must still be reported as selected, got %v", got)
	}
}

func TestTheDockerAddonStartsChecked(t *testing.T) {
	t.Parallel()
	opts := []string{"browser", addonSandbox}
	got := (&Params{}).addonDefaults(opts)
	if diff := cmp.Diff([]bool{false, true}, got); diff != "" {
		t.Errorf("first-run defaults mismatch (-want +got):\n%s", diff)
	}
	// A remembered answer is authoritative in both directions.
	remembered := &Params{Addons: []string{"browser"}, AddonsAnswered: true}
	if diff := cmp.Diff([]bool{true, false}, remembered.addonDefaults(opts)); diff != "" {
		t.Errorf("remembered choice mismatch (-want +got):\n%s", diff)
	}
}

func TestNormalizeAddonsDropsTheRetiredSidecarNames(t *testing.T) {
	t.Parallel()
	got := normalizeAddons([]string{"browser", "dind", "docker (dind)", addonSandbox, "tui (this session)", "android (host emulator)", "host chrome (CDP)"})
	if diff := cmp.Diff([]string{"browser", addonSandbox, addonTUI, addonAndroid, addonHostCDP}, got); diff != "" {
		t.Errorf("normalizeAddons() mismatch (-want +got):\n%s", diff)
	}
}

// (_spec/internal/agentsettings/choice-cache.puml). Applying it headlessly let
func TestCacheOnlyAppliesWhereThereIsAPromptToSeed(t *testing.T) {
	for _, tc := range []struct {
		name           string
		printOnly, tty bool
		wizard         string
		want           bool
	}{
		{name: "interactive", tty: true, want: true},
		{name: "no tty", tty: false, want: false},
		{name: "dry run", printOnly: true, tty: true, want: false},
		{name: "wizard off", tty: true, wizard: "off", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROVEO_WIZARD", tc.wizard)
			if got := cacheApplies(tc.printOnly, tc.tty); got != tc.want {
				t.Errorf("cacheApplies(printOnly=%v, tty=%v) = %v, want %v",
					tc.printOnly, tc.tty, got, tc.want)
			}
		})
	}
}

// The seeding itself must not overwrite an axis the operator stated on the
// command line — the cache fills gaps, it does not out-rank a flag.
func TestSeedFromCacheYieldsToExplicitFlags(t *testing.T) {
	t.Parallel()
	cached := agentsettings.Choice{Credentials: "forward", Addons: []string{"browser"}}
	lookup := func(string) string { return "" }

	stated := Params{Credentials: "broker", CredsSet: true}
	stated.seedFromCache(cached, lookup, false)
	if stated.Credentials != "broker" {
		t.Errorf("an explicit flag was overwritten: credentials=%q", stated.Credentials)
	}

	unstated := Params{Credentials: "broker"}
	unstated.seedFromCache(cached, lookup, false)
	if unstated.Credentials != "forward" {
		t.Errorf("an unstated axis was not seeded: credentials=%q", unstated.Credentials)
	}
	if !hasAddon(unstated.Addons, "browser") {
		t.Errorf("remembered add-ons were not seeded: %v", unstated.Addons)
	}
}

func TestEgressRowShowsTheHostBaseline(t *testing.T) {
	t.Run("sbx shows the host baseline, read-only", func(t *testing.T) {
		orig := policyBaseline
		t.Cleanup(func() { policyBaseline = orig })
		policyBaseline = func() (string, bool) { return sbx.BaselineBalanced, true }

		got := egressRow()
		if !got.Locked {
			t.Error("the sbx baseline is host-wide and proveo does not set it; the row must be locked")
		}
		if len(got.Options) != 3 || got.Options[0] != sbx.BaselineAllowAll {
			t.Errorf("sbx must show sbx's own three baselines, got %v", got.Options)
		}
		if got.Options[got.Selected] != sbx.BaselineBalanced {
			t.Errorf("selected %q, want the host's actual baseline %q",
				got.Options[got.Selected], sbx.BaselineBalanced)
		}
		if !slices.Equal(got.Cols, []int{0, 2, 3}) {
			t.Errorf("egress cols = %v, want allow-all, gap, balanced, deny-all", got.Cols)
		}
	})

	t.Run("the change hint is uniform and runnable", func(t *testing.T) {
		orig := policyBaseline
		t.Cleanup(func() { policyBaseline = orig })

		var seen []string
		for _, b := range append(sbx.Baselines(), "") {
			policyBaseline = func() (string, bool) { return b, b != "" }
			r := egressRow()
			if !strings.Contains(r.Reason, "sbx policy reset && sbx policy init") {
				t.Errorf("baseline %q: hint must include the reset, got %q", b, r.Reason)
			}
			if !strings.Contains(r.Reason, "host-wide, not per-run") {
				t.Errorf("baseline %q: hint must say the baseline is the HOST's — offering it "+
					"per-run would imply it is a property of this container, got %q", b, r.Reason)
			}
			seen = append(seen, r.Reason)
		}
		for _, got := range seen[1:] {
			if !strings.Contains(got, changeBaselineHint) {
				t.Errorf("the sample must be the same for every baseline, got %q", got)
			}
		}
	})

	t.Run("unreadable is never rendered as a posture", func(t *testing.T) {
		orig := policyBaseline
		t.Cleanup(func() { policyBaseline = orig })
		policyBaseline = func() (string, bool) { return "", false }

		got := egressRow()
		for _, b := range sbx.Baselines() {
			for _, o := range got.Options {
				if o == b {
					t.Errorf("an unreadable baseline must not name %q — the operator would "+
						"trust a boundary that may not exist", b)
				}
			}
		}
	})
}

func claudecodeMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "claudecode", Subscription: true, Env: []manifest.EnvVar{
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
			{Name: "ANTHROPIC_API_KEY", Secret: true},
		},
	}
}

// blankedHome writes the macOS husk: tokens cleared, stamps intact.
func blankedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	husk := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"refreshTokenExpiresAt":%d}}`,
		time.Now().Add(20*24*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(husk), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// SPEC: _spec/_plans/init-credential-provisioning.puml
// A run that starts without a credential says ONE line. The screenful of routes
// it used to print — obtain, export, sbx setup, secret set, set-custom, /login —
// was read as a wall of text and skipped, which is worse than saying less.
func TestTheNoCredentialNoticeIsOneLine(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "run `proveo init` to persist credentials between runs") {
		t.Error("the notice no longer names `proveo init`, the one place a credential is provisioned")
	}
	for _, gone := range []string{"PrintSubscriptionAuthHints", "SandboxAuthRoutes", "SandboxAuthRefusal"} {
		if strings.Contains(body, gone) {
			t.Errorf("%s is back on the run path — it prints a screenful where one line is wanted", gone)
		}
	}
}

func TestThePostureNamesTheCredentialRouteThatWillHappen(t *testing.T) {
	t.Parallel()
	rs := &Spec{Man: manifest.Manifest{Name: "cursor"}}
	if got := credentialsPosture(rs, &Params{Credentials: "forward"}); !strings.HasPrefix(got, "forward") {
		t.Errorf("forward puts the key in the sandbox env, but the posture says %q", got)
	}
	for _, creds := range []string{"", "broker"} {
		if got := credentialsPosture(rs, &Params{Credentials: creds}); got != "broker (sbx proxy)" {
			t.Errorf("credentials %q: posture %q, want broker (sbx proxy)", creds, got)
		}
	}
}

func TestHostCDPAddonIsOfferedByCapabilityAndNeedsSbx(t *testing.T) {
	t.Parallel()
	cdp := manifest.Manifest{Capabilities: manifest.Capabilities{HostBrowser: manifest.HostBrowserCDP}}
	opts := interfaceOptions(cdp)
	if !slices.Contains(opts, addonHostCDP) {
		t.Errorf("hostBrowser: cdp ⇒ %v; want %q", opts, addonHostCDP)
	}
	if opts := interfaceOptions(manifest.Manifest{Capabilities: manifest.Capabilities{HostBrowser: "claude-in-chrome"}}); slices.Contains(opts, "claude-in-chrome") {
		t.Errorf("the retired Claude-in-Chrome bridge is offered again: %v", opts)
	}

	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonHostCDP}, Multi: true, On: []bool{true}},
	}}
	gateAddons(f)
	if r := f.Rows[0]; r.Off[0] {
		t.Errorf("with sbx the add-on must be live: reason=%q", r.Reason)
	}
}

func TestAddonFlagResolvesAliasesAndRefusesTheUnoffered(t *testing.T) {
	t.Parallel()
	cdp := manifest.Manifest{Capabilities: manifest.Capabilities{HostBrowser: manifest.HostBrowserCDP}}
	p := Params{Target: "hermes", Addons: []string{"host-chrome", "host-chrome"}, AddonsSet: true}
	if err := p.resolveAddonFlags(cdp); err != nil || !slices.Equal(p.Addons, []string{addonHostCDP}) || !p.AddonsAnswered {
		t.Errorf("addons = %v answered=%v err=%v", p.Addons, p.AddonsAnswered, err)
	}
	bad := Params{Target: "hermes", Addons: []string{"android"}, AddonsSet: true}
	if err := bad.resolveAddonFlags(cdp); err == nil || !strings.Contains(err.Error(), addonHostCDP) {
		t.Errorf("an add-on the harness does not offer must be refused and the offered ones named: %v", err)
	}
}

func TestTheCacheDoesNotOverrideFlaggedAddons(t *testing.T) {
	t.Parallel()
	p := Params{Addons: []string{addonHostCDP}, AddonsSet: true}
	p.seedFromCache(agentsettings.Choice{Addons: []string{"browser"}}, func(string) string { return "" }, false)
	if !slices.Equal(p.Addons, []string{addonHostCDP}) {
		t.Errorf("addons = %v, want the flagged host chrome", p.Addons)
	}
}

func TestThePickerPromisesADaemonOnlyWhereTheManifestDoes(t *testing.T) {
	t.Parallel()
	off := false
	if got := addonHelpFor(manifest.Manifest{Name: "h"})[addonSandbox]; !strings.Contains(got, "Docker daemon") {
		t.Errorf("a default manifest's sandbox help = %q, want the daemon named", got)
	}
	got := addonHelpFor(manifest.Manifest{Name: "h", Daemon: &off})
	if strings.Contains(got[addonSandbox], "Docker daemon") && !strings.Contains(got[addonSandbox], "no Docker daemon") {
		t.Errorf("a `daemon: false` manifest's sandbox help = %q, want no daemon promised", got[addonSandbox])
	}
	if addonHelp[addonSandbox] == got[addonSandbox] {
		t.Error("addonHelpFor must not edit the shared addonHelp map")
	}
	for k, v := range addonHelp {
		if k != addonSandbox && got[k] != v {
			t.Errorf("help for %q changed to %q", k, got[k])
		}
	}
}
