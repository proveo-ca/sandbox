package run

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestAddonOptionsNeverOffersBothDaemons(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.DockerMode{manifest.DockerNone, manifest.DockerSbx, manifest.DockerDind} {
		man := manifest.Manifest{Name: "h", Docker: mode, Images: map[string]string{"h": "proveo/h:latest"}}
		opts := addonOptions(man)
		if slices.Contains(opts, addonSandbox) && slices.Contains(opts, addonDind) {
			t.Errorf("docker %q offered both daemons: %v", mode, opts)
		}
		switch mode {
		case manifest.DockerSbx:
			if !slices.Contains(opts, addonSandbox) {
				t.Errorf("docker: sbx must offer %q, got %v", addonSandbox, opts)
			}
		case manifest.DockerDind:
			if !slices.Contains(opts, addonDind) {
				t.Errorf("docker: dind must offer %q, got %v", addonDind, opts)
			}
		default:
			if len(opts) != 0 {
				t.Errorf("a harness with no docker mode must be offered none, got %v", opts)
			}
		}
	}
}

func TestGateAddonsEgressStillGatesWithoutSandbox(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Options: []string{addonDind}, Multi: true, On: []bool{false},
	}}}
	gateAddons(f, "firewall", "inject", "", "")
	if !f.Rows[0].Off[0] {
		t.Fatal("firewall+inject must still disable dind")
	}
	if f.Rows[0].Reason != addonDind+" needs egress open + credentials forward" {
		t.Errorf("reason = %q", f.Rows[0].Reason)
	}
	gateAddons(f, "open", "forward", "", "")
	if f.Rows[0].Off[0] {
		t.Error("open+forward on a docker: dind harness must leave the add-on enabled")
	}
}

func TestEvidenceRowDefaultsToVerbose(t *testing.T) {
	t.Parallel()
	r := evidenceRow((Params{}).evidenceOrDefault())
	if r.Label != evidenceLabel || !r.Multi {
		t.Fatalf("row = %+v, want a checkbox row labelled %q", r, evidenceLabel)
	}
	if len(r.Options) != 2 || r.Options[0] != EvidenceDefault || r.Options[1] != EvidenceVerbose {
		t.Fatalf("options = %v, want [%s %s]", r.Options, EvidenceDefault, EvidenceVerbose)
	}
	if r.On[0] || !r.On[1] {
		t.Errorf("On = %v, want verbose ticked and default clear", r.On)
	}
	if got := evidenceRow(EvidenceDefault); !got.On[0] || got.On[1] {
		t.Errorf("a remembered 'default' must tick default only, got %v", got.On)
	}
}

// The two boxes are one answer wearing checkbox glyphs: ticking one clears the
// other, and clearing both reads as default rather than as a third state.
func TestGateEvidenceKeepsTheLevelsExclusive(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "add-ons", Options: []string{"browser"}, Multi: true, On: []bool{true}},
		evidenceRow(EvidenceVerbose),
	}}
	// Ticking "default" (index 0) must clear the verbose box.
	f.Rows[1].Selected, f.Rows[1].On[0] = 0, true
	gateEvidence(f)
	if f.Rows[1].On[1] {
		t.Errorf("verbose survived a tick on default: %v", f.Rows[1].On)
	}
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != EvidenceDefault {
		t.Errorf("evidence = %q, want %q", got, EvidenceDefault)
	}
	// Un-ticking the only box leaves nothing selected, which is still default.
	f.Rows[1].On[0] = false
	gateEvidence(f)
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != EvidenceDefault {
		t.Errorf("empty row = %q, want %q", got, EvidenceDefault)
	}
	// Back to verbose, and the other row must be untouched throughout.
	f.Rows[1].Selected, f.Rows[1].On[1] = 1, true
	gateEvidence(f)
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != EvidenceVerbose {
		t.Errorf("evidence = %q, want %q", got, EvidenceVerbose)
	}
	if !f.Rows[0].On[0] {
		t.Error("gateEvidence must not reach into the add-ons row")
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
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
			"ANTHROPIC_API_KEY":       "sk-value",
			"ANTHROPIC_BASE_URL":      "https://api.anthropic.com",
		}[k]
	}
	in := sandbox.Input{
		Target:         "claudecode",
		Image:          "proveo/claudecode:latest",
		Extra:          []string{"--verbose"},
		Evidence:       EvidenceDefault,
		Forwards:       false,
		SandboxAddonOn: true,
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

	wantSecrets := map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": false, "ANTHROPIC_API_KEY": false}
	if len(secrets) != len(wantSecrets) {
		t.Fatalf("secrets = %v, want exactly the declared+provider keys %v", secrets, wantSecrets)
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

	if len(kit.Permissions.Network.Allow) == 0 {
		t.Error("allowlist must include at least the manifest hosts")
	}
	sawManifestHost := false
	for _, d := range kit.Permissions.Network.Allow {
		if d == "api.anthropic.com" || d == "statsig.anthropic.com" {
			sawManifestHost = true
		}
	}
	if !sawManifestHost {
		t.Errorf("allowlist missing manifest hosts: %v", kit.Permissions.Network.Allow)
	}
	// Credentials are NOT declared here any more. The built-in agent's own kit
	// declares service "anthropic", and a mixin repeating it is rejected outright
	// ("defined in both") — sbx's proxy does the injection either way.
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin: a sandbox kit declares an agent sbx will not register", kit.Kind)
	}
	if kit.Setup == nil || len(kit.Setup.Startup) == 0 {
		t.Error("the Kit must carry the seed step, or nothing composes subagents under sbx")
	}

	if cfg.Name != "proveo-1-2" || cfg.Image != "proveo/claudecode:latest" {
		t.Errorf("run config name/image = %q/%q", cfg.Name, cfg.Image)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "--verbose" {
		t.Errorf("command = %v, want agent args passed through", cfg.Command)
	}
}

func TestReviewAvailabilityGreysReviewOnSandboxBackend(t *testing.T) {
	row := choiceui.Row{Label: "egress", Options: []string{"open", "review"}, Selected: 1}
	greyed := reviewAvailability(row, true)
	if !greyed.Off[1] {
		t.Error("sbx backend must grey out review")
	}
	if greyed.Selected == 1 {
		t.Errorf("selection must move off review, got %d", greyed.Selected)
	}
	if !strings.Contains(greyed.Reason, "sandbox") {
		t.Errorf("reason = %q, want it to name the sandbox backend", greyed.Reason)
	}
	keep := reviewAvailability(choiceui.Row{Label: "egress", Options: []string{"open", "review"}}, false)
	if runtime.GOOS == "linux" && len(keep.Off) != 0 {
		t.Errorf("linux host without sbx must leave review enabled, got Off=%v", keep.Off)
	}
}

func TestSandboxSpecForwardsCredentialsWhenTheHarnessRequiresIt(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{"CURSOR_API_KEY": "key-value"}[k]
	}
	in := sandbox.Input{
		Target:         "cursor",
		Image:          "proveo/cursor:latest",
		Evidence:       EvidenceDefault,
		Forwards:       true,
		SandboxAddonOn: true,
		Man: manifest.Manifest{
			Name: "cursor",
			Env:  []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
			Capabilities: manifest.Capabilities{
				Hosts:       []string{"api2.cursor.sh"},
				Egress:      []string{"open"},
				Credentials: []string{"forward"},
			},
		},
		Sid:    "proveo-cursor-1",
		Lookup: lookup,
	}

	cfg, kit, secrets := sandbox.Spec(in)

	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none: forward mode must not route through sbx secret set", secrets)
	}
	// The Kit never declares credentials at all now — brokered or forwarded, the
	// built-in agent owns that service and a mixin repeating it is rejected.
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin", kit.Kind)
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
		t.Errorf("cfg.Env = %v, want a bare CURSOR_API_KEY forwarded from the host", cfg.Env)
	}
}

func TestSandboxSpecBrokeredCredentialsStayHostSide(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value"}[k]
	}
	in := sandbox.Input{
		Target:         "claudecode",
		Image:          "proveo/claudecode:latest",
		Evidence:       EvidenceDefault,
		Forwards:       false,
		SandboxAddonOn: true,
		Man: manifest.Manifest{
			Name: "claudecode",
			Env:  []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		},
		Sid:    "proveo-cc-1",
		Lookup: lookup,
	}

	_, kit, secrets := sandbox.Spec(in)

	if len(secrets) == 0 {
		t.Fatal("secrets = none, want the declared secret injected host-side outside forward mode")
	}
	// The secret still goes to sbx's store host-side, but the Kit no longer NAMES
	// it: the built-in agent declares service "anthropic" itself, and a mixin
	// repeating it is rejected ("defined in both").
	var named bool
	for _, kv := range secrets {
		if kv[0] == "CLAUDE_CODE_OAUTH_TOKEN" {
			named = true
		}
	}
	if !named {
		t.Errorf("secrets = %v, want the declared credential injected host-side", secrets)
	}
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin", kit.Kind)
	}
}

func TestAddonOptionsOffersTheDockerSandbox(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:   "claudecode",
		Docker: manifest.DockerSbx,
		Images: map[string]string{"claudecode": "proveo/claudecode:latest", "claudecode-browser": "proveo/claudecode-browser:latest"},
	}
	got := addonOptions(man)
	want := []string{"browser", addonSandbox}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("addonOptions() mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "opencode", Docker: manifest.DockerDind}); !slices.Contains(opts, addonDind) || slices.Contains(opts, addonSandbox) {
		t.Errorf("a docker: dind harness must not be offered the sandbox: %v", opts)
	}
}

// The two browsers are different things — a Chromium inside the sandbox versus
// the operator's own Chrome over the bridge — so a harness that has both is
// offered both, and one that declares no host-browser client is never sold a
// bridge it cannot use.
func TestAddonOptionsOffersTheHostBrowserOnlyToAHarnessWithAClient(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Docker:       manifest.DockerSbx,
		Images:       map[string]string{"claudecode": "proveo/claudecode:latest", "claudecode-browser": "proveo/claudecode-browser:latest"},
		Capabilities: manifest.Capabilities{HostBrowser: "claude-in-chrome"},
	}
	want := []string{"browser", addonChrome, addonSandbox}
	if diff := cmp.Diff(want, addonOptions(man)); diff != "" {
		t.Errorf("addonOptions() mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "cursor", Docker: manifest.DockerSbx}); slices.Contains(opts, addonChrome) {
		t.Errorf("no hostBrowser capability, yet offered %q: %v", addonChrome, opts)
	}
	// Off by default: the picker never pre-ticks a box that hands the agent the
	// operator's logged-in browser.
	p := &Params{}
	if on := p.addonDefaults(want); on[1] {
		t.Errorf("%q must start unticked, got %v", addonChrome, on)
	}
	if on := (&Params{Addons: []string{addonChrome}, AddonsAnswered: true}).addonDefaults(want); !on[1] {
		t.Errorf("a remembered %q must come back ticked, got %v", addonChrome, on)
	}
}

// The host-browser box is gated three ways, each re-evaluated on every toggle:
// the host preflight (Chrome + native host, a /login session), the tier (open +
// forward is the only one with a route to the host), and the sandbox box (a VM
// cannot name the host). The reason on the row names the one that applied.
func TestGateAddonsGreysTheHostBrowserForEachReason(t *testing.T) {
	t.Parallel()
	row := func(sandboxOn bool) *choiceui.Form {
		return &choiceui.Form{Rows: []choiceui.Row{{
			Label: "add-ons", Options: []string{addonChrome, addonSandbox}, Multi: true, On: []bool{true, sandboxOn},
		}}}
	}
	f := row(false)
	gateAddons(f, "open", "forward", "", "no Claude in Chrome native host on this machine")
	if !f.Rows[0].Off[0] || f.Rows[0].On[0] || !strings.Contains(f.Rows[0].Reason, "native host") {
		t.Errorf("host preflight failure must grey+untick with its reason: off=%v on=%v reason=%q", f.Rows[0].Off, f.Rows[0].On, f.Rows[0].Reason)
	}

	f = row(true)
	gateAddons(f, "open", "forward", "", "")
	if !f.Rows[0].Off[0] || f.Rows[0].On[0] || !strings.Contains(f.Rows[0].Reason, "untick docker (sandbox)") {
		t.Errorf("a ticked sandbox must grey the host browser: off=%v on=%v reason=%q", f.Rows[0].Off, f.Rows[0].On, f.Rows[0].Reason)
	}
	if f.Rows[0].Off[1] || !f.Rows[0].On[1] {
		t.Errorf("the sandbox box itself must stay ticked and selectable: off=%v on=%v", f.Rows[0].Off, f.Rows[0].On)
	}

	f = row(false)
	gateAddons(f, "allowlist", "forward", "", "")
	if !f.Rows[0].Off[0] || !strings.Contains(f.Rows[0].Reason, "egress open + credentials forward") {
		t.Errorf("an intercepting tier must grey the host browser: off=%v reason=%q", f.Rows[0].Off, f.Rows[0].Reason)
	}

	f = row(false)
	gateAddons(f, "open", "forward", "", "")
	if f.Rows[0].Off[0] || !f.Rows[0].On[0] || f.Rows[0].Reason != "" {
		t.Errorf("open + forward, sandbox off, host ready: the box must be live: off=%v on=%v reason=%q", f.Rows[0].Off, f.Rows[0].On, f.Rows[0].Reason)
	}

	// A sandbox the host cannot run is unticked by its own gate, so it must not
	// count as "ticked" against the host browser.
	f = row(true)
	gateAddons(f, "open", "forward", "sbx CLI not found on PATH", "")
	if f.Rows[0].Off[0] || !f.Rows[0].On[0] {
		t.Errorf("an unavailable sandbox must not grey the host browser: off=%v on=%v reason=%q", f.Rows[0].Off, f.Rows[0].On, f.Rows[0].Reason)
	}
	if !strings.Contains(f.Rows[0].Reason, "docker sandbox: sbx CLI not found") {
		t.Errorf("the sandbox reason must still be on the row: %q", f.Rows[0].Reason)
	}
}

// Claude Code's own rule, mirrored: an env-var or setup-token session gets no
// Chrome integration, so the preflight names the variable rather than letting the
// operator discover "Disabled" inside the sandbox.
func TestChromeUnavailableNamesTheCredentialThatDisablesIt(t *testing.T) {
	t.Parallel()
	lookup := func(k string) string {
		if k == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "sk-ant-oat01-…"
		}
		return ""
	}
	why := chromeUnavailable(lookup)
	if !strings.Contains(why, "CLAUDE_CODE_OAUTH_TOKEN") || !strings.Contains(why, "/login") {
		t.Errorf("why = %q", why)
	}
}

func TestSandboxAddonIsOnUntilAnAnswerSaysOtherwise(t *testing.T) {
	t.Parallel()
	if !(&Params{}).sandboxAddonOn() {
		t.Error("a first run must take the sandbox backend without being asked")
	}
	if !(&Params{Addons: []string{addonSandbox}, AddonsAnswered: true}).sandboxAddonOn() {
		t.Error("a remembered yes must keep the sandbox on")
	}
	if (&Params{Addons: []string{"browser"}, AddonsAnswered: true}).sandboxAddonOn() {
		t.Error("a remembered answer WITHOUT the add-on means the operator turned it off")
	}
	if (&Params{AddonsAnswered: true}).sandboxAddonOn() {
		t.Error("an empty remembered answer is still an answer — the sandbox stays off")
	}
}

func TestGateAddonsGreysTheSandboxWhenTheHostCannotRunIt(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Options: []string{"browser", addonSandbox}, Multi: true, On: []bool{false, true},
	}}}
	gateAddons(f, "open", "forward", "sbx CLI not found on PATH", "")
	r := f.Rows[0]
	if !r.Off[1] {
		t.Fatal("the sandbox add-on must be greyed out when sbx is unavailable")
	}
	if !strings.Contains(r.Reason, "sbx CLI not found on PATH") {
		t.Errorf("reason = %q, want the availability reason", r.Reason)
	}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Errorf("a greyed add-on must not count as selected, got %v", got)
	}
	f.Rows[0].Off, f.Rows[0].Reason = nil, ""
	gateAddons(f, "open", "forward", "", "")
	if f.Rows[0].Off[1] {
		t.Error("an available sbx must leave the add-on checkable")
	}
}

func TestGateReviewFollowsTheSandboxAddon(t *testing.T) {
	t.Parallel()
	row := choiceui.Row{Label: "egress", Options: []string{"open", "review"}, Selected: 1}
	f := &choiceui.Form{Rows: []choiceui.Row{row}}
	gateReview(f, true)
	if !f.Rows[0].Off[1] {
		t.Fatal("review must be greyed out while the sandbox add-on is on")
	}
	if f.Rows[0].Selected == 1 {
		t.Error("selection must move off a greyed option")
	}
	if !strings.Contains(f.Rows[0].Reason, "docker sandbox backend") {
		t.Errorf("reason = %q", f.Rows[0].Reason)
	}
	gateReview(f, false)
	if ok, _ := dockeregress.ReviewSupported(func(string) string { return "" }); ok && f.Rows[0].Off[1] {
		t.Error("turning the add-on off must hand the review tier back")
	}
}

func TestBothDockerAddonsStartChecked(t *testing.T) {
	t.Parallel()
	opts := []string{"browser", addonSandbox, addonDind}
	got := (&Params{}).addonDefaults(opts)
	if diff := cmp.Diff([]bool{false, true, true}, got); diff != "" {
		t.Errorf("first-run defaults mismatch (-want +got):\n%s", diff)
	}
	// A remembered answer is authoritative in both directions.
	remembered := &Params{Addons: []string{"browser"}, AddonsAnswered: true}
	if diff := cmp.Diff([]bool{true, false, false}, remembered.addonDefaults(opts)); diff != "" {
		t.Errorf("remembered choice mismatch (-want +got):\n%s", diff)
	}
}

func TestNormalizeAddonsUpgradesTheRememberedDindName(t *testing.T) {
	t.Parallel()
	got := normalizeAddons([]string{"browser", "dind"})
	if diff := cmp.Diff([]string{"browser", addonDind}, got); diff != "" {
		t.Errorf("normalizeAddons() mismatch (-want +got):\n%s", diff)
	}
}

// The DLP's on-provider exemption cannot be derived from detected keys alone. A
// subscription harness authenticates INSIDE the sandbox, so nothing is
// detectable host-side, yet the token it mints there still has to reach the
// vendor — the manifest's declared providers are the only statement of where
// that is. Deriving the set from detection alone made the exemption empty for
// exactly the harness that needs it most.
func TestPolicyProviderHostsCoversDeclaredAndDetected(t *testing.T) {
	t.Parallel()
	subscription := manifest.Capabilities{Providers: []string{"anthropic"}}

	if got := policyProviderHosts(nil, subscription); len(got) == 0 {
		t.Error("a subscription harness with no host-side key got no provider hosts")
	}
	if got := policyProviderHosts([]string{"anthropic"}, manifest.Capabilities{}); len(got) == 0 {
		t.Error("a detected provider with no declared capability got no provider hosts")
	}
	// Declared and detected overlap on the common path; the union must not
	// double-list a host (the policy would still match, but the plan reads twice).
	got := policyProviderHosts([]string{"anthropic"}, subscription)
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Errorf("duplicate host %q in %v", h, got)
		}
		seen[h] = true
	}
	if len(got) == 0 {
		t.Fatal("union of declared and detected must not be empty")
	}
}

// The cache seeds a prompt and is never an authority of its own, so a run with
// no prompt to seed takes the manifest default
// (_spec/internal/agentsettings/choice-cache.puml). Applying it headlessly let
// the last interactive session decide a later run's security posture: an e2e run
// that asked for the default `--credentials broker` silently got `forward`, and
// with it a `browser` image variant it never selected.
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
	cached := agentsettings.Choice{Egress: "open", Credentials: "forward", Addons: []string{"browser"}}
	lookup := func(string) string { return "" }

	stated := Params{Mode: "allowlist", Credentials: "broker", ModeSet: true, CredsSet: true}
	stated.seedFromCache(cached, lookup, false)
	if stated.Mode != "allowlist" || stated.Credentials != "broker" {
		t.Errorf("explicit flags were overwritten: mode=%q credentials=%q", stated.Mode, stated.Credentials)
	}

	unstated := Params{Mode: "allowlist", Credentials: "broker"}
	unstated.seedFromCache(cached, lookup, false)
	if unstated.Mode != "open" || unstated.Credentials != "forward" {
		t.Errorf("unstated axes were not seeded: mode=%q credentials=%q", unstated.Mode, unstated.Credentials)
	}
	if !hasAddon(unstated.Addons, "browser") {
		t.Errorf("remembered add-ons were not seeded: %v", unstated.Addons)
	}
}

// The egress axis must name what actually governs the backend in front of the
// operator. On docker that is proveo's tier; on sbx the tier is inert — the Kit
// allowlist is derived from capabilities and providers, never from the tier — and
// the real lever is sbx's host-wide baseline, which proveo reports but never sets.
func TestEgressRowShowsWhatGovernsEachBackend(t *testing.T) {
	man := manifest.Manifest{}

	t.Run("docker keeps proveo's tiers selectable", func(t *testing.T) {
		got := egressRow(man, "open", false)
		if got.Locked {
			t.Error("the docker tier is a real choice and must not be locked")
		}
		if len(got.Options) == 0 || got.Options[0] != "open" {
			t.Errorf("docker must offer proveo's canonical tiers, got %v", got.Options)
		}
	})

	t.Run("sbx shows the host baseline, read-only", func(t *testing.T) {
		orig := policyBaseline
		t.Cleanup(func() { policyBaseline = orig })
		policyBaseline = func() (string, bool) { return sbx.BaselineBalanced, true }

		got := egressRow(man, "allowlist", true)
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
	})

	// One sample, identical for every baseline, and it must carry the reset: `sbx
	// policy init` on its own is rejected once the host is initialized, so a hint
	// without it would print a command that fails.
	t.Run("the change hint is uniform and runnable", func(t *testing.T) {
		orig := policyBaseline
		t.Cleanup(func() { policyBaseline = orig })

		var seen []string
		for _, b := range append(sbx.Baselines(), "") {
			policyBaseline = func() (string, bool) { return b, b != "" }
			r := egressRow(man, "allowlist", true)
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

		got := egressRow(man, "allowlist", true)
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

func TestSandboxAddonIsGreyedAndUntickedWhenUnavailable(t *testing.T) {
	t.Parallel()
	row := choiceui.Row{
		Label: "add-ons", Multi: true,
		Options: []string{"browser", addonSandbox},
		On:      []bool{true, true}, // as addonDefaults leaves it: sandbox pre-ticked
	}
	f := &choiceui.Form{Rows: []choiceui.Row{row}}

	gateAddons(f, "open", "forward", "PROVEO_SBX is off", "")

	got := f.Rows[0]
	var i int
	for j, o := range got.Options {
		if o == addonSandbox {
			i = j
		}
	}
	if !got.Off[i] {
		t.Error("an unavailable sandbox add-on must be greyed")
	}
	if got.On[i] {
		t.Error("a greyed add-on must also be unticked: a ticked box reads as the run's posture")
	}
	if !strings.Contains(got.Reason, "PROVEO_SBX is off") {
		t.Errorf("the row must name why, got %q", got.Reason)
	}
	// An available backend leaves the operator's choice alone.
	f2 := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Multi: true,
		Options: []string{addonSandbox}, On: []bool{true},
	}}}
	gateAddons(f2, "open", "forward", "", "")
	if f2.Rows[0].Off[0] || !f2.Rows[0].On[0] {
		t.Error("an available sandbox add-on must stay selectable and ticked")
	}
}

func TestSbxSuppliesCredentialOnlyOnTheBackendThatUsesIt(t *testing.T) {
	sbxMan := manifest.Manifest{
		Name:         "claudecode",
		Subscription: true,
		Docker:       manifest.DockerSbx,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
	}
	dockerMan := sbxMan
	dockerMan.Docker = manifest.DockerDind
	apiMan := sbxMan
	apiMan.Subscription = false

	for _, c := range []struct {
		name    string
		man     manifest.Manifest
		p       Params
		sbxOK   bool
		sbxOff  bool
		want    bool
		because string
	}{
		{name: "the case that was broken", man: sbxMan, sbxOK: true, want: true,
			because: "an sbx subscription run: the store IS where its credential lives"},
		{name: "docker backend", man: dockerMan, sbxOK: true, want: false,
			because: "the proveo home is the credential there; sbx's store is not consulted"},
		{name: "not a subscription harness", man: apiMan, sbxOK: true, want: false,
			because: "an API-key harness authenticates from the env, not from a login"},
		{name: "review mode", man: sbxMan, p: Params{Mode: "review"}, sbxOK: true, want: false,
			because: "review runs on docker+egress, so it never reaches sbx's store"},
		{name: "sbx unavailable", man: sbxMan, sbxOK: false, want: false,
			because: "a store proveo cannot reach cannot be the reason to launch"},
		{name: "PROVEO_SBX=off", man: sbxMan, sbxOK: true, sbxOff: true, want: false,
			because: "the knob pins docker+egress; the backend decides the store"},
		{name: "sandbox add-on unchecked", man: sbxMan,
			p:     Params{Addons: []string{}, AddonsAnswered: true},
			sbxOK: true, want: false,
			because: "an answered picker that turned the sandbox off runs on docker"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.sbxOff {
				t.Setenv("PROVEO_SBX", "off")
			} else {
				t.Setenv("PROVEO_SBX", "")
			}
			p := c.p
			if got := sbxSuppliesCredential(c.man, &p, c.sbxOK); got != c.want {
				t.Errorf("sbxSuppliesCredential = %v, want %v (%s)", got, c.want, c.because)
			}
		})
	}
}
