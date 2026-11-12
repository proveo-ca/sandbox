// SPEC: _spec/_devops/agent-version-pin.puml, _spec/internal/runlog/run-transcript.puml, _spec/_paradigms/retire-dind.puml, _spec/internal/sbx/credential-resolution.puml, _spec/internal/credentials/credential-decisions.puml, _spec/internal/sbx/clone-workspace.puml, _spec/_paradigms/credential-boundary.puml
package run

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/envflag"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/githubaccess"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/hermesdesktop"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/hostcdp"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/operator"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Deps are the host- and terminal-bound capabilities a run needs.
type Deps struct {
	ManifestFor      func(target string) (manifest.Manifest, error)
	PickProject      func(ask ScopeAsk) (scope string, ok bool)
	PromptEnv        func(target string, missing []manifest.EnvVar) map[string]string
	GitHubTokenEnv   func() (envVar string, items []sandbox.Item) // stages the host's GitHub token; items say what degraded
	ProvisionConfirm func(question string) bool
	PreflightImages  func(man manifest.Manifest, agentImage string) (string, error)
	EnsureDesktop    func() (launch []string, installed bool, err error) // installs Hermes Desktop when missing; installed is this run's install
}

func Do(p Params, d Deps) (err error) {
	var rs Spec
	rs.Sid = fmt.Sprintf("proveo-%d-%d", time.Now().Unix(), os.Getpid())

	log, logErr := runlog.Open(rs.Sid)
	rs.Log = log
	runReported := false
	if logErr != nil {
		ui.Warnf("run log unavailable: %v", logErr)
	} else {
		ui.TeeTo(rs.Log.Writer())
		defer rs.Log.Close()
		ui.Section(ui.SectionRun)
	}
	rs.RunDir = RunDirOf(rs.Sid)
	// Registered after the log's own Close, so LIFO runs it first.
	defer func() { recordOutcome(rs.AgentLaunched, err) }()
	defer func() {
		if rs.AgentLaunched {
			results.SetExit(results.Dir(StateDir()), rs.Sid, exitStatus(err))
		}
	}()
	defer func() {
		if err != nil {
			endEarly(&rs, p.PrintOnly, runReported)
		}
	}()

	rs.Man, err = d.ManifestFor(p.Target)
	if err != nil {
		return err
	}
	if err := requireSandbox(p, d); err != nil {
		return err
	}
	rs.Log.Artifacts(rs.RunDir)

	if !acceptsLocalModel(p.Target) && p.LocalModel != "" {
		return fmt.Errorf("cursor has no --local-model path (inference is vendor-pinned); unset it or use another harness")
	}

	if err := detectWorkspace(&rs, &p); err != nil {
		return err
	}
	if !p.PrintOnly {
		sandbox.ReconcileResults(results.Dir(StateDir()), rs.Workspace.RepoRoot)
	}
	if err := choosePages(&rs, &p, d); err != nil {
		return err
	}
	reportRun(&rs, p.PrintOnly)
	runReported = true
	if err := preflightImage(&rs, &p, d); err != nil {
		return err
	}
	if err := ensureDesktop(&rs, &p, d); err != nil {
		return err
	}
	resolveCredentials(&rs, &p, d)
	if err := planMounts(&rs, &p); err != nil {
		return err
	}
	buildPosture(&rs, &p)
	return launchSandbox(&rs, &p, d)
}

// ensureDesktop makes sure Hermes Desktop is installed when the run drives it, under "run" with the image pull.
func ensureDesktop(rs *Spec, p *Params, d Deps) error {
	if !slices.Contains(p.Addons, addonDesktop) || p.PrintOnly || d.EnsureDesktop == nil {
		return nil
	}
	launch, installed, err := d.EnsureDesktop()
	if err != nil {
		return err
	}
	rs.DesktopLaunch, rs.Desktop = launch, desktopFact(installed)
	return nil
}

var sandboxReady = sandbox.Ready

// reportRun prints the run block: where the transcript is, and the scope the run took.
func reportRun(rs *Spec, printOnly bool) {
	ui.Section(ui.SectionRun)
	ui.Facts(logFact(rs.Log.Path(), printOnly), ui.Fact{Role: ui.RoleStore, Key: "scope", Value: rs.Workspace.ScopeLabel})
}

// endEarly prints what a failed run had not printed yet: the log row under "run", then the
// workspace and interface items it held.
func endEarly(rs *Spec, printOnly, runReported bool) {
	if !runReported {
		ui.Section(ui.SectionRun)
		ui.Facts(logFact(rs.Log.Path(), printOnly))
	}
	if !rs.ItemsHanded {
		reportUnhanded(rs, printOnly)
	}
}

// reportUnhanded prints the workspace and interface items of a run that ended before its blocks did.
func reportUnhanded(rs *Spec, printOnly bool) {
	path := screenPaths(printOnly)
	sandbox.PrintBlock(sandbox.WorkspaceHeading, nil, sandbox.WithPaths(rs.Workspace.Items, path))
	sandbox.PrintBlock(sandbox.InterfaceHeading, append(slices.Clone(rs.InterfaceFacts), rs.Desktop), sandbox.WithPaths(rs.Interface, path))
}

// screenPaths writes the host paths inside a screen line: home-relative on a live run, absolute under --print.
func screenPaths(printOnly bool) func(string) string {
	if printOnly {
		return sandbox.Absolute
	}
	return ui.TildeIn
}

// logFact is the run log's row: home-relative on a live run, absolute under --print.
func logFact(path string, printOnly bool) ui.Fact {
	if path != "" && !printOnly {
		path = ui.Tilde(path)
	}
	return ui.Fact{Role: ui.RoleStore, Key: "log", Value: path}
}

// preflightImage makes the agent image local and reads the agent version it pins.
func preflightImage(rs *Spec, p *Params, d Deps) error {
	if !p.PrintOnly && d.PreflightImages != nil {
		image, err := d.PreflightImages(rs.Man, p.Image)
		if err != nil {
			return err
		}
		p.Image = image
	}
	if v := posture.AgentVersion(p.Image); v != "" {
		rs.Backend.AgentVersion = v
		ui.Logf("agent: %s %s — pinned at build (`mise run build %s` moves it)", p.Target, v, p.Target)
	}
	return nil
}

// requireSandbox refuses a run that sbx cannot host: there is no other backend.
func requireSandbox(p Params, d Deps) error {
	if ok, why := sandboxReady(p.PrintOnly, d.ProvisionConfirm); !ok {
		return sandbox.Unavailable(why)
	}
	return nil
}

// ScopeAsk is what the scope page is asked with.
type ScopeAsk struct {
	Target   string
	RepoRoot string
	Projects []workspace.Project
	Picked   string        // the scope an earlier visit chose; "" is the repo root
	Step     choiceui.Step // the page's place among the run's pages
}

// scopeChoice is what a monorepo run that named no scope does about it.
type scopeChoice int

const (
	scopeSilent scopeChoice = iota // take the repo root and say nothing
	scopeAsk                       // the operator picks
	scopeRoot                      // the question is skipped: take the repo root and say so
)

// scopeGate decides whether a run asks for a scope: never under --print, --yes or the scheduler.
func scopeGate(p *Params, tty, wizard, scheduled bool) scopeChoice {
	switch {
	case p.PrintOnly:
		return scopeSilent
	case p.Yes || scheduled:
		return scopeRoot
	case tty && wizard:
		return scopeAsk
	}
	return scopeSilent
}

// chooseScope resolves the scope of a monorepo run that named none; "" is the repo root.
func chooseScope(gate scopeChoice, ask ScopeAsk, pick func(ScopeAsk) (string, bool)) (string, error) {
	if gate != scopeAsk {
		return "", nil
	}
	scope, ok := pick(ask)
	if !ok {
		return "", errors.New("cancelled at the scope prompt")
	}
	return scope, nil
}

// errStepBack is the form reporting a step back to the page before it.
var errStepBack = errors.New("step back")

// runLeave is what Ctrl+C asks on a run's pages: nothing exists yet to leave behind.
var runLeave = choiceui.Leave{Title: "Leave proveo run?", Body: "No sandbox has been created or resumed."}

// formVisit is one visit to the run's form: its place among the pages, and the
// form an earlier visit stepped back from.
type formVisit struct {
	step    choiceui.Step
	left    *choiceui.Form
	onEnter func() []string // what accepting the form starts, for the scope this visit has
}

// runOnEnter names what accepting the run's form starts, in order.
// SPEC: _spec/internal/run/run-spec.puml
func runOnEnter(p *Params, ws workspace.MountSpec) []string {
	var out []string
	switch on, whyOff, err := decideClone(p, ws); {
	case err != nil:
	case on:
		out = append(out, "clone the repository into a private workspace — the checkout is not written")
	case whyOff != "":
		out = append(out, "mount the checkout — the agent edits it directly ("+whyOff+")")
	default:
		out = append(out, "mount the checkout — the agent edits it directly (--clone=false)")
	}
	return append(out, "create the sandbox, or re-attach to the one this folder left", "start "+p.Target)
}

// choosePages walks the run's pages: the scope, then the form. A step back from
// the form returns to the scope page with the cursor where it was left.
// SPEC: _spec/internal/run/run-spec.puml
func choosePages(rs *Spec, p *Params, d Deps) error {
	gate := scopeGate(p, agentio.IsStdinTTY(), agentio.WizardEnabled(), scheduled())
	var projs []workspace.Project
	if strings.Trim(p.Scope, "/") == "" && gate != scopeSilent && rs.Workspace.Scope.IsRepo {
		projs = workspace.DiscoverProjects(rs.Workspace.RepoRoot)
	}
	return walkPages(rs, p, gate, projs, d.PickProject, func(visit *formVisit) error {
		visit.onEnter = func() []string { return runOnEnter(p, rs.Workspace.WS) }
		return promptChoices(rs, p, d, visit)
	})
}

// walkPages asks for the scope when projs holds any, applies it, and runs form;
// each step back starts over from the parameters the pages were entered with.
func walkPages(rs *Spec, p *Params, gate scopeChoice, projs []workspace.Project,
	pick func(ScopeAsk) (string, bool), form func(*formVisit) error) error {
	scope := strings.Trim(p.Scope, "/")
	base, visit := p.clone(), formVisit{}
	for {
		*p = base.clone()
		if len(projs) > 0 {
			ask := ScopeAsk{Target: p.Target, RepoRoot: rs.Workspace.RepoRoot, Projects: projs, Picked: scope}
			if gate == scopeAsk {
				ask.Step, visit.step = choiceui.Step{At: 1, Of: 2, Leave: runLeave}, choiceui.Step{At: 2, Of: 2, Leave: runLeave}
			}
			var err error
			if scope, err = chooseScope(gate, ask, pick); err != nil {
				return err
			}
		}
		applyScope(rs, p, scope)
		err := form(&visit)
		if errors.Is(err, errStepBack) {
			continue
		}
		switch {
		case err != nil:
		case scope != "":
			rs.Workspace.ScopeLabel = scope
		case len(projs) > 0 && gate == scopeRoot:
			rs.Workspace.ScopeLabel = "repo root"
		}
		return err
	}
}

// detectWorkspace finds, once, what the run stands on: the access settings, the
// repository and the output directory.
func detectWorkspace(rs *Spec, p *Params) error {
	path, err := githubaccess.Path()
	if err != nil {
		return fmt.Errorf("GitHub access settings: %w", err)
	}
	access, err := githubaccess.Load(path)
	if err != nil {
		return fmt.Errorf("GitHub access settings: %w", err)
	}
	p.GitTransport = access.Transport
	rs.Start = OrWD(p.Input)
	rs.Workspace.Scope = workspace.Resolve(rs.Start)
	rs.Workspace.RepoRoot = rs.Start
	if rs.Workspace.Scope.IsRepo {
		rs.Workspace.RepoRoot = rs.Workspace.Scope.Root
	}
	if p.Output == "" {
		p.Output = filepath.Join(rs.Workspace.RepoRoot, "reports")
	}
	if !p.PrintOnly && p.Output != "" {
		if err := os.MkdirAll(p.Output, 0o755); err != nil {
			return fmt.Errorf("output dir %s: %w", p.Output, err)
		}
	}

	return nil
}

// applyScope points the run at scope: its mount, its env file, and what that file sets.
func applyScope(rs *Spec, p *Params, scope string) {
	rs.Workspace.SubScope = scope
	rs.Workspace.WS = workspace.MountSpec{
		Workspace: rs.Man.Workspace, OutputDir: p.Output,
		MountRootDeps: mountRootDeps(os.Getenv),
	}
	{ // one layout: the scope dir drives the /app mount path
		if rs.Workspace.SubScope != "" {
			rs.Workspace.WS.InputDir = filepath.Join(rs.Workspace.RepoRoot, rs.Workspace.SubScope)
		} else {
			rs.Workspace.WS.InputDir = rs.Start
		}
		if rs.Workspace.Scope.IsRepo {
			rs.Workspace.WS.RepoRoot = rs.Workspace.RepoRoot
		}
	}

	rs.InvocationWD, _ = os.Getwd()
	rs.Creds.HostEnvFile = strings.TrimSpace(os.Getenv("PROVEO_ENV_FILE"))
	if rs.Creds.HostEnvFile == "" {
		rs.Creds.HostEnvFile = workspace.ProjectEnvFile(rs.InvocationWD, rs.Workspace.WS.InputDir, rs.Workspace.WS.RepoRoot)
	}
	rs.Creds.Lookup = credentials.ProviderLookup(rs.Creds.HostEnvFile)
	p.HostEnvFile = rs.Creds.HostEnvFile

	rs.Choices.EvidenceSet = false
	if v := strings.ToLower(strings.TrimSpace(rs.Creds.Lookup(EvidenceVar))); v != "" {
		if v != EvidenceDefault && v != EvidenceVerbose {
			ui.Warnf("%s=%q is not %s|%s — using %s", EvidenceVar, v, EvidenceDefault, EvidenceVerbose, EvidenceVerbose)
			v = EvidenceVerbose
		}
		p.Evidence, rs.Choices.EvidenceSet = v, true
	}
	if strings.TrimSpace(os.Getenv("PROVEO_SBX_MCP")) != "" {
		p.MCP, p.MCPSet = mcpOff, true
		if sandbox.MCPGatewayAllowed() {
			p.MCP = mcpOn
		}
	}
	if v := strings.TrimSpace(os.Getenv("PROVEO_MISE_TRUST")); v != "" {
		p.MiseTrust, p.MiseTrustSet = mcpOff, true
		if envflag.On(v) {
			p.MiseTrust = mcpOn
		}
	}
}

func promptChoices(rs *Spec, p *Params, d Deps, visit *formVisit) error {
	var err error
	rs.Choices.SettingsRoot = proveohome.Root(os.Getenv)
	rs.Choices.Settings, err = agentsettings.Load(rs.Choices.SettingsRoot)
	if err != nil {
		settingsWarning("cached settings — unreadable, not used", err, p.PrintOnly)
	}
	rs.Choices.Promptable = cacheApplies(p.PrintOnly, agentio.IsStdinTTY())
	if err := p.applyCapabilities(rs.Man); err != nil {
		return err
	}
	if rs.Choices.Promptable || (p.PrintOnly && scheduled() && agentio.WizardEnabled()) {
		if cached, ok := rs.Choices.Settings.Lookup(p.Target, rs.Man.Capabilities); ok {
			p.seedFromCache(cached, rs.Creds.Lookup, rs.Choices.EvidenceSet)
		}
	}
	rs.Choices.PortsRoot = realPathOf(scanRoot(p.Input, gitRootOrEmpty(rs.Workspace.Scope, rs.Workspace.RepoRoot)))
	p.discovered = devports.Discover(rs.Choices.PortsRoot)
	var gone []sandbox.Item
	if saved, ok := rs.Choices.Settings.PortsFor(rs.Choices.PortsRoot); ok {
		p.Ports, gone = rememberedPorts(saved, p.discovered)
	}
	p.discoveredApps = devports.AndroidApps(rs.Choices.PortsRoot)
	if saved, ok := rs.Choices.Settings.AppsFor(rs.Choices.PortsRoot); ok {
		var apps []sandbox.Item
		p.Apps, apps = rememberedApps(saved, p.discoveredApps)
		gone = append(gone, apps...)
	}
	rs.Interface = gone
	if p.Roles == nil {
		p.Roles = provider.Roles{}
	}
	if err := p.resolveAddonFlags(rs.Man); err != nil {
		return err
	}
	if rs.Choices.Promptable && !p.Yes {
		if err := p.promptChoices(rs.Man, rs.Creds.Lookup, gitRootOrEmpty(rs.Workspace.Scope, rs.Workspace.RepoRoot), rs.Choices.SettingsRoot, visit); err != nil {
			return err
		}
	}
	if err := p.applyCapabilities(rs.Man); err != nil {
		return err
	}
	if rs.Choices.Promptable {
		rs.Choices.Settings.Remember(p.Target, rs.Man.Capabilities, agentsettings.Choice{
			Credentials: p.credentialsOrDefault(), Addons: p.Addons, AuthVar: p.AuthVar,
			Evidence: p.evidenceOrDefault(), LocalModel: rememberedLocalModel(rs.Man, p.LocalModel),
			MCP:       p.MCP,
			MiseTrust: p.MiseTrust,
		})
		if p.portsAsked {
			rs.Choices.Settings.RememberPorts(rs.Choices.PortsRoot, portsToRemember(p.Ports))
		}
		if p.appsAsked {
			rs.Choices.Settings.RememberApps(rs.Choices.PortsRoot, appsToRemember(p.Apps))
		}
		if err := rs.Choices.Settings.Save(rs.Choices.SettingsRoot); err != nil {
			settingsWarning("cached settings — not saved", err, p.PrintOnly)
		}
	}
	if scheduled() {
		if err := p.tuiForSchedule(); err != nil {
			return err
		}
	}
	rs.Backend.BrowserImage = rs.Man.Images[p.Target+"-browser"] // the -browser variant, if this harness has one
	if hasAddon(p.Addons, "browser") && rs.Backend.BrowserImage != "" {
		var isLocal bool
		p.Image, isLocal = posture.ResolveImageChoice(rs.Backend.BrowserImage)
		if isLocal {
			ui.Logf("image: %s (local build — newer than the published tag)", p.Image)
		}
		ui.Logf("variant: browser → %s", posture.Image(p.Image))
	}
	return nil
}

// settingsWarning is a ⚠ item about the cached settings, with err dim under it, its host paths written for the screen.
func settingsWarning(head string, err error, printOnly bool) {
	ui.Warnf("%s", head)
	ui.Detailf("%s", screenPaths(printOnly)(err.Error()))
}

// rememberedLocalModel keeps a local model in the cache only for a def whose row chooses it.
func rememberedLocalModel(man manifest.Manifest, localModel string) string {
	if !hasModelRow(man) {
		return ""
	}
	return localModel
}

// startHostCDP opens or reuses the host browser for the host-CDP add-on; it returns the agent's env,
// the browser's interface fact and the interface items.
func startHostCDP(p *Params) ([]string, ui.Fact, []sandbox.Item, error) {
	if !hasAddon(p.Addons, addonHostCDP) {
		return nil, ui.Fact{}, nil, nil
	}
	port, err := hostcdp.Port(os.Getenv)
	if err != nil {
		return nil, ui.Fact{}, nil, err
	}
	var fact ui.Fact
	if p.PrintOnly {
		profile, _ := hostcdp.ProfileDir(p.Target)
		fact = ui.Fact{Role: ui.RoleHost, Key: "chrome", Value: "not started in print mode",
			Detail: []string{fmt.Sprintf("CDP 127.0.0.1:%d · profile %s", port, profile)}}
	} else {
		var opened hostcdp.Opened
		if err := startAddon(addonHostCDP, func(report func(string, ...any)) (err error) {
			opened, err = hostcdp.Ensure(p.Target, port, report)
			return err
		}); err != nil {
			return nil, ui.Fact{}, nil, fmt.Errorf("%s: %w", addonHostCDP, err)
		}
		fact = chromeFact(opened, port)
	}
	items := []sandbox.Item{{Warn: true, Text: addonHostCDP + " — the agent controls every tab and session in that browser",
		Detail: []string{"log into only what it should touch"}}}
	return []string{fmt.Sprintf("%s=%d", hostcdp.EnvPort, port)}, fact, items, nil
}

// chromeFact is the host browser on CDP port: the one the run opened and its profile, or a reused one.
func chromeFact(o hostcdp.Opened, port int) ui.Fact {
	at := fmt.Sprintf("CDP 127.0.0.1:%d", port)
	if o.Browser == "" {
		return ui.Fact{Role: ui.RoleHost, Key: "chrome", Value: at + " (reused)"}
	}
	return ui.Fact{Role: ui.RoleHost, Key: "chrome", Value: o.Browser + " · " + at, Detail: []string{"profile " + ui.Tilde(o.Profile)}}
}

// startHostADB reuses or boots the host emulator for the android add-on; it returns the agent's env,
// the device's interface fact and the interface items.
func startHostADB(p *Params) ([]string, ui.Fact, []sandbox.Item, error) {
	if !hasAddon(p.Addons, addonAndroid) {
		return nil, ui.Fact{}, nil, nil
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		return nil, ui.Fact{}, nil, err
	}
	var dev hostadb.Started
	if !p.PrintOnly {
		if err := startAddon(addonAndroid, func(report func(string, ...any)) (err error) {
			dev, err = hostadb.Ensure(os.Getenv, port, report)
			return err
		}); err != nil {
			return nil, ui.Fact{}, nil, fmt.Errorf("%s: %w", addonAndroid, err)
		}
	}
	items := []sandbox.Item{{Warn: true, Text: addonAndroid + " — the agent controls every device on that adb server",
		Detail: []string{"the server dials any address it is told to from this host, outside sbx egress"}}}
	return hostadb.GuestEnv(port), androidFact(dev, port), items, nil
}

// androidFact is the device on the host adb server: the AVD the run booted, a device it reused, or none under --print.
func androidFact(dev hostadb.Started, port int) ui.Fact {
	f := ui.Fact{Role: ui.RoleHost, Key: addonAndroid, Value: "not started in print mode",
		Detail: []string{fmt.Sprintf("adb server 127.0.0.1:%d", port)}}
	switch {
	case dev.AVD != "":
		f.Value = dev.Serial + " (AVD " + dev.AVD + ")"
	case dev.Serial != "":
		f.Value = dev.Serial + " (reused)"
	}
	return f
}

// desktopFact is the Hermes Desktop app the run opens on the sandbox, and whether this run installed it.
func desktopFact(installed bool) ui.Fact {
	f := ui.Fact{Role: ui.RoleHost, Key: addonDesktop, Value: "Hermes Desktop"}
	if installed {
		f.Value += " " + hermesdesktop.Tag + " (installed now)"
	}
	return f
}

// startAddon runs an add-on's host start behind a live line on a terminal; each line it reports
// goes to the run log alone.
func startAddon(name string, ensure func(report func(string, ...any)) error) error {
	done := func() {}
	if ui.Fancy() {
		done = ui.Running(func(ran time.Duration) string { return name + " — starting · " + ran.Round(time.Second).String() }, name+" — starting")
	} else {
		ui.Logf("%s — starting", name)
	}
	err := ensure(func(format string, a ...any) { ui.Logf(format, a...) })
	done()
	return err
}

// resolveCredentials finds the login the run uses, prompts for a required variable no
// credential covers, and prints the credentials block.
func resolveCredentials(rs *Spec, p *Params, d Deps) {
	rs.Creds.FileLogin, rs.Creds.LoginNeedsRefresh = credentials.PersistedLogin(p.Target, proveohome.Root(os.Getenv))
	rs.Creds.StoreHeld = sbxStoredAuth(rs.Man)
	rs.Creds.LoggedIn = rs.Creds.FileLogin || len(rs.Creds.StoreHeld) > 0
	var facts []ui.Fact
	var items []sandbox.Item
	if !p.PrintOnly {
		facts, items = loginFacts(rs, p, d)
	}
	rs.Creds.Detected = credentials.FilterProviders(provider.Detect(rs.Creds.Lookup), rs.Man.Capabilities)
	withheld := credentials.WithheldProviders(rs.Man, p.Target, p.AuthVar,
		proveohome.Root(os.Getenv), rs.Creds.Lookup, rs.Creds.Detected)
	if len(withheld) > 0 {
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "withheld", Value: strings.Join(withheld, ", "),
			Detail: []string{fmt.Sprintf("auth %q keeps them out of sbx's secret store and the agent's environment", p.AuthVar)}})
	}
	for _, msg := range slices.Concat(p.Roles.MissingKeys(rs.Creds.Detected), p.Roles.WithheldKeys(withheld, p.AuthVar), p.Roles.BillingClashes(p.AuthVar)) {
		items = append(items, sandbox.Item{Warn: true, Text: msg})
	}
	items = append(items, unknownModels(rs, p)...)
	if !p.PrintOnly && d.GitHubTokenEnv != nil {
		_, gh := d.GitHubTokenEnv()
		items = append(items, gh...)
	}
	sandbox.PrintBlock(credentialsHeading, facts, items)
}

// credentialsHeading declares the credentials heading, for sandbox.PrintBlock.
func credentialsHeading() { ui.Section(ui.SectionCredentials) }

// loginFacts are the credentials block's login rows and warnings.
func loginFacts(rs *Spec, p *Params, d Deps) (facts []ui.Fact, items []sandbox.Item) {
	if credentials.NeedsSandboxLogin(rs.Man, true, rs.Creds.FileLogin, rs.Creds.StoreHeld, rs.Creds.Lookup) {
		svc := sbx.SubscriptionService(sbx.BuiltinAgent(credentials.HarnessFamily(rs.Man.Name)))
		ui.Logf("the sandbox has no credential of its own — sbx keeps a subscription login only in its own store")
		login := sandbox.Item{Warn: true, Text: "login — none found"}
		if how := sbx.SignIn(svc); how != "" {
			login.Detail = []string{"sign in once: " + how}
			ui.Logf("sign in once with %s; sbx then holds it as %s (oauth configured) for every sandbox", how, svc)
		}
		items = append(items, login)
	}
	gapFacts, gapItems := credentialGaps(rs, p, d)
	facts = append(facts, gapFacts...)
	if rs.Creds.LoginNeedsRefresh {
		items = append(items, sandbox.Item{Warn: true, Text: "proveo home login — needs a refresh",
			Detail: []string{`the agent may report "Login expired" on its first turn`}})
		ui.Logf("the login in the proveo home needs a refresh; the agent carries on only if the renewal reaches the provider from where it runs")
	}
	if rs.Creds.FileLogin && strings.TrimSpace(p.AuthVar) == "" {
		if av := credentials.EffectiveAuthVar(rs.Man, p.Target, p.AuthVar, proveohome.Root(os.Getenv), rs.Creds.Lookup); av != "" && strings.TrimSpace(rs.Creds.Lookup(av)) != "" {
			facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "env token", Value: av + " set, not injected"})
			ui.Logf("%s is set but not injected — the login in the proveo home is the credential, and an env token would override it", av)
		}
	}
	return facts, append(items, gapItems...)
}

// planMounts plans the workspace's mounts: the worktree links, the mount plan, the proveo home and the gh config.
func planMounts(rs *Spec, p *Params) error {
	var err error
	rs.Workspace.WorktreeLinks, err = rs.Workspace.WS.PrepareWorktreeLinks(proveohome.Root(os.Getenv))
	if err != nil {
		rs.Workspace.Items = append(rs.Workspace.Items, sandbox.Item{Warn: true, Text: "git worktree — falling back to GIT_DIR pinning",
			Detail: []string{err.Error()}})
	}
	rs.Workspace.WS.WorktreeLinkDir = rs.Workspace.WorktreeLinks

	rs.Workspace.Mounts, rs.Workspace.Links = rs.Workspace.WS.Plan()

	rs.Creds.HomePlan, err = proveohome.Prepare(rs.Man.Home, os.Getenv)
	if err != nil {
		return err
	}
	if rs.Creds.HomePlan.Root != "" {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, rs.Creds.HomePlan.Mounts...)
		if !p.PrintOnly {
			ui.Logf("%s", homeLine(rs.Creds.HomePlan.Root, rs.Man.Home))
		}
	}

	if m, ok := credentials.GhConfigMount(os.Getenv); ok {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, m)
		ui.Logf("gh config: %s not mounted — its bind targets %s, and sandbox.WorkspaceBinds drops every bind under %s/",
			m.Host, m.Container, proveohome.ContainerHome)
	}
	return nil
}

// homeLine names the proveo home and the directories under it the sandbox sees, at their host paths.
func homeLine(root string, h manifest.Home) string {
	return fmt.Sprintf("proveo home: %s (%s)", root, homeSeen(h))
}

// homeFact is homeLine as a workspace fact.
func homeFact(root string, h manifest.Home) ui.Fact {
	return ui.Fact{Role: ui.RoleStore, Key: "proveo home", Value: root, Detail: []string{homeSeen(h)}}
}

// homeSeen names the directories under the proveo home the sandbox sees, at their host paths.
func homeSeen(h manifest.Home) string {
	var seen []string
	for _, m := range h.Mounts {
		if rel := strings.Trim(strings.TrimSpace(m.Host), "/"); rel != "" {
			seen = append(seen, rel)
		}
	}
	seen = append(seen, "toolchains")
	where := "at their host paths"
	if len(seen) == 1 {
		where = "at its host path"
	}
	return "the sandbox sees " + strings.Join(seen, ", ") + " " + where
}

// askOnTerminal reports whether the run may prompt the operator on the terminal; tests replace it.
var askOnTerminal = func() bool { return agentio.IsStdinTTY() && agentio.WizardEnabled() }

// credentialGaps names what satisfies a subscription harness's missing variables, or prompts
// for or warns about each required variable no credential covers.
func credentialGaps(rs *Spec, p *Params, d Deps) (facts []ui.Fact, items []sandbox.Item) {
	missing := rs.Man.MissingEnv(rs.Creds.Lookup)
	if len(missing) == 0 || p.PrintOnly {
		return nil, nil
	}
	switch {
	case rs.Man.Subscription && rs.Creds.FileLogin:
		facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: "login", Value: "persisted in the proveo home"})
		missing = nil
	case rs.Man.Subscription && len(rs.Creds.StoreHeld) > 0:
		missing = nil
	case rs.Man.Subscription:
		rs.Creds.AuthMissingAtStart = append([]manifest.EnvVar(nil), missing...)
		items = append(items, sandbox.Item{Warn: true, Text: rs.Man.Name + " — starting without a credential",
			Detail: []string{"run `proveo init` to persist credentials between runs"}})
	case askOnTerminal():
		credentialsHeading()
		for name, v := range d.PromptEnv(p.Target, missing) {
			_ = os.Setenv(name, v)
		}
		missing = rs.Man.MissingEnv(rs.Creds.Lookup)
	}
	for _, e := range missing {
		msg := e.Name + " not set"
		if e.Description != "" {
			msg += " — " + e.Description
		}
		items = append(items, sandbox.Item{Warn: true, Text: msg})
	}
	return facts, items
}

// credentialsPosture reports where the run puts the provider key.
func credentialsPosture(rs *Spec, p *Params) string {
	if p.forwards() {
		return "forward (the key rides the sandbox env)"
	}
	return "broker (sbx proxy)"
}

func buildPosture(rs *Spec, p *Params) {
	image := posture.Image(p.Image)
	if !p.PrintOnly {
		src := sbx.ResolveImageSource(p.Image, imagebuild.New("").HostBuilder)
		rs.Backend.ImageSource, image = &src, p.Image+" ("+src.Label()+")"
	}
	rs.Posture = posture.Posture{
		Target:        p.Target,
		Credentials:   credentialsPosture(rs, p),
		AddOns:        strings.Join(p.Addons, ","),
		AgentEvidence: p.evidenceOrDefault(),
		DetectedKeys:  strings.Join(rs.Creds.Detected, ","),
		AuthVar:       p.AuthVar,
		LocalModel:    p.LocalModel,
		Observability: posture.Observability(),
		EnforcedBy:    posture.EnforcedBy(),
		Image:         image,
		ModelRoles:    posture.RolesLine(p.Roles),
		RoleProviders: strings.Join(p.Roles.Providers(), ","),
		MCPGateway:    posture.MCPGateway(p.mcpAllowed() || rs.Man.Capabilities.AlwaysMCPGateway(), sandbox.MCPGatewayVar),
		Workspace:     posture.Workspace(predictClone(p, rs.Workspace.WS)),
	}
	rs.Log.Fields("resolved posture", rs.Posture.Fields())
}

func decideClone(p *Params, ws workspace.MountSpec) (on bool, whyOff string, err error) {
	if !p.Clone {
		return false, "", nil // --clone=false or PROVEO_CLONE=off: the mounted checkout, by choice
	}
	switch {
	case ws.RepoRoot == "":
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone needs a git repository and %s is not inside one:\n"+
				"  sbx builds the sandbox workspace by cloning the host repo over a git daemon.\n"+
				"  Run it from a checkout, or drop --clone to work on the mounted directory",
				ws.InputDir)
		}
		return false, "not a git repository — sbx clones with git", nil
	case workspace.LinkedWorktree(ws.InputDir):
		if _, err := workspace.CloneSource(ws.InputDir); err != nil {
			if p.CloneSet {
				return false, "", fmt.Errorf("--clone cannot clone the linked git worktree %s: %v\n"+
					"  sbx clones the main worktree and checks out the worktree's branch. Commit onto a branch, or drop --clone",
					ws.InputDir, err)
			}
			return false, "linked git worktree — " + err.Error(), nil
		}
		return true, "", nil
	case ws.ScopeRel() != "":
		if p.CloneSet {
			return true, "", nil
		}
		return false, "monorepo sub-scope — the primary workspace has to be the repository root", nil
	}
	return true, "", nil
}

// stageCloneEnv stages the project .env a clone lacks; broker runs drop the provider keys sbx's proxy holds.
func stageCloneEnv(rs *Spec, p *Params) string {
	src := workspace.ProjectEnvFile(rs.Workspace.WS.InputDir, rs.Workspace.WS.RepoRoot)
	if src == "" {
		return ""
	}
	dir := filepath.Join(rs.RunDir, "sbx", "project-env")
	var strip []string
	if !p.forwards() {
		strip = provider.KeyVars()
	}
	if p.PrintOnly {
		return filepath.Join(dir, ".env")
	}
	path, dropped, err := credentials.StageProjectEnv(src, dir, strip)
	if err == nil {
		err = copyFile0600(path, filepath.Join(rs.RunDir, "sbx", "project-env.base"))
	}
	if err != nil {
		rs.Workspace.Items = append(rs.Workspace.Items, sandbox.Item{Warn: true, Text: ".env — not staged, the agent runs without it",
			Detail: []string{ui.Tilde(src), err.Error()}})
		return ""
	}
	rs.Backend.CloneEnvBase = filepath.Join(rs.RunDir, "sbx", "project-env.base")
	rs.Backend.CloneEnvHost, rs.Backend.CloneStrip, rs.Backend.CloneDropped = src, strip, dropped
	return path
}

func copyFile0600(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func cloneOffHint(whyOff string) string {
	if whyOff == "" {
		return "Clone mode is off for this run (--clone=false or PROVEO_CLONE=off)."
	}
	return "Clone mode cannot apply here: " + whyOff + "."
}

func predictClone(p *Params, ws workspace.MountSpec) bool {
	on, _, err := decideClone(p, ws)
	return err == nil && on
}

// unknownModels is one ⚠ item per model variable the agent reads that names no model this build knows.
func unknownModels(rs *Spec, p *Params) []sandbox.Item {
	var out []sandbox.Item
	for _, k := range credentials.ConfigVarsFor(rs.Man) {
		if v := strings.TrimSpace(rs.Creds.Lookup(k)); v != "" {
			out = append(out, unknownModel(k, v, p.LocalModel)...)
		}
	}
	return out
}

// launchSandbox resolves the clone and add-ons, then runs the agent in sbx (or prints the plan).
func launchSandbox(rs *Spec, p *Params, d Deps) error {
	desktop := slices.Contains(p.Addons, addonDesktop)
	applyAutoLocalModel(rs, p)
	rs.Model.HostLLM = p.LocalModel != ""

	ui.Section(ui.SectionEgress)
	egress, egressItems := sandbox.Baseline(sandbox.ReachHosts(rs.Man, rs.Creds.Detected))
	if env := credentials.MountedSecretEnv(rs.Workspace.WS.InputDir, rs.Creds.Lookup); env != "" {
		path := ui.Tilde
		if p.PrintOnly {
			path = sandbox.Absolute
		}
		rs.Workspace.Items = append(rs.Workspace.Items, sandbox.Item{Warn: true, Text: path(env) + " — holds a provider key the agent can read"})
	}

	ui.Logf("backend: docker sandboxes (sbx)")
	var err error
	rs.Backend.Clone, rs.Backend.CloneOff, err = decideClone(p, rs.Workspace.WS)
	if err != nil {
		return err
	}
	if rs.Backend.Clone && workspace.LinkedWorktree(rs.Workspace.WS.InputDir) {
		rs.Backend.CloneSource, _ = workspace.CloneSource(rs.Workspace.WS.InputDir)
	}
	if rs.Backend.Clone {
		rs.Backend.CloneEnv = stageCloneEnv(rs, p)
	}
	rs.Posture.Workspace = posture.Workspace(rs.Backend.Clone)
	if dropped := len(rs.Workspace.WS.DepCopies()); dropped > 0 && !rs.Backend.Clone {
		ok, why := workspace.SharedTreesAllowed(rs.Creds.Lookup, workspace.HostPlatform(), workspace.ImagePlatform(os.Getenv))
		if !ok {
			return fmt.Errorf("sbx would mount your checkout with its %d dependency tree(s) (node_modules, target, .venv …): %s.\n"+
				"  %s\n"+
				"  Run from a git checkout where --clone applies, so the seed installs into a private clone, or set PROVEO_DEPS=shared to accept the rewrite",
				dropped, why, cloneOffHint(rs.Backend.CloneOff))
		}
		rs.Workspace.Items = append(rs.Workspace.Items, sandbox.Item{Warn: true,
			Text:   fmt.Sprintf("dependency trees — %d cross into the sandbox as the host built them", dropped),
			Detail: []string{why}})
	}
	browserOn := hasAddon(p.Addons, addonBrowser) && rs.Backend.BrowserImage != ""
	cdpPort := 0
	if browserOn && !p.PrintOnly {
		cdpPort = sandbox.FreeLoopbackPort()
	}
	servedModel := p.LocalModel
	if rs.Model.HostLLM {
		lm := rs.Man.Capabilities.LocalModel
		servedModel = sbx.ContextTag(p.LocalModel, lm.Context)
		if !p.PrintOnly {
			notes := localModelNotes(lm, p.Target, servedModel)
			pulled, err := sbx.EnsureHostOllama(p.LocalModel, lm.Context, os.Getenv, ui.Default)
			if err := admitOversizedModel(err, notes); err != nil {
				return fmt.Errorf("local model %s: %w", p.LocalModel, err)
			}
			if _, err := sbx.EnsureContextTag(p.LocalModel, lm.Context); err != nil {
				return fmt.Errorf("local model %s: serve the %d-token context %s needs: %w", p.LocalModel, lm.Context, p.Target, err)
			}
			model := ui.Fact{Role: ui.RoleHost, Key: "local model", Value: servedModel + " on the host's Ollama (host GPU)"}
			if pulled > 0 {
				model.Detail = []string{"pulled this run · " + sbx.GiB(pulled)}
			}
			egress = append(egress, model)
		}
	}
	sandbox.PrintBlock(func() { ui.Section(ui.SectionEgress) }, egress, egressItems)
	cdpEnv, cdpFact, cdpItems, err := startHostCDP(p)
	if err != nil {
		return err
	}
	rs.InterfaceFacts = append(rs.InterfaceFacts, cdpFact)
	rs.Interface = append(rs.Interface, cdpItems...)
	adbEnv, adbFact, adbItems, err := startHostADB(p)
	if err != nil {
		return err
	}
	rs.InterfaceFacts = append(rs.InterfaceFacts, adbFact, rs.Desktop)
	rs.Desktop = ui.Fact{}
	rs.Interface = append(rs.Interface, adbItems...)
	agentEnv := append(append([]string{}, cdpEnv...), adbEnv...)
	agentEnv = append(agentEnv, operator.Env(proveohome.Root(os.Getenv))...)
	if rs.Model.HostLLM {
		agentEnv = append(agentEnv, sbx.LocalModelEnv(servedModel)...)
		agentEnv = append(agentEnv, "SBX_CRED_ANTHROPIC_MODE=none", "SBX_CRED_OPENAI_MODE=none")
	}
	homeAccess, err := sandbox.PrepareHomeAccess(
		rs.Creds.HomePlan.Root, rs.RunDir, rs.Man.Home)
	if err != nil {
		return err
	}
	ports, unpublished := planPorts(p.Ports, rs.Choices.PortsRoot, p.PrintOnly, cdpPort)
	rs.Interface = append(rs.Interface, unpublished...)
	in := sandbox.Input{
		AgentEnv: agentEnv,
		Target:   p.Target, Image: p.Image, AuthVar: p.AuthVar,
		AgentVersion: rs.Backend.AgentVersion,
		Clone:        rs.Backend.Clone, CloneOff: rs.Backend.CloneOff, Extra: p.Extra,
		RepoRoot: rs.Workspace.WS.RepoRoot, OutputDir: p.Output,
		Browser: browserOn, CDPHostPort: cdpPort,
		Ports: ports, InterfaceItems: rs.Interface, InterfaceFacts: rs.InterfaceFacts,
		AndroidApps: androidApps(p), AndroidEnv: adbEnv,
		Roles:        p.Roles,
		Evidence:     p.evidenceOrDefault(),
		Forwards:     p.forwards(),
		MCPGateway:   p.mcpAllowed() || rs.Man.Capabilities.AlwaysMCPGateway(),
		MiseTrust:    p.miseTrustAllowed(),
		GitTransport: p.GitTransport,
		Desktop:      desktop, DesktopLaunch: rs.DesktopLaunch,
		Man: rs.Man, Sid: rs.Sid, RunDir: rs.RunDir, ResultsDir: results.Dir(StateDir()),
		ImageEntrypoint: sbx.ImageEntrypoint,
		ImageSource: func(ref string) sbx.ImageSource {
			if src := rs.Backend.ImageSource; src != nil && src.Ref == ref {
				return *src
			}
			return sbx.ResolveImageSource(ref, imagebuild.New("").HostBuilder)
		},
		StoredAPIKeys:    sbx.APIKeyServices,
		Mounts:           rs.Workspace.Mounts,
		Lookup:           rs.Creds.Lookup,
		Detected:         rs.Creds.Detected,
		GitEnv:           gitidentity.Resolve(os.Getenv, nil).EnvPairs(),
		HomeEnv:          rs.Creds.HomePlan.Env,
		ScopeRel:         rs.Workspace.WS.ScopeRel(),
		WorktreeFallback: rs.Workspace.WS.WorktreeLinkDir == "",
		WorktreeEnv:      rs.Workspace.WS.WorktreeEnv(),
		CloneSource:      rs.Backend.CloneSource,
		CloneEnv:         rs.Backend.CloneEnv,
		CloneEnvBase:     rs.Backend.CloneEnvBase,
		CloneEnvHost:     rs.Backend.CloneEnvHost,
		ProjectEnv:       workspace.ProjectEnvFile(rs.Workspace.WS.InputDir, rs.Workspace.WS.RepoRoot),
		CloneEnvStrip:    rs.Backend.CloneStrip,
		CloneEnvDropped:  rs.Backend.CloneDropped,
		WorkspaceItems:   rs.Workspace.Items,
		Links:            rs.Workspace.Links,
		DataDir:          p.DataDir,
		Memory:           sbx.MemoryLimit(),
		CPUs:             sbx.CPULimit(),
		HomeRoot:         rs.Creds.HomePlan.Root,
		HomeAccess:       homeAccess,
	}
	if p.PrintOnly {
		cfg, w, secrets := sandbox.Spec(in)
		rs.ItemsHanded = true
		var home []ui.Fact
		if rs.Creds.HomePlan.Root != "" {
			home = append(home, homeFact(rs.Creds.HomePlan.Root, rs.Man.Home))
		}
		sandbox.ReportWorkspace(in, w.Report, sandbox.Absolute, home...)
		ui.Section(ui.SectionSbx)
		sandbox.PrintNotes(w.Report, sandbox.Absolute)
		if _, err := sbx.WriteWorkloadV3(cfg.KitDir, w.Descriptor, w.Recipe, w.Files); err != nil {
			sandbox.PrintInterfacePlan(in)
			return fmt.Errorf("write sandbox kit: %w", err)
		}
		kit := []ui.Fact{{Role: ui.RoleStore, Key: "sandbox kit", Value: cfg.KitDir, Detail: []string{"`proveo clean` removes it"}}}
		if homeAccess.FilesRoot != "" {
			kit = append(kit, ui.Fact{Role: ui.RoleStore, Key: "home files", Value: homeAccess.FilesRoot,
				Detail: []string{"copies the printed create line mounts", "`proveo clean` removes them"}})
		}
		ui.Facts(kit...)
		if len(secrets) > 0 {
			names := make([]string, 0, len(secrets))
			for _, kv := range secrets {
				names = append(names, kv[0])
			}
			ui.Warnf("printed command needs these in sbx's secret store first: %s (`sbx secret set NAME`)", strings.Join(names, ", "))
		}
		sandbox.PrintInterfacePlan(in)
		if desktop {
			fmt.Printf("# sandbox\n%s\n# desktop\nsbx %s && Hermes Desktop → SSH gateway %s\n", sbx.CreateLine(cfg),
				sbx.ShellJoin(sbx.SetupSSHArgs()), sandbox.DesktopGateway(cfg.Name))
			return nil
		}
		var imageCmd []string
		if len(w.Recipe.Entrypoint) == 0 {
			imageCmd = sbx.ImageCmd(in.Image)
		}
		fmt.Printf("# sandbox\n%s\n# agent\nsbx %s\n# in-sandbox\n%s\n", sbx.CreateLine(cfg),
			sbx.ShellJoin(sbx.RunArgs(sbx.Reattach(cfg))),
			cmp.Or(sbx.ShellJoin(sbx.Launched(w.Entrypoint, imageCmd, cfg.Command)), "(no readable ENTRYPOINT on "+in.Image+")"))
		return nil
	}
	rs.AgentLaunched, rs.ItemsHanded = true, true
	return sandbox.Run(in)
}

func realPathOf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// rememberedPorts maps a saved answer onto this run's discovery; gone is one ⚠ item per port whose command is gone.
func rememberedPorts(saved []agentsettings.Port, found []devports.Candidate) (kept []devports.Candidate, gone []sandbox.Item) {
	for _, sp := range saved {
		i := slices.IndexFunc(found, func(c devports.Candidate) bool { return c.Port == sp.Port })
		if i < 0 {
			gone = append(gone, sandbox.Item{Warn: true, Text: fmt.Sprintf("port %d — gone from this workspace, not published", sp.Port),
				Detail: []string{fmt.Sprintf("remembered: `%s` in %s", sp.Command, sp.Source)}})
			continue
		}
		kept = append(kept, found[i])
	}
	return kept, gone
}

// rememberedApps maps a saved answer onto this run's discovery; a module that is gone is dropped and reported.
// androidApps is what the run installs: the chosen modules, only with the android add-on on.
func androidApps(p *Params) []devports.AndroidApp {
	if !hasAddon(p.Addons, addonAndroid) {
		return nil
	}
	return p.Apps
}

func rememberedApps(saved []agentsettings.App, found []devports.AndroidApp) (kept []devports.AndroidApp, gone []sandbox.Item) {
	for _, sa := range saved {
		i := slices.IndexFunc(found, func(a devports.AndroidApp) bool { return a.Module == sa.Module })
		if i < 0 {
			gone = append(gone, sandbox.Item{Warn: true, Text: "android app " + sa.AppID + " — gone from this workspace, not installed",
				Detail: []string{"remembered: module " + sa.Module}})
			continue
		}
		kept = append(kept, found[i])
	}
	return kept, gone
}

func appsToRemember(chosen []devports.AndroidApp) []agentsettings.App {
	out := make([]agentsettings.App, 0, len(chosen))
	for _, a := range chosen {
		out = append(out, agentsettings.App{Module: a.Module, AppID: a.AppID})
	}
	return out
}

func portsToRemember(chosen []devports.Candidate) []agentsettings.Port {
	out := make([]agentsettings.Port, 0, len(chosen))
	for _, c := range chosen {
		out = append(out, agentsettings.Port{Port: c.Port, Command: c.Command, Source: c.Source})
	}
	return out
}

// planPorts keeps every guest port off relay, the browser relay's host port; unpublished names each port it drops.
func planPorts(chosen []devports.Candidate, root string, printOnly bool, relay int) (kept []sandbox.PublishedPort, unpublished []sandbox.Item) {
	free := sandbox.LoopbackFree
	if printOnly {
		free = func(int) bool { return true }
	}
	out := make([]sandbox.PublishedPort, 0, len(chosen))
	for _, c := range chosen {
		dir, cmd := devports.Launch(c, root)
		out = append(out, sandbox.PublishedPort{Guest: c.Port, What: c.Tool + " · " + c.Source, Dir: dir, Cmd: cmd})
	}
	kept, dropped := sandbox.PlanPorts(out, free, relay)
	for _, d := range dropped {
		unpublished = append(unpublished, sandbox.Item{Warn: true, Text: fmt.Sprintf("port %d — not published", d.Guest),
			Detail: []string{devports.HostPortWhy(d.Guest)}})
	}
	return kept, unpublished
}

// recordOutcome writes the run's verdict into the transcript before Do returns
// and the log closes.
// exitStatus is the process status err leads to: the agent's own, 128+signal for a stop, else 1.
func exitStatus(err error) int {
	var ae backend.ExitError
	var st backend.Stopped
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ae):
		return ae.Code
	case errors.As(err, &st):
		return st.Code()
	}
	return 1
}

func recordOutcome(launched bool, err error) {
	var ae backend.ExitError
	switch {
	case err == nil && !launched:
		ui.Logf("outcome: no agent was launched")
	case err == nil:
		ui.Logf("outcome: the agent exited 0")
	case errors.As(err, &ae):
		ui.Logf("outcome: the agent exited with code %d", ae.Code)
	default:
		ui.Logf("outcome: %v", err)
	}
}
