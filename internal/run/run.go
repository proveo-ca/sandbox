// SPEC: _spec/_devops/agent-version-pin.puml, _spec/internal/runlog/run-transcript.puml, _spec/_plans/retire-model-bridging.puml, _spec/_paradigms/retire-dind.puml, _spec/defs/claudecode/chrome-bridge.puml, _spec/internal/sbx/oauth-provisioning.puml, _spec/internal/credentials/credential-decisions.puml, _spec/internal/sbx/clone-workspace.puml, _spec/_paradigms/credential-boundary.puml
package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/devports"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/hostcdp"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/operator"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/secretref"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Deps are the host- and terminal-bound capabilities a run needs.
type Deps struct {
	ManifestFor      func(target string) (manifest.Manifest, error)
	PickProject      func(projs []workspace.Project) string
	PromptEnv        func(target string, missing []manifest.EnvVar) map[string]string
	GitHubTokenEnv   func() string
	ProvisionConfirm func(question string) bool
	PreflightImages  func(man manifest.Manifest, agentImage string) (string, error)
}

func Do(p Params, d Deps) (err error) {
	var rs Spec
	rs.Sid = fmt.Sprintf("proveo-%d-%d", time.Now().Unix(), os.Getpid())
	rs.EgDir = filepath.Join(StateDir(), "egress", rs.Sid)

	log, logErr := runlog.Open(rs.Sid)
	rs.Log = log
	if logErr != nil {
		ui.Warnf("run log unavailable: %v", logErr)
	} else {
		ui.TeeTo(rs.Log.Writer())
		defer rs.Log.Close()
		ui.Section(ui.SectionRun)
		ui.Storef("run log: %s", rs.Log.Path())
	}
	// Registered after the log's own Close, so LIFO runs it first.
	defer func() { recordOutcome(rs.AgentLaunched, err) }()

	rs.Man, err = d.ManifestFor(p.Target)
	if err != nil {
		return err
	}
	if err := requireSandbox(rs.Man, p, d); err != nil {
		return err
	}
	rs.Log.Artifacts(rs.EgDir)

	if !acceptsLocalModel(p.Target) && p.LocalModel != "" {
		return fmt.Errorf("cursor has no --local-model path (inference is vendor-pinned); unset it or use another harness")
	}

	if err := resolveWorkspace(&rs, &p, d); err != nil {
		return err
	}
	if err := promptChoices(&rs, &p, d); err != nil {
		return err
	}
	if err := resolveCredentials(&rs, &p, d); err != nil {
		return err
	}
	buildPosture(&rs, &p)
	assembleEnv(&rs, &p, d)
	return launchSandbox(&rs, &p, d)
}

var sandboxReady = sandbox.Ready

// requireSandbox refuses a run that sbx cannot host: there is no other backend.
func requireSandbox(man manifest.Manifest, p Params, d Deps) error {
	if !man.IsSbx() {
		return fmt.Errorf("%s does not declare `docker: sbx` in its harness.manifest, and sbx is the only backend", p.Target)
	}
	if ok, why := sandboxReady(p.PrintOnly, d.ProvisionConfirm); !ok {
		return sandbox.Unavailable(why)
	}
	return nil
}

func resolveWorkspace(rs *Spec, p *Params, d Deps) error {
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

	rs.Workspace.SubScope = strings.Trim(p.Scope, "/")
	if rs.Workspace.SubScope == "" && !p.PrintOnly && agentio.IsStdinTTY() && WizardEnabled() && rs.Workspace.Scope.IsRepo {
		if projs := workspace.DiscoverProjects(rs.Workspace.RepoRoot); len(projs) > 0 {
			rs.Workspace.SubScope = d.PickProject(projs)
		}
	}
	if rs.Workspace.SubScope != "" {
		ui.Section(ui.SectionScope)
		ui.Storef("scope: %s", rs.Workspace.SubScope)
	}

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
	rs.Creds.HostEnvFile = strings.TrimSpace(os.Getenv("PROVEO_EGRESS_ENV_FILE"))
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

	return nil
}

func promptChoices(rs *Spec, p *Params, d Deps) error {
	var err error
	rs.Choices.SettingsRoot = proveohome.Root(os.Getenv)
	rs.Choices.Settings, err = agentsettings.Load(rs.Choices.SettingsRoot)
	if err != nil {
		ui.Warnf("%v — continuing without cached settings", err)
	}
	rs.Choices.Promptable = cacheApplies(p.PrintOnly, agentio.IsStdinTTY())
	if err := p.applyCapabilities(rs.Man); err != nil {
		return err
	}
	if rs.Choices.Promptable {
		if cached, ok := rs.Choices.Settings.Lookup(p.Target, rs.Man.Capabilities); ok {
			p.seedFromCache(cached, rs.Creds.Lookup, rs.Choices.EvidenceSet)
		}
	}
	rs.Choices.PortsRoot = realPathOf(scanRoot(p.Input, gitRootOrEmpty(rs.Workspace.Scope, rs.Workspace.RepoRoot)))
	p.discovered = devports.Discover(rs.Choices.PortsRoot)
	if saved, ok := rs.Choices.Settings.PortsFor(rs.Choices.PortsRoot); ok {
		p.Ports = rememberedPorts(saved, p.discovered)
	}
	p.discoveredApps = devports.AndroidApps(rs.Choices.PortsRoot)
	if saved, ok := rs.Choices.Settings.AppsFor(rs.Choices.PortsRoot); ok {
		p.Apps = rememberedApps(saved, p.discoveredApps)
	}
	// Roles are no longer read from the environment: proveo does not choose an
	// agent's model. What remains is whatever a previous session remembered,
	// seeded above, and it is kept only as the vocabulary the credential and
	// billing warnings are written in.
	if p.Roles == nil {
		p.Roles = provider.Roles{}
	}
	if err := p.resolveAddonFlags(rs.Man); err != nil {
		return err
	}
	if rs.Choices.Promptable && !p.Yes {
		if err := p.promptChoices(rs.Man, rs.Creds.Lookup, gitRootOrEmpty(rs.Workspace.Scope, rs.Workspace.RepoRoot), rs.Choices.SettingsRoot); err != nil {
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
		})
		if p.portsAsked {
			rs.Choices.Settings.RememberPorts(rs.Choices.PortsRoot, portsToRemember(p.Ports))
		}
		if p.appsAsked {
			rs.Choices.Settings.RememberApps(rs.Choices.PortsRoot, appsToRemember(p.Apps))
		}
		if err := rs.Choices.Settings.Save(rs.Choices.SettingsRoot); err != nil {
			ui.Warnf("%v", err)
		}
	}
	rs.Backend.BrowserImage = rs.Man.Images[p.Target+"-browser"] // the -browser variant, if this harness has one
	if hasAddon(p.Addons, "browser") && rs.Backend.BrowserImage != "" {
		chosen, isLocal := posture.ResolveImageChoice(rs.Backend.BrowserImage)
		if isLocal {
			ui.Section(ui.SectionRun)
			ui.Appf("image: %s (local build — newer than the published tag)", chosen)
		}
		p.Image = chosen
		ui.Appf("variant: browser → %s", p.Image)
	}
	if v := posture.AgentVersion(p.Image); v != "" {
		ui.Section(ui.SectionRun)
		ui.Appf("agent: %s %s — pinned at build (`proveo build %s` moves it)", p.Target, v, p.Target)
	}
	return nil
}

// rememberedLocalModel keeps a local model in the cache only for a def whose row chooses it.
func rememberedLocalModel(man manifest.Manifest, localModel string) string {
	if !hasModelRow(man) {
		return ""
	}
	return localModel
}

// startHostCDP opens or reuses the host browser for the host-CDP add-on and returns the agent's env.
func startHostCDP(p *Params) ([]string, error) {
	if !hasAddon(p.Addons, addonHostCDP) {
		return nil, nil
	}
	ui.Section(ui.SectionInterface)
	port, err := hostcdp.Port(os.Getenv)
	if err != nil {
		return nil, err
	}
	profile, _ := hostcdp.ProfileDir(p.Target)
	if p.PrintOnly {
		ui.Hostf("%s: the run opens a Chrome on %s with CDP on 127.0.0.1:%d (not started in print mode)", addonHostCDP, profile, port)
	} else if err := hostcdp.Ensure(p.Target, port, ui.Hostf); err != nil {
		return nil, fmt.Errorf("%s: %w", addonHostCDP, err)
	}
	ui.Warnf("%s: the agent controls every tab and session in that browser — log into only what it should touch", addonHostCDP)
	return []string{fmt.Sprintf("%s=%d", hostcdp.EnvPort, port)}, nil
}

// startHostADB reuses or boots the host emulator for the android add-on and returns the agent's env.
func startHostADB(p *Params) ([]string, error) {
	if !hasAddon(p.Addons, addonAndroid) {
		return nil, nil
	}
	ui.Section(ui.SectionInterface)
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		return nil, err
	}
	if p.PrintOnly {
		ui.Hostf("%s: the run reuses or boots an emulator on the host adb server 127.0.0.1:%d (not started in print mode)", addonAndroid, port)
	} else if err := hostadb.Ensure(os.Getenv, port, ui.Hostf); err != nil {
		return nil, fmt.Errorf("%s: %w", addonAndroid, err)
	}
	ui.Warnf("%s: the agent controls every device on that adb server, and the server dials any address it is told to from this host — outside sbx egress", addonAndroid)
	return hostadb.GuestEnv(port), nil
}

func startChromeBridge(rs *Spec, p *Params, tierBlocked string) (*chromebridge.Relay, []string) {
	ui.Section(ui.SectionInterface)
	if !hasAddon(p.Addons, addonChrome) {
		return nil, nil
	}
	switch {
	case tierBlocked != "":
		ui.Warnf("%s: skipped — %s", addonChrome, tierBlocked)
		return nil, nil
	case p.PrintOnly:
		ui.Hostf("%s: the run starts a host relay and sets %s + %s on the agent (not started in print mode)",
			addonChrome, chromebridge.EnvAddr, chromebridge.EnvToken)
		return nil, nil
	}
	if why := chromeUnavailable(rs.Man, rs.Creds.Lookup, p.AuthVar, p.Target, rs.Creds.HomePlan.Root); why != "" {
		ui.Warnf("%s: skipped — %s", addonChrome, why)
		return nil, nil
	}
	r, err := chromebridge.Start(chromebridge.BindAddr(), chromebridge.HostSocketDir(), ui.Warnf)
	if err != nil {
		ui.Warnf("%s: skipped — %v", addonChrome, err)
		return nil, nil
	}
	if err := r.SetTokenEnv(); err != nil {
		ui.Warnf("%s: cannot export %s: %v", addonChrome, chromebridge.EnvToken, err)
	}
	ui.Hostf("chrome: Claude in Chrome through YOUR browser — host relay %s → %s. "+
		"The agent gets your logged-in sessions; site permissions stay the extension's",
		r.Addr(), chromebridge.HostSocketDir())
	return r, r.Env()
}

func hostStoreResolver() *secretref.Resolver {
	return &secretref.Resolver{
		Getenv: os.Getenv,
		Announce: func(string) {
			ui.Section(ui.SectionCredentials)
			ui.Hostf("reading the host secret store — approve the prompt if one appears")
		},
	}
}

func reportSandboxLogin(rs *Spec, p *Params) {
	ui.Section(ui.SectionCredentials)
	if !credentials.NeedsSandboxLogin(rs.Man, true,
		rs.Creds.FileLogin, rs.Creds.StoreHeld, rs.Creds.Lookup) {
		return
	}
	argv := ""
	// Keyed on the DEF: a variant target (claudecode-browser) is the same
	// harness and the same built-in agent.
	if a := sbx.AuthLoginArgs(sbx.BuiltinAgent(rs.Man.Name), rs.Workspace.WS.InputDir); a != nil {
		argv = sbx.Binary + " " + strings.Join(a, " ")
	}
	lines := rs.Creds.Keychain.SandboxLoginHint(argv)
	ui.Hostf("%s", lines[0])
	for _, l := range lines[1:] {
		ui.Notef("%s", l)
	}
}

func reportKeychain(k credentials.KeychainLogin, sbxBackend, fileLogin bool) {
	ui.Section(ui.SectionCredentials)
	if line := k.Report(); line != "" {
		ui.Hostf("%s", line)
		if advice := k.KeychainAdvice(sbxBackend, fileLogin); advice != "" {
			ui.Notef("%s", advice)
		}
		return
	}
	if advice := k.KeychainFailureAdvice(); advice != "" {
		ui.Warnf("%s", advice)
	}
}

func resolveCredentials(rs *Spec, p *Params, d Deps) error {
	var err error
	rs.Creds.FileLogin, rs.Creds.LoginNeedsRefresh = credentials.PersistedLogin(p.Target, proveohome.Root(os.Getenv))
	rs.Creds.StoreHeld = sbxStoredAuth(rs.Man)
	rs.Creds.LoggedIn = rs.Creds.FileLogin || len(rs.Creds.StoreHeld) > 0
	if !p.PrintOnly {
		rs.Creds.Keychain = credentials.ReadKeychainLogin(
			p.Target, credentials.OSLookupEnv, hostStoreResolver(), time.Now())
		reportKeychain(rs.Creds.Keychain, true, rs.Creds.FileLogin)
		reportSandboxLogin(rs, p)
	}
	if rs.Creds.LoginNeedsRefresh && !p.PrintOnly {
		ui.Hostf("the login in the proveo home needs a refresh — the agent may report " +
			"\"Login expired\" on its first turn, and can only carry on if the renewal reaches the " +
			"provider from where it runs")
	}
	if rs.Creds.FileLogin && !p.PrintOnly && strings.TrimSpace(p.AuthVar) == "" {
		if av := credentials.EffectiveAuthVar(rs.Man, p.Target, p.AuthVar, proveohome.Root(os.Getenv), rs.Creds.Lookup); av != "" && strings.TrimSpace(rs.Creds.Lookup(av)) != "" {
			ui.Hostf("%s is set but not injected — the login in the proveo home is the credential, and an env token would override it", av)
		}
	}
	reportCredentialGaps(rs, p, d)

	rs.Workspace.WorktreeLinks, err = rs.Workspace.WS.PrepareWorktreeLinks(proveohome.Root(os.Getenv))
	if err != nil {
		ui.Warnf("git worktree: %v; falling back to GIT_DIR pinning", err)
	}
	rs.Workspace.WS.WorktreeLinkDir = rs.Workspace.WorktreeLinks

	rs.Workspace.Mounts, rs.Workspace.Links = rs.Workspace.WS.Plan()
	ui.Section(ui.SectionWorkspace)
	reportLinks(rs.Workspace.Links)

	rs.Creds.HomePlan, err = proveohome.Prepare(rs.Man.Home, os.Getenv)
	if err != nil {
		return err
	}
	if rs.Creds.HomePlan.Root != "" {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, rs.Creds.HomePlan.Mounts...)
		ui.Storef("%s", homeLine(rs.Creds.HomePlan.Root, rs.Man.Home))
	}

	if m, ok := credentials.GhConfigMount(os.Getenv); ok {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, m)
		ui.Hostf("gh session: %s mounted read-only", m.Host)
	}

	ui.Section(ui.SectionEgress)
	rs.Creds.Detected = credentials.FilterProviders(provider.Detect(rs.Creds.Lookup), rs.Man.Capabilities)
	withheld := credentials.WithheldProviders(rs.Man, p.Target, p.AuthVar,
		proveohome.Root(os.Getenv), rs.Creds.Lookup, rs.Creds.Detected)
	if len(withheld) > 0 {
		ui.Hostf("auth %q: %s withheld from sbx's secret store too, not just the agent's environment",
			p.AuthVar, strings.Join(withheld, ", "))
	}
	for _, msg := range p.Roles.MissingKeys(rs.Creds.Detected) {
		ui.Warnf("%s", msg)
	}
	for _, msg := range p.Roles.WithheldKeys(withheld, p.AuthVar) {
		ui.Warnf("%s", msg)
	}
	for _, msg := range p.Roles.BillingClashes(p.AuthVar) {
		ui.Warnf("%s", msg)
	}
	return nil
}

// reportCredentialGaps names what satisfies a subscription harness's missing variables, or prompts
// or warns for each required variable no credential covers.
// homeLine names the proveo home and the directories under it the sandbox sees, at their host paths.
func homeLine(root string, h manifest.Home) string {
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
	return fmt.Sprintf("proveo home: %s (the sandbox sees %s %s)", root, strings.Join(seen, ", "), where)
}

func reportCredentialGaps(rs *Spec, p *Params, d Deps) {
	missing := rs.Man.MissingEnv(rs.Creds.Lookup)
	if len(missing) == 0 || p.PrintOnly {
		return
	}
	switch {
	case rs.Man.Subscription && rs.Creds.FileLogin:
		ui.Hostf("%s: using the login persisted in the proveo home", rs.Man.Name)
		missing = nil
	case rs.Man.Subscription && len(rs.Creds.StoreHeld) > 0:
		ui.Hostf("%s: using %s from sbx's stored credentials — proveo can see that it is there, not what it holds",
			rs.Man.Name, strings.Join(rs.Creds.StoreHeld, ", "))
		missing = nil
	case rs.Man.Subscription:
		rs.Creds.AuthMissingAtStart = append([]manifest.EnvVar(nil), missing...)
		ui.Warnf("%s is starting without a credential — run `proveo init` to persist credentials between runs",
			rs.Man.Name)
	case agentio.IsStdinTTY() && WizardEnabled():
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
		ui.Warnf("%s", msg)
	}
}

// credentialsPosture reports where the run puts the provider key.
func credentialsPosture(rs *Spec, p *Params) string {
	if p.forwards() {
		return "forward (the key rides the sandbox env)"
	}
	return "broker (sbx proxy)"
}

func buildPosture(rs *Spec, p *Params) {
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
		Image:         posture.Image(p.Image),
		ModelRoles:    posture.RolesLine(p.Roles),
		RoleProviders: strings.Join(p.Roles.Providers(), ","),
		MCPGateway:    posture.MCPGateway(sandbox.MCPGatewayAllowed(), sandbox.MCPGatewayVar),
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
	dir := filepath.Join(rs.EgDir, "sbx", "project-env")
	var strip []string
	if !p.forwards() {
		strip = provider.KeyVars()
	}
	if p.PrintOnly {
		return filepath.Join(dir, ".env")
	}
	path, dropped, err := credentials.StageProjectEnv(src, dir, strip)
	if err == nil {
		err = copyFile0600(path, filepath.Join(rs.EgDir, "sbx", "project-env.base"))
	}
	if err != nil {
		ui.Warnf("clone: %s not staged (%v) — the agent runs without the project .env", src, err)
		return ""
	}
	rs.Backend.CloneEnvBase = filepath.Join(rs.EgDir, "sbx", "project-env.base")
	rs.Backend.CloneEnvHost, rs.Backend.CloneStrip = src, strip
	ui.Storef("clone: %s mounted read-write as the clone's .env (a copy, refreshed each run; edits merge back at teardown)", src)
	if len(dropped) > 0 {
		ui.Notef("clone: dropped %s from it — sbx's proxy attaches those, the agent never holds them (`--credentials forward` keeps them)", strings.Join(dropped, ", "))
	}
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

// assembleEnv checks the model variables the agent reads and exports the host's GitHub token.
func assembleEnv(rs *Spec, p *Params, d Deps) {
	for _, k := range credentials.ConfigVarsFor(rs.Man) {
		if v := strings.TrimSpace(rs.Creds.Lookup(k)); v != "" {
			warnUnknownModel(k, v, p.LocalModel)
		}
	}
	if !p.PrintOnly && d.GitHubTokenEnv != nil {
		d.GitHubTokenEnv()
	}
}

// launchSandbox resolves the clone and add-ons, then runs the agent in sbx (or prints the plan).
func launchSandbox(rs *Spec, p *Params, d Deps) error {
	if !p.PrintOnly && d.PreflightImages != nil && !sbx.HasTemplate(p.Image) {
		image, err := d.PreflightImages(rs.Man, p.Image)
		if err != nil {
			return err
		}
		p.Image = image
	}
	rs.Model.HostLLM = p.LocalModel != ""

	ui.Section(ui.SectionEgress)
	sandbox.WarnBaseline(sandbox.ReachHosts(rs.Man, rs.Creds.Detected))
	credentials.WarnMountedSecrets(rs.Workspace.WS.InputDir, rs.Creds.Lookup)

	ui.Section(ui.SectionExecution)
	ui.Appf("backend: docker sandboxes (sbx)")
	var err error
	rs.Backend.Clone, rs.Backend.CloneOff, err = decideClone(p, rs.Workspace.WS)
	if err != nil {
		return err
	}
	switch {
	case rs.Backend.Clone:
		ui.Storef("workspace: private clone — your checkout is NOT written. "+
			"The agent's commits are fetched back at teardown under refs/proveo/%s/ (`--clone=false` edits the checkout directly)", rs.Sid)
	case rs.Backend.CloneOff != "":
		ui.Notef("workspace: mounted checkout — clone default does not apply here: %s", rs.Backend.CloneOff)
	}
	if rs.Backend.Clone && workspace.LinkedWorktree(rs.Workspace.WS.InputDir) {
		rs.Backend.CloneSource, _ = workspace.CloneSource(rs.Workspace.WS.InputDir)
		ui.Notef("workspace: linked worktree — sbx clones the main worktree %s and the seed checks out %s; "+
			"uncommitted edits in %s stay on the host", rs.Backend.CloneSource.Main, rs.Backend.CloneSource.Ref, rs.Workspace.WS.InputDir)
	}
	if rs.Backend.Clone {
		rs.Backend.CloneEnv = stageCloneEnv(rs, p)
	}
	rs.Posture.Workspace = posture.Workspace(rs.Backend.Clone)
	if dropped := len(rs.Workspace.WS.DepCopies()); dropped > 0 && !rs.Backend.Clone {
		ok, why := workspace.SharedTreesAllowed(os.Getenv, workspace.HostPlatform(), workspace.ImagePlatform(os.Getenv))
		if !ok {
			return fmt.Errorf("sbx would mount your checkout with its %d dependency tree(s) (node_modules, target, .venv …): %s.\n"+
				"  %s\n"+
				"  Run from a git checkout where --clone applies, so the seed installs into a private clone, or set PROVEO_DEPS=shared to accept the rewrite",
				dropped, why, cloneOffHint(rs.Backend.CloneOff))
		}
		ui.Warnf("sbx mirrors the checkout, so its %d dependency tree(s) (node_modules, target, .venv …) cross into the sandbox as the host built them (%s);\n"+
			"  `--clone` keeps untracked trees out and installs fresh", dropped, why)
	}
	browserOn := hasAddon(p.Addons, addonBrowser) && rs.Backend.BrowserImage != ""
	cdpPort := 0
	if browserOn && !p.PrintOnly {
		cdpPort = sandbox.FreeLoopbackPort()
	}
	sbxBridge, _ := startChromeBridge(rs, p, chromebridge.SbxWhy)
	var sbxBridgeEnv []string
	if sbxBridge != nil {
		defer func() { _ = sbxBridge.Close() }()
		sbxBridgeEnv = sbxBridge.ResolvedEnv()
	}
	if rs.Model.HostLLM {
		if !p.PrintOnly {
			if err := sbx.EnsureHostOllama(p.LocalModel, os.Getenv, ui.Default); err != nil {
				return fmt.Errorf("local model %s: %w", p.LocalModel, err)
			}
			ui.Appf("local model: %s on the host's Ollama (host GPU)", p.LocalModel)
		}
	}
	var agentEnv []string
	if hostEnv, err := startHostCDP(p); err != nil {
		return err
	} else {
		agentEnv = append(agentEnv, hostEnv...)
	}
	adbEnv, err := startHostADB(p)
	if err != nil {
		return err
	}
	agentEnv = append(agentEnv, adbEnv...)
	agentEnv = append(agentEnv, operator.Env(proveohome.Root(os.Getenv))...)
	if rs.Model.HostLLM {
		agentEnv = append(agentEnv, sbx.LocalModelEnv(p.LocalModel)...)
		agentEnv = append(agentEnv, "SBX_CRED_ANTHROPIC_MODE=none", "SBX_CRED_OPENAI_MODE=none")
	}
	homeAccess, err := sandbox.PrepareHomeAccess(
		rs.Creds.HomePlan.Root, rs.EgDir, rs.Man.Home)
	if err != nil {
		return err
	}
	in := sandbox.Input{
		AgentEnv: agentEnv,
		Target:   p.Target, Image: p.Image, AuthVar: p.AuthVar,
		Clone: rs.Backend.Clone, Extra: p.Extra,
		RepoRoot: rs.Workspace.WS.RepoRoot, OutputDir: p.Output,
		Browser: browserOn, CDPHostPort: cdpPort,
		Ports:       planPorts(p.Ports, rs.Choices.PortsRoot, p.PrintOnly),
		AndroidApps: androidApps(p), AndroidEnv: adbEnv,
		Roles:    p.Roles,
		Evidence: p.evidenceOrDefault(),
		Forwards: p.forwards(),
		Man:      rs.Man, Sid: rs.Sid, EgDir: rs.EgDir,
		ImageEntrypoint:  sbx.ImageEntrypoint,
		Mounts:           rs.Workspace.Mounts,
		Lookup:           rs.Creds.Lookup,
		Detected:         rs.Creds.Detected,
		GitEnv:           gitidentity.Resolve(os.Getenv, nil).EnvPairs(),
		HomeEnv:          rs.Creds.HomePlan.Env,
		BridgeEnv:        sbxBridgeEnv,
		ScopeRel:         rs.Workspace.WS.ScopeRel(),
		WorktreeFallback: rs.Workspace.WS.WorktreeLinkDir == "",
		WorktreeEnv:      rs.Workspace.WS.WorktreeEnv(),
		CloneSource:      rs.Backend.CloneSource,
		CloneEnv:         rs.Backend.CloneEnv,
		CloneEnvBase:     rs.Backend.CloneEnvBase,
		CloneEnvHost:     rs.Backend.CloneEnvHost,
		ProjectEnv:       workspace.ProjectEnvFile(rs.Workspace.WS.InputDir, rs.Workspace.WS.RepoRoot),
		CloneEnvStrip:    rs.Backend.CloneStrip,
		Links:            rs.Workspace.Links,
		DataDir:          p.DataDir,
		Memory:           sbx.MemoryLimit(),
		CPUs:             sbx.CPULimit(),
		HomeRoot:         rs.Creds.HomePlan.Root,
		RunLog:           rs.Log.Path(),
		HomeAccess:       homeAccess,
	}
	if p.PrintOnly {
		defer homeAccess.Cleanup()
		cfg, kit, secrets := sandbox.Spec(in)
		if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
			return fmt.Errorf("write sandbox kit: %w", err)
		}
		ui.Storef("sandbox kit: %s (removed by `proveo clean`)", filepath.Join(cfg.KitDir, "spec.yaml"))
		if len(secrets) > 0 {
			names := make([]string, 0, len(secrets))
			for _, kv := range secrets {
				names = append(names, kv[0])
			}
			ui.Warnf("printed command needs these in sbx's secret store first: %s (`sbx secret set NAME`)", strings.Join(names, ", "))
		}
		fmt.Printf("# agent\nsbx %s\n", strings.Join(sbx.RunArgs(cfg), " "))
		return nil
	}
	rs.AgentLaunched = true
	return sandbox.Run(in)
}

// recordOutcome writes the run's verdict into the transcript before Do returns
// and the log closes.
func realPathOf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// rememberedPorts maps a saved answer onto this run's discovery; a port whose command is gone is dropped and reported.
func rememberedPorts(saved []agentsettings.Port, found []devports.Candidate) []devports.Candidate {
	var out []devports.Candidate
	for _, sp := range saved {
		i := slices.IndexFunc(found, func(c devports.Candidate) bool { return c.Port == sp.Port })
		if i < 0 {
			ui.Warnf("remembered port %d (%q in %s) is no longer in this workspace — not published", sp.Port, sp.Command, sp.Source)
			continue
		}
		out = append(out, found[i])
	}
	return out
}

// rememberedApps maps a saved answer onto this run's discovery; a module that is gone is dropped and reported.
// androidApps is what the run installs: the chosen modules, only with the android add-on on.
func androidApps(p *Params) []devports.AndroidApp {
	if !hasAddon(p.Addons, addonAndroid) {
		return nil
	}
	return p.Apps
}

func rememberedApps(saved []agentsettings.App, found []devports.AndroidApp) []devports.AndroidApp {
	var out []devports.AndroidApp
	for _, sa := range saved {
		i := slices.IndexFunc(found, func(a devports.AndroidApp) bool { return a.Module == sa.Module })
		if i < 0 {
			ui.Warnf("remembered Android app %s (%s) is no longer in this workspace — not installed", sa.AppID, sa.Module)
			continue
		}
		out = append(out, found[i])
	}
	return out
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

func planPorts(chosen []devports.Candidate, root string, printOnly bool) []sandbox.PublishedPort {
	free := sandbox.LoopbackFree
	if printOnly {
		free = func(int) bool { return true }
	}
	out := make([]sandbox.PublishedPort, 0, len(chosen))
	for _, c := range chosen {
		dir, cmd := devports.Launch(c, root)
		out = append(out, sandbox.PublishedPort{Guest: c.Port, What: c.Tool + " · " + c.Source, Dir: dir, Cmd: cmd})
	}
	return sandbox.PlanPorts(out, free)
}

func recordOutcome(launched bool, err error) {
	var ae backend.ExitError
	switch {
	case err == nil && !launched:
		ui.Logf("outcome: no agent was launched")
	case err == nil:
		ui.Logf("outcome: the agent exited 0")
	case errors.As(err, &ae):
		ui.Section(ui.SectionResults)
		ui.Failf("the agent exited with code %d", ae.Code)
	default:
		ui.Logf("outcome: %v", err)
	}
}
