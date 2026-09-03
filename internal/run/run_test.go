package run

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
		opts := addonOptions(man, rowExecution)
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
			// "host" alone: the plane still states what it excludes, and there is no
			// daemon to offer beside it.
			if !slices.Equal(opts, []string{addonHost}) {
				t.Errorf("a harness with no docker mode must be offered no daemon, got %v", opts)
			}
		}
	}
}

// The two planes answer different questions, and every box belongs to exactly
// one of them: an undifferentiated row put a Docker daemon beside a browser as
// though they were the same kind of choice.
func TestEachPlaneOffersOnlyItsOwnKindOfChoice(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "claudecode", Docker: manifest.DockerSbx,
		Images:       map[string]string{"claudecode": "i", "claudecode-browser": "i"},
		Capabilities: manifest.Capabilities{HostBrowser: "claude-in-chrome"},
	}
	exec := addonOptions(man, rowExecution)
	iface := addonOptions(man, rowInterface)
	if !slices.Equal(exec, []string{addonHost, addonSandbox}) {
		t.Errorf("execution = %v, want the excluded host then the declared daemon", exec)
	}
	if !slices.Equal(iface, []string{addonTUI, addonBrowser, addonChrome}) {
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
	gateAddons(f, "open", "forward", "", "")

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
	// "host" and the TUI state facts, so neither is reported as a run option. The
	// sandbox is greyed for the opposite reason — it is compulsory — and dropping
	// it here would report the run as taking the weaker backend.
	if !exec.Off[1] || !exec.On[1] {
		t.Error("an available sandbox must be greyed and TICKED: it is compulsory, not optional")
	}
	if got := selectedAddons(f); !slices.Equal(got, []string{addonSandbox, addonBrowser}) {
		t.Errorf("selectedAddons = %v, want the real toggles plus the compulsory sandbox", got)
	}
}

func TestGateAddonsEgressStillGatesWithoutSandbox(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: rowExecution, Options: []string{addonDind}, Multi: true, On: []bool{false},
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
	if r.Label != evidenceLabel || r.Multi {
		t.Fatalf("row = %+v, want a RADIO row labelled %q", r, evidenceLabel)
	}
	if len(r.Options) != 2 || r.Options[0] != EvidenceDefault || r.Options[1] != EvidenceVerbose {
		t.Fatalf("options = %v, want [%s %s]", r.Options, EvidenceDefault, EvidenceVerbose)
	}
	if r.Options[r.Selected] != EvidenceVerbose {
		t.Errorf("selected = %q, want verbose to be the default answer", r.Options[r.Selected])
	}
}

// The two levels are one answer, so they are one radio. They used to be a
// checkbox pair kept exclusive by a gate, which left "neither ticked" reachable
// and quietly meaning default — a state the picker could not explain.
func TestEvidenceIsOneAnswerNotTwoBoxes(t *testing.T) {
	t.Parallel()
	r := evidenceRow(EvidenceVerbose)
	if r.Multi {
		t.Error("evidence is one-of, so it must not be a checkbox row")
	}
	if r.Options[r.Selected] != EvidenceVerbose {
		t.Errorf("a remembered verbose must come back selected, got %q", r.Options[r.Selected])
	}
	if got := evidenceRow(EvidenceDefault); got.Options[got.Selected] != EvidenceDefault {
		t.Errorf("default must come back selected, got %q", got.Options[got.Selected])
	}
	// Cycling reaches the other level and nothing else: there is no third state.
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowInterface, Options: []string{addonBrowser}, Multi: true, On: []bool{true}},
		evidenceRow(EvidenceDefault),
	}}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[f.Selection(evidenceLabel)] = true
		f.Rows[1].Selected = (f.Rows[1].Selected + 1) % len(f.Rows[1].Options)
	}
	if len(seen) != 2 || !seen[EvidenceDefault] || !seen[EvidenceVerbose] {
		t.Errorf("the row reaches %v, want exactly the two levels", seen)
	}
	if !f.Rows[0].On[0] {
		t.Error("the evidence row must not reach into the add-ons row")
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
		Target:   "cursor",
		Image:    "proveo/cursor:latest",
		Evidence: EvidenceDefault,
		Forwards: true,
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
	got := addonOptions(man, rowExecution)
	want := []string{addonHost, addonSandbox}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("execution options mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "opencode", Docker: manifest.DockerDind}, rowExecution); !slices.Contains(opts, addonDind) || slices.Contains(opts, addonSandbox) {
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
	want := []string{addonTUI, addonBrowser, addonChrome}
	if diff := cmp.Diff(want, addonOptions(man, rowInterface)); diff != "" {
		t.Errorf("interface options mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "cursor", Docker: manifest.DockerSbx}, rowInterface); slices.Contains(opts, addonChrome) {
		t.Errorf("no hostBrowser capability, yet offered %q: %v", addonChrome, opts)
	}
	// Off by default: the picker never pre-ticks a box that hands the agent the
	// operator's logged-in browser.
	p := &Params{}
	if on := p.addonDefaults(want); on[2] {
		t.Errorf("%q must start unticked, got %v", addonChrome, on)
	}
	if on := (&Params{Addons: []string{addonChrome}, AddonsAnswered: true}).addonDefaults(want); !on[2] {
		t.Errorf("a remembered %q must come back ticked, got %v", addonChrome, on)
	}
}

// The host-browser box is gated three ways, each re-evaluated on every toggle:
// the host preflight (Chrome + native host, a /login session), the tier (open +
// forward is the only one with a route to the host), and the sandbox box (a VM
// cannot name the host). The reason on the row names the one that applied.
func TestGateAddonsGreysTheHostBrowserForEachReason(t *testing.T) {
	t.Parallel()
	// The two boxes live in DIFFERENT groups now — the daemon is an execution
	// choice and the browser an interface one — so the exclusion between them is
	// read across the form rather than along one slice. chrome() and sandbox()
	// name the rows so the assertions below say which plane they are about.
	row := func(sandboxOn bool) *choiceui.Form {
		return &choiceui.Form{Rows: []choiceui.Row{
			{Label: rowExecution, Options: []string{addonSandbox}, Multi: true, On: []bool{sandboxOn}},
			{Label: rowInterface, Options: []string{addonChrome}, Multi: true, On: []bool{true}},
		}}
	}
	chrome := func(f *choiceui.Form) *choiceui.Row { return &f.Rows[1] }
	sandbox := func(f *choiceui.Form) *choiceui.Row { return &f.Rows[0] }
	f := row(false)
	gateAddons(f, "open", "forward", "", "no Claude in Chrome native host on this machine")
	if c := chrome(f); !c.Off[0] || c.On[0] || !strings.Contains(c.Reason, "native host") {
		t.Errorf("host preflight failure must grey+untick with its reason: off=%v on=%v reason=%q", c.Off, c.On, c.Reason)
	}

	f = row(true)
	gateAddons(f, "open", "forward", "", "")
	if c := chrome(f); !c.Off[0] || c.On[0] || !strings.Contains(c.Reason, "PROVEO_SBX=0") {
		t.Errorf("a ticked sandbox must grey the host browser: off=%v on=%v reason=%q", c.Off, c.On, c.Reason)
	}
	// The escape the reason names is the env var, not the checkbox: an available
	// sandbox is greyed AND ticked, so there is no box left to untick.
	if sb := sandbox(f); !sb.Off[0] || !sb.On[0] {
		t.Errorf("the sandbox box itself must be greyed and ticked: off=%v on=%v", sb.Off, sb.On)
	}

	f = row(false)
	gateAddons(f, "allowlist", "forward", "no sbx", "")
	// sbxWhy != "" above is a host that cannot run sbx at all — the only way the
	// box is unticked. With sbx available the box is force-ticked by this very
	// call, and the host browser must be excluded on that same pass.
	if c := chrome(f); !c.Off[0] || !strings.Contains(c.Reason, "egress open + credentials forward") {
		t.Errorf("an intercepting tier must grey the host browser: off=%v reason=%q", c.Off, c.Reason)
	}

	f = row(false)
	gateAddons(f, "open", "forward", "no sbx", "")
	if c := chrome(f); c.Off[0] || !c.On[0] || c.Reason != "" {
		t.Errorf("open + forward, no sandbox to exclude it, host ready: the box must be live: off=%v on=%v reason=%q", c.Off, c.On, c.Reason)
	}

	// A sandbox the host cannot run is unticked by its own gate, so it must not
	// count as "ticked" against the host browser.
	f = row(true)
	gateAddons(f, "open", "forward", "sbx CLI not found on PATH", "")
	if c := chrome(f); c.Off[0] || !c.On[0] {
		t.Errorf("an unavailable sandbox must not grey the host browser: off=%v on=%v reason=%q", c.Off, c.On, c.Reason)
	}
	// And its own reason stays on its own row: the groups explain themselves
	// separately now.
	if sb := sandbox(f); !strings.Contains(sb.Reason, "docker sandbox: sbx CLI not found") {
		t.Errorf("the sandbox reason must be on the execution row: %q", sb.Reason)
	}
}

// Claude Code's own rule, mirrored: a bare setup-token session gets no Chrome
// integration, so the preflight names the variable rather than letting the
// operator discover "Disabled" inside the sandbox. The scope table itself lives in
// chromebridge; what this pins is that the preflight asks it, and asks it with the
// proveo home — the login that answers the question is a FILE in there, and
// dropping that argument is how the box ends up greyed for a session that works.
func TestChromeUnavailableNamesTheCredentialThatDisablesIt(t *testing.T) {
	t.Parallel()
	lookup := func(k string) string {
		if k == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "sk-ant-oat01-…"
		}
		return ""
	}
	why := chromeUnavailable(lookup, "claudecode", "")
	if !strings.Contains(why, "CLAUDE_CODE_OAUTH_TOKEN") || !strings.Contains(why, "/login") {
		t.Errorf("why = %q", why)
	}
}

// The credential half must consult the proveo home, not just the environment: an
// ANTHROPIC_API_KEY exported beside a persisted /login does not displace it, and
// Claude Code reads the store either way. Only the credential half is asserted —
// whether a native host is listening is the operator's Chrome, not this test's.
func TestChromeUnavailableReadsTheLoginInTheProveoHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	live := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real","expiresAt":%d}}`,
		time.Now().Add(8*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-api03-…"
		}
		return ""
	}
	if why := chromeUnavailable(lookup, "claudecode", home); strings.Contains(why, "ANTHROPIC_API_KEY") {
		t.Errorf("the login in the home must outrank the key beside it: %q", why)
	}
	// Same key, no login: now the refusal is the honest one.
	if why := chromeUnavailable(lookup, "claudecode", t.TempDir()); !strings.Contains(why, "ANTHROPIC_API_KEY") {
		t.Errorf("why = %q", why)
	}
}

// The sandbox stopped being an add-on decision: a harness that declares sbx runs
// there whenever the host allows it. A remembered answer written before the lock
// — or one with the box cleared — must no longer be able to route the run to the
// weaker docker backend, so every Params below has to agree with the host test.
func TestTheSandboxBackendIgnoresTheRememberedAddon(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Docker: manifest.DockerSbx}
	want := sandbox.Selected(man)
	for _, p := range []*Params{
		{},
		{Addons: []string{addonSandbox}, AddonsAnswered: true},
		{Addons: []string{addonBrowser}, AddonsAnswered: true},
		{AddonsAnswered: true},
	} {
		if got := p.willSandbox(man); got != want {
			t.Errorf("Addons=%v answered=%v: willSandbox = %v, want the host test %v",
				p.Addons, p.AddonsAnswered, got, want)
		}
	}
	if (&Params{}).willSandbox(manifest.Manifest{Docker: manifest.DockerDind}) {
		t.Error("a harness that does not declare sbx never takes the sandbox backend")
	}
}

// The compulsory tick happens INSIDE gateAddons, so anything gating on it must
// read the row's options rather than On — or the first paint, the one an Enter
// can accept outright, is computed from a state that no longer exists.
func TestGateAddonsIsStableOnTheFirstPass(t *testing.T) {
	t.Parallel()
	form := func() *choiceui.Form {
		return &choiceui.Form{Rows: []choiceui.Row{
			// On=false is what a cache answered before the lock looks like.
			{Label: rowExecution, Options: []string{addonSandbox}, Multi: true, On: []bool{false}},
			{Label: rowInterface, Options: []string{addonChrome}, Multi: true, On: []bool{true}},
		}}
	}
	first := form()
	gateAddons(first, "open", "forward", "", "")
	second := form()
	gateAddons(second, "open", "forward", "", "")
	gateAddons(second, "open", "forward", "", "")
	for _, tc := range []struct {
		name string
		f    *choiceui.Form
	}{{"first pass", first}, {"second pass", second}} {
		if sb := tc.f.Rows[0]; !sb.Off[0] || !sb.On[0] {
			t.Errorf("%s: the sandbox must be greyed and ticked, got off=%v on=%v", tc.name, sb.Off, sb.On)
		}
		if c := tc.f.Rows[1]; !c.Off[0] || c.On[0] {
			t.Errorf("%s: a sandboxed run must grey and untick the host browser, got off=%v on=%v",
				tc.name, c.Off, c.On)
		}
	}
}

// A dind harness carries no sandbox box at all, so nothing may read it as one.
func TestGateAddonsLeavesTheHostBrowserAloneOnADindHarness(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: rowExecution, Options: []string{addonDind}, Multi: true, On: []bool{true}},
		{Label: rowInterface, Options: []string{addonChrome}, Multi: true, On: []bool{true}},
	}}
	gateAddons(f, "open", "forward", "", "")
	if c := f.Rows[1]; c.Off[0] || !c.On[0] {
		t.Errorf("no sandbox in the execution row means no exclusion: off=%v on=%v reason=%q", c.Off, c.On, c.Reason)
	}
}

func TestGateAddonsGreysTheSandboxWhenTheHostCannotRunIt(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: rowExecution, Options: []string{addonHost, addonSandbox}, Multi: true, On: []bool{false, true},
	}}}
	gateAddons(f, "open", "forward", "sbx CLI not found on PATH", "")
	r := f.Rows[0]
	if !r.Off[1] {
		t.Fatal("the sandbox add-on must be greyed out when sbx is unavailable")
	}
	if !strings.Contains(r.Reason, "sbx CLI not found on PATH") {
		t.Errorf("reason = %q, want the availability reason", r.Reason)
	}
	if got := selectedAddons(f); len(got) != 0 {
		t.Errorf("a greyed add-on must not count as selected, got %v", got)
	}
	f.Rows[0].Off, f.Rows[0].Reason = nil, ""
	gateAddons(f, "open", "forward", "", "")
	r = f.Rows[0]
	if !r.Off[1] || !r.On[1] {
		t.Errorf("an available sbx is compulsory: greyed and ticked, got off=%v on=%v", r.Off, r.On)
	}
	if r.Reason != "" {
		t.Errorf("a box greyed on every run must stay out of the inline reason, got %q", r.Reason)
	}
	if !strings.Contains(r.OffWhy[addonSandbox], "PROVEO_SBX=0") {
		t.Errorf("the compulsory sandbox must name its escape hatch, got %q", r.OffWhy[addonSandbox])
	}
	if got := selectedAddons(f); !slices.Equal(got, []string{addonSandbox}) {
		t.Errorf("a compulsory sandbox must still be reported as selected, got %v", got)
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
		Label: rowExecution, Multi: true,
		Options: []string{addonHost, addonSandbox},
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
		Label: rowExecution, Multi: true,
		Options: []string{addonSandbox}, On: []bool{true},
	}}}
	gateAddons(f2, "open", "forward", "", "")
	if !f2.Rows[0].Off[0] || !f2.Rows[0].On[0] {
		t.Error("an available sandbox add-on must be greyed and ticked — it is compulsory")
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
		{name: "a remembered answer without the add-on", man: sbxMan,
			p:     Params{Addons: []string{}, AddonsAnswered: true},
			sbxOK: true, want: true,
			because: "the add-on no longer votes: an sbx harness runs on sbx, so the store is still the source"},
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
