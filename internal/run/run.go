package run

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/reviewgate"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/secretref"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Deps are the host- and terminal-bound capabilities a run needs.
type Deps struct {
	ManifestFor      func(target string) (manifest.Manifest, error)
	PickProject      func(projs []workspace.Project) string
	PromptEnv        func(target string, missing []manifest.EnvVar) map[string]string
	GitHubTokenEnv   func(interactive bool) string
	ProvisionConfirm func(question string) bool
	PreflightImages  func(plan egress.Plan, man manifest.Manifest, agentImage string) error
	SquidConfig      fs.FS // the root package's embedded squid config
	ModelBridges     fs.FS // and its model bridge tables — same reason: internal/ never imports the root
}

func Do(p Params, d Deps) error {
	var rs Spec
	rs.UID, rs.GID = strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
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

	var err error
	rs.Man, err = d.ManifestFor(p.Target)
	if err != nil {
		return err
	}
	rs.SquidConfig = d.SquidConfig
	rs.Log.Artifacts(rs.EgDir, p.willSandbox(rs.Man))

	if p.Target == "cursor" && p.LocalModel != "" {
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
	if err := assembleEnv(&rs, &p, d); err != nil {
		return err
	}
	if done, err := selectBackend(&rs, &p, d); err != nil || done {
		return err
	}
	defer stageDeps(&rs, &p)()
	return execute(&rs, &p, d)
}

func stageDeps(rs *Spec, p *Params) func() {
	none := func() {}
	if p.PrintOnly {
		return none
	}
	copies := rs.Workspace.WS.DepCopies()
	if len(copies) == 0 {
		return none
	}
	reuse, why := workspace.DepCopyPolicy(os.Getenv, workspace.HostPlatform(), workspace.ImagePlatform(os.Getenv))
	made, err := workspace.MaterializeDeps(copies, reuse)
	if err != nil {
		ui.Warnf("dependency trees: %v", err)
	}
	copied := map[string]bool{}
	for _, c := range made {
		copied[c.Container] = true
	}
	var parts []string
	for _, c := range copies {
		if copied[c.Container] {
			parts = append(parts, c.Container+" (copied)")
		} else {
			parts = append(parts, c.Container+" (empty)")
		}
	}
	ui.Appf("dependency trees isolated, host untouched: %s", why)
	ui.Notef("%s", strings.Join(parts, ", "))
	stage := rs.Workspace.WS.DepStage
	return func() {
		if stage != "" {
			_ = os.RemoveAll(stage)
		}
	}
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
		Workspace: rs.Man.Workspace, OutputDir: p.Output, EgressMode: p.Mode, Credentials: p.Credentials,
		MountRootDeps: mountRootDeps(os.Getenv),
		DepStage:      filepath.Join(rs.EgDir, "deps"),
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
		rs.Creds.HostEnvFile = workspace.EnvFileSource(rs.InvocationWD, rs.Workspace.WS.InputDir, rs.Workspace.WS.RepoRoot)
	}
	rs.Creds.Lookup = credentials.ProviderLookup(rs.Creds.HostEnvFile)

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
	if err := p.applyCapabilities(rs.Man.Capabilities); err != nil {
		return err
	}
	if rs.Choices.Promptable {
		if cached, ok := rs.Choices.Settings.Lookup(p.Target, rs.Man.Capabilities); ok {
			p.seedFromCache(cached, rs.Creds.Lookup, rs.Choices.EvidenceSet)
		}
	}
	if p.Bridges == nil {
		if tab, err := provider.LoadBridges(d.ModelBridges); err == nil {
			p.Bridges = tab
		} else {
			ui.Warnf("model bridge tables unreadable (%v); the header will list role variables instead of resolved slots", err)
		}
	}
	if p.Roles == nil {
		p.Roles = provider.RolesFrom(rs.Creds.Lookup)
	}
	if rs.Choices.Promptable {
		if err := p.promptChoices(rs.Man, rs.Creds.Lookup, gitRootOrEmpty(rs.Workspace.Scope, rs.Workspace.RepoRoot), rs.Choices.SettingsRoot); err != nil {
			return err
		}
	}
	if err := p.applyCapabilities(rs.Man.Capabilities); err != nil {
		return err
	}
	if rs.Choices.Promptable {
		rs.Choices.Settings.Remember(p.Target, rs.Man.Capabilities, agentsettings.Choice{
			Egress: p.Mode, Credentials: p.credentialsOrDefault(), Addons: p.Addons, AuthVar: p.AuthVar,
			Evidence: p.evidenceOrDefault(), Models: p.Roles.Canonical(),
		})
		if err := rs.Choices.Settings.Save(rs.Choices.SettingsRoot); err != nil {
			ui.Warnf("%v", err)
		}
	}
	rs.Workspace.WS.EgressMode, rs.Workspace.WS.Credentials = p.Mode, p.Credentials

	if p.Mode == "review" {
		if ok, why := dockeregress.ReviewSupported(os.Getenv); !ok {
			ui.Warnf("--egress-mode review is %s: the consent gate cannot be reached from the "+
				"inspector on this host, so every new connection will be DENIED without a prompt", why)
		}
	}
	if p.Target == "cursor" && p.intercepts() {
		ui.Warnf("cursor + --egress-mode %s --credentials %s: cursor-agent pins its TLS, so any intercepting tier "+
			"breaks it (it reports \"invalid API key\") — use --egress-mode open --credentials forward",
			p.Mode, p.credentialsOrDefault())
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
	warnDindRetired()

	return nil
}

// SPEC: _spec/_plans/retire-dind.puml
func warnDindRetired() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_DIND"))) {
	case "", "0", "false", "no", "off":
		return
	}
	ui.Warnf("PROVEO_DIND is retired and does nothing: the privileged Docker-in-Docker " +
		"sidecar is gone. A harness that declares `docker: sbx` gets its own daemon inside " +
		"the sandbox instead; unset the variable")
}

// SPEC: _spec/defs/claudecode/chrome-bridge.puml
func startChromeBridge(rs *Spec, p *Params, tierBlocked string) (*chromebridge.Relay, []string) {
	ui.Section(ui.SectionInterface)
	if !hasAddon(p.Addons, addonChrome) {
		return nil, nil
	}
	switch {
	case p.PrintOnly:
		ui.Hostf("%s: the run starts a host relay and sets %s + %s on the agent (not started in print mode)",
			addonChrome, chromebridge.EnvAddr, chromebridge.EnvToken)
		return nil, nil
	case tierBlocked != "":
		ui.Warnf("%s: skipped — %s", addonChrome, tierBlocked)
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

// SPEC: _spec/internal/sbx/oauth-provisioning.puml
func reportSandboxLogin(rs *Spec, p *Params) {
	ui.Section(ui.SectionCredentials)
	if !credentials.NeedsSandboxLogin(rs.Man, p.willSandbox(rs.Man),
		rs.Creds.FileLogin, rs.Creds.StoreHeld, rs.Creds.Lookup) {
		return
	}
	argv := ""
	if a := sbx.AuthLoginArgs(sbx.BuiltinAgent(p.Target), rs.Workspace.WS.InputDir); a != nil {
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
	rs.Creds.StoreHeld = sbxStoredAuth(rs.Man, p)
	rs.Creds.LoggedIn = rs.Creds.FileLogin || len(rs.Creds.StoreHeld) > 0
	if !p.PrintOnly {
		rs.Creds.Keychain = credentials.ReadKeychainLogin(
			p.Target, credentials.OSLookupEnv, hostStoreResolver(), time.Now())
		reportKeychain(rs.Creds.Keychain, p.willSandbox(rs.Man), rs.Creds.FileLogin)
		reportSandboxLogin(rs, p)
	}
	if rs.Creds.LoginNeedsRefresh && !p.PrintOnly {
		ui.Hostf("the login in the proveo home needs a refresh — the agent may report " +
			"\"Login expired\" on its first turn, and can only carry on if the renewal reaches the " +
			"provider from where it runs")
	}
	if rs.Creds.FileLogin && !p.PrintOnly && strings.TrimSpace(p.AuthVar) == "" {
		if av := credentials.EffectiveAuthVar(rs.Man, p.Target, p.AuthVar, proveohome.Root(os.Getenv)); av != "" && strings.TrimSpace(rs.Creds.Lookup(av)) != "" {
			ui.Hostf("%s is set but not injected — the login in the proveo home is the credential, and an env token would override it", av)
		}
	}
	if missing := rs.Man.MissingEnv(rs.Creds.Lookup); len(missing) > 0 && !p.PrintOnly {
		switch {
		case rs.Man.Subscription && rs.Creds.FileLogin:
			ui.Hostf("%s: using the login persisted in the proveo home", rs.Man.Name)
		case rs.Man.Subscription && len(rs.Creds.StoreHeld) > 0:
			ui.Hostf("%s: using %s from sbx's stored credentials — proveo can see that it is there, not what it holds",
				rs.Man.Name, strings.Join(rs.Creds.StoreHeld, ", "))
		case rs.Man.Subscription:
			rs.Creds.AuthMissingAtStart = append([]manifest.EnvVar(nil), missing...)
			ui.Warnf("no auth present for subscription agent %s — running anyway; the agent will handle login", rs.Man.Name)
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

	rs.Workspace.WorktreeLinks, err = rs.Workspace.WS.PrepareWorktreeLinks(proveohome.Root(os.Getenv))
	if err != nil {
		ui.Warnf("git worktree: %v; falling back to GIT_DIR pinning", err)
	}
	rs.Workspace.WS.WorktreeLinkDir = rs.Workspace.WorktreeLinks

	var planWorkdir string
	rs.Workspace.Mounts, planWorkdir, rs.Workspace.Links = rs.Workspace.WS.Plan()
	if planWorkdir != "" {
		rs.Workspace.Workdir = planWorkdir
	}
	ui.Section(ui.SectionWorkspace)
	reportLinks(rs.Workspace.Links)

	rs.Creds.HomePlan, err = proveohome.Prepare(rs.Man.Home, os.Getenv)
	if err != nil {
		return err
	}
	if rs.Creds.HomePlan.Root != "" {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, rs.Creds.HomePlan.Mounts...)
		ui.Storef("proveo home: %s (mounted at %s)", rs.Creds.HomePlan.Root, proveohome.ContainerHome)
	}

	if m, ok := credentials.GhConfigMount(os.Getenv); ok {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, m)
		ui.Hostf("gh session: %s mounted read-only", m.Host)
	}

	ui.Section(ui.SectionEgress)
	rs.Creds.Detected = credentials.FilterProviders(provider.Detect(rs.Creds.Lookup), rs.Man.Capabilities)
	rs.Creds.Brokered = credentials.BrokerProviders(p.forwards(), rs.Man, rs.Creds.Detected, rs.Creds.Lookup, brokerEnabled())
	if reason := credentials.BrokerOffReason(p.forwards(), rs.Creds.Brokered, rs.Creds.Detected, brokerEnabled()); reason != "" {
		ui.Warnf("%s", reason)
	}
	if len(rs.Creds.Brokered) > 1 {
		ui.Hostf("broker: %d providers injected at the egress layer (%s)",
			len(rs.Creds.Brokered), strings.Join(rs.Creds.Brokered, ", "))
	}
	for _, msg := range p.Roles.MissingKeys(rs.Creds.Detected) {
		ui.Warnf("%s", msg)
	}
	for _, r := range p.Bridges.RefusedSlots(p.Target, p.Roles) {
		ui.Warnf("%s", r.Reason())
	}
	return nil
}

func buildPosture(rs *Spec, p *Params) {
	rs.Posture = posture.Posture{
		Target:         p.Target,
		EgressTier:     p.Mode,
		Credentials:    p.credentialsOrDefault(),
		AddOns:         strings.Join(p.Addons, ","),
		AgentEvidence:  p.evidenceOrDefault(),
		DetectedKeys:   strings.Join(rs.Creds.Detected, ","),
		Brokered:       strings.Join(rs.Creds.Brokered, ","),
		ReachableHosts: strings.Join(credentials.ReachableHosts(rs.Creds.Detected), ","),
		HarnessHosts:   strings.Join(rs.Man.Capabilities.Hosts, ","),
		AuthVar:        p.AuthVar,
		LocalModel:     p.LocalModel,
		Observability:  posture.Observability(p.Mode, p.credentialsOrDefault(), p.willSandbox(rs.Man)),
		EnforcedBy:     posture.EnforcedBy(p.willSandbox(rs.Man)),
		Image:          posture.Image(p.Image),
		ModelRoles:     posture.RolesLine(p.Bridges, p.Target, p.Roles),
		RoleProviders:  strings.Join(p.Roles.Providers(), ","),
		MCPGateway:     posture.MCPGateway(p.willSandbox(rs.Man), sandbox.MCPGatewayAllowed(), sandbox.MCPGatewayVar),
		Workspace:      posture.Workspace(predictClone(p, p.willSandbox(rs.Man), rs.Workspace.WS)),
	}
	rs.Log.Fields("resolved posture", rs.Posture.Fields())
}

// SPEC: _spec/internal/sbx/clone-workspace.puml
func decideClone(p *Params, sbxBackend bool, ws workspace.MountSpec) (on bool, whyOff string, err error) {
	if !p.Clone {
		return false, "", nil // --clone=false or PROVEO_CLONE=off: the mounted checkout, by choice
	}
	switch {
	case !sbxBackend:
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone is an sbx-backend feature and this run is on docker+egress:\n" +
				"  the agent would edit your checkout directly, which is what --clone asks it not to do.\n" +
				"  Re-run without --clone, or on a target whose manifest declares `docker: sbx`")
		}
		return false, "", nil // docker has no clone; nothing to announce
	case ws.RepoRoot == "":
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone needs a git repository and %s is not inside one:\n"+
				"  sbx builds the sandbox workspace by cloning the host repo over a git daemon.\n"+
				"  Run it from a checkout, or drop --clone to work on the mounted directory",
				ws.InputDir)
		}
		return false, "not a git repository — sbx clones with git", nil
	case workspace.LinkedWorktree(ws.InputDir):
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone cannot clone a linked git worktree (%s):\n"+
				"  sbx clones the MAIN worktree only. Run from the main checkout, or drop --clone",
				ws.InputDir)
		}
		return false, "linked git worktree — sbx clones only the main worktree", nil
	case ws.ScopeRel() != "":
		if p.CloneSet {
			return true, "", nil
		}
		return false, "monorepo sub-scope — the primary workspace has to be the repository root", nil
	}
	return true, "", nil
}

func predictClone(p *Params, sbxBackend bool, ws workspace.MountSpec) bool {
	on, _, err := decideClone(p, sbxBackend, ws)
	return err == nil && on
}

func assembleEnv(rs *Spec, p *Params, d Deps) error {
	if len(rs.Creds.Brokered) > 0 {
		if p.PrintOnly {
			rs.Creds.BrokerFile = filepath.Join(rs.EgDir, "inject", "broker.env") // path only in dry-run
		} else if f, err := credentials.WriteBrokerEnv(filepath.Join(rs.EgDir, "inject"), rs.Creds.Lookup); err == nil {
			rs.Creds.BrokerFile = f
		} else {
			ui.Warnf("broker secret file: %v", err)
		}
	}

	if p.LocalModel != "" {
		rs.Model.ModelsDir = ollamaModelsDir()
		rs.Model.HostOllama = preferHostOllama()
		rs.Model.OllamaGPU = sidecarOllamaGPU()
	}

	suppressedAuth := credentials.AuthSuppressor(rs.Man, p.Target, p.AuthVar, proveohome.Root(os.Getenv))
	for _, e := range rs.Man.Env {
		if strings.TrimSpace(rs.Creds.Lookup(e.Name)) == "" {
			continue
		}
		if e.Secret {
			if suppressedAuth(e.Name) {
				continue
			}
			if p.forwards() {
				rs.Creds.Env = append(rs.Creds.Env, e.Name)
				rs.Creds.Child.Add(e.Name, rs.Creds.Lookup)
			} else {
				rs.Creds.Env = append(rs.Creds.Env, e.Name+"="+entrypoint.DefaultSentinel)
				rs.Creds.BrokerKeyNames = append(rs.Creds.BrokerKeyNames, e.Name)
			}
			continue
		}
		rs.Creds.Env = append(rs.Creds.Env, e.Name)
	}
	if !p.forwards() {
		for _, k := range provider.KeyVars() {
			if strings.TrimSpace(rs.Creds.Lookup(k)) == "" {
				continue
			}
			already := false
			for _, n := range rs.Creds.BrokerKeyNames {
				if n == k {
					already = true
					break
				}
			}
			if !already {
				rs.Creds.Env = append(rs.Creds.Env, k+"="+entrypoint.DefaultSentinel)
				rs.Creds.BrokerKeyNames = append(rs.Creds.BrokerKeyNames, k)
			}
		}
		if len(rs.Creds.BrokerKeyNames) > 0 {
			rs.Creds.Env = append(rs.Creds.Env, "PROVEO_CREDENTIAL_BROKER_KEYS="+strings.Join(rs.Creds.BrokerKeyNames, ","))
		}
	}
	for _, k := range credentials.ConfigVarsFor(rs.Man) {
		if v := strings.TrimSpace(rs.Creds.Lookup(k)); v != "" {
			rs.Creds.Env = append(rs.Creds.Env, k+"="+v)
			warnUnknownModel(k, v, p.LocalModel)
		}
	}
	rs.Creds.Env = append(rs.Creds.Env, EvidenceVar+"="+p.evidenceOrDefault())
	rs.Creds.Env = append(rs.Creds.Env, rs.Man.AgentEnvPairs(rs.Creds.Lookup)...)
	rs.Creds.Env = append(rs.Creds.Env, gitidentity.Resolve(os.Getenv, nil).EnvPairs()...)
	rs.Creds.Env = append(rs.Creds.Env, rs.Creds.HomePlan.Env...)
	if rel := rs.Workspace.WS.ScopeRel(); rel != "" {
		rs.Creds.Env = append(rs.Creds.Env, "PROVEO_SCOPE_REL="+rel)
	}
	if rs.Workspace.WS.WorktreeLinkDir == "" {
		rs.Creds.Env = append(rs.Creds.Env, rs.Workspace.WS.WorktreeEnv()...)
	}

	if !p.PrintOnly {
		if k := d.GitHubTokenEnv(agentio.IsStdinTTY() && WizardEnabled()); k != "" {
			rs.Creds.Env = append(rs.Creds.Env, k)
		}
	}

	return nil
}

func selectBackend(rs *Spec, p *Params, d Deps) (bool, error) {
	rs.Backend.Sbx = false
	sbxUnavailable := ""
	if rs.Man.IsSbx() && p.Mode != "review" && sandbox.Enabled() {
		if ok, why := sandbox.Ready(p.PrintOnly, d.ProvisionConfirm); ok {
			rs.Backend.Sbx = true
		} else {
			sbxUnavailable = why
		}
	}

	ui.Section(ui.SectionEgress)
	if rs.Backend.Sbx {
		sandbox.WarnBaseline()
	}
	credentials.WarnMountedSecrets(rs.Workspace.WS.InputDir, p.Mode, rs.Backend.Sbx, rs.Creds.Lookup)

	ui.Section(ui.SectionExecution)
	switch {
	case sbxUnavailable != "":
		sandbox.ReportUnavailable(sbxUnavailable)
	case rs.Backend.Sbx:
		ui.Appf("backend: docker sandboxes (sbx)")
		if hasAddon(p.Addons, addonChrome) {
			ui.Warnf("%s: skipped — a sandbox VM cannot reach the host's Claude in Chrome socket; set PROVEO_SBX=0 to use it", addonChrome)
		}
	}
	// SPEC: _spec/_plans/retire-dind.puml
	var err error
	rs.Backend.Clone, rs.Backend.CloneOff, err = decideClone(p, rs.Backend.Sbx, rs.Workspace.WS)
	if err != nil {
		return false, err
	}
	if rs.Backend.Sbx {
		switch {
		case rs.Backend.Clone:
			ui.Storef("workspace: private clone — your checkout is NOT written. "+
				"The agent's commits are fetched back at teardown under refs/proveo/%s/ (`--clone=false` edits the checkout directly)", rs.Sid)
		case rs.Backend.CloneOff != "":
			ui.Notef("workspace: mounted checkout — clone default does not apply here: %s", rs.Backend.CloneOff)
		}
	}
	rs.Posture.Workspace = posture.Workspace(rs.Backend.Clone)
	if rs.Backend.Sbx {
		mounts, dropped := workspace.StripDepCopies(rs.Workspace.Mounts, rs.Workspace.WS.DepStage)
		if dropped > 0 && !rs.Backend.Clone {
			ui.Warnf("sbx mirrors the checkout, so its %d dependency tree(s) (node_modules, target, .venv …) cross into the sandbox as the host built them;\n"+
				"  the seed rebuilds a foreign tree only with PROVEO_DEPS=reinstall (it rewrites your checkout) — `--clone` keeps untracked trees out and installs fresh", dropped)
		}
		browserOn := hasAddon(p.Addons, addonBrowser) && rs.Backend.BrowserImage != ""
		cdpPort := 0
		if browserOn && !p.PrintOnly {
			cdpPort = sandbox.FreeLoopbackPort()
		}
		sbxBridge, sbxBridgeEnv := startChromeBridge(rs, p, "")
		if sbxBridge != nil {
			defer func() { _ = sbxBridge.Close() }()
		}
		in := sandbox.Input{
			Target: p.Target, Image: p.Image, AuthVar: p.AuthVar,
			Shell: p.Shell, Clone: rs.Backend.Clone, Extra: p.Extra,
			RepoRoot: rs.Workspace.WS.RepoRoot, OutputDir: p.Output,
			Browser: browserOn, CDPHostPort: cdpPort,
			Roles: p.Roles, Bridges: p.Bridges,
			Evidence: p.evidenceOrDefault(),
			Forwards: p.forwards(),
			Man:      rs.Man, Sid: rs.Sid, EgDir: rs.EgDir,
			Mounts: mounts, Workdir: rs.Workspace.Workdir,
			Lookup:           rs.Creds.Lookup,
			Detected:         rs.Creds.Detected,
			GitEnv:           gitidentity.Resolve(os.Getenv, nil).EnvPairs(),
			HomeEnv:          rs.Creds.HomePlan.Env,
			BridgeEnv:        sbxBridgeEnv,
			ScopeRel:         rs.Workspace.WS.ScopeRel(),
			WorktreeFallback: rs.Workspace.WS.WorktreeLinkDir == "",
			WorktreeEnv:      rs.Workspace.WS.WorktreeEnv(),
			DataDir:          p.DataDir,
			Memory:           sbx.MemoryLimit(),
			CPUs:             sbx.CPULimit(),
			HomeRoot:         rs.Creds.HomePlan.Root,
			RunLog:           rs.Log.Path(),
		}
		if p.PrintOnly {
			cfg, kit, secrets := sandbox.Spec(in)
			if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
				return false, fmt.Errorf("write sandbox kit: %w", err)
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
			return true, nil
		}
		if len(rs.Creds.AuthMissingAtStart) > 0 {
			if rs.Man.Subscription && !rs.Creds.LoggedIn {
				credentials.PrintSubscriptionAuthHints(rs.Man, rs.Creds.AuthMissingAtStart, os.Stderr)
				sh, _ := shell.Detect(os.Getenv("SHELL"))
				return false, fmt.Errorf("%s needs a subscription login and the sbx backend cannot complete one:\n"+
					"  the agent exits at its login prompt and the sandbox stops with it.\n"+
					"  Mint a token on the host and export it:\n"+
					"      claude setup-token\n"+
					"      %s\n"+
					"  Or use --egress-mode review, which runs on the docker backend where a login persists",
					rs.Man.Name, sh.ExportLine("CLAUDE_CODE_OAUTH_TOKEN", "<token>"))
			}
			credentials.PrintSubscriptionAuthHints(rs.Man, rs.Creds.AuthMissingAtStart, os.Stderr)
		}
		return true, sandbox.Run(in)
	}

	return false, nil
}

func execute(rs *Spec, p *Params, d Deps) error {
	if !p.PrintOnly {
		if err := d.PreflightImages(egress.Plan{}, rs.Man, p.Image); err != nil {
			return err
		}
	}
	rs.Docker.Host = runner.DetectHost(p.Image)
	rs.Docker.Browser = runner.IsBrowserImage(p.Image)
	ov, ovSet := runner.ParsePidsOverride(os.Getenv("PROVEO_PIDS_LIMIT"))
	if err := runner.EnsurePidsCapability(rs.Docker.Host, rs.Docker.Browser, ov, ovSet); err != nil {
		return err
	}
	rs.Docker.PidsLimit = runner.ResolvePidsLimit(rs.Docker.Host, rs.Docker.Browser, ov, ovSet)

	tierBlocked := ""
	if !chromebridge.TierSupported(p.Mode, p.credentialsOrDefault()) {
		tierBlocked = "needs --egress-mode open --credentials forward (an intercepting tier puts the agent on an internal network with no route to the host)"
	}
	bridge, bridgeEnv := startChromeBridge(rs, p, tierBlocked)
	if bridge != nil {
		defer func() { _ = bridge.Close() }()
	}
	rs.Creds.Env = append(rs.Creds.Env, bridgeEnv...)

	consent, reviewProxy := reviewConsent(p.Mode)
	reviewGate, stopReview := dockeregress.StartReviewGate(p.Mode, rs.EgDir, consent)
	defer stopReview()
	rs.Docker.ReviewSocket = ""
	if reviewGate != nil {
		rs.Docker.ReviewSocket = reviewgate.Path(filepath.Join(rs.EgDir, "review"))
	}

	plan, agent, err := dockeregress.Assemble(dockeregress.Input{
		Target: p.Target, Image: p.Image, AuthVar: p.AuthVar,
		Mode: p.Mode, Credentials: p.Credentials,
		LocalModel: p.LocalModel, DataDir: p.DataDir,
		Shell: p.Shell, Extra: p.Extra,
		Sid: rs.Sid, EgDir: rs.EgDir, UID: rs.UID, GID: rs.GID,
		ReviewSocket:  rs.Docker.ReviewSocket,
		WriteHosts:    credentials.ReachableHosts(rs.Creds.Detected),
		ProviderHosts: policyProviderHosts(rs.Creds.Detected, rs.Man.Capabilities),
		ModelsDir:     rs.Model.ModelsDir, Providers: rs.Creds.Brokered, BrokerFile: rs.Creds.BrokerFile,
		HostOllama: rs.Model.HostOllama, OllamaGPU: rs.Model.OllamaGPU,
		HostBridge: bridge != nil,
		Mounts:     rs.Workspace.Mounts, Workdir: rs.Workspace.Workdir, Env: rs.Creds.Env,
		ChildEnv:        rs.Creds.Child.Pairs(),
		ProviderDomains: credentials.JoinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), rs.Man.Capabilities.Hosts),
		SquidImage:      os.Getenv("PROVEO_SQUID_PROXY_IMAGE"),
		ProxyImage:      orElseFirst(p.ProxyImage, []string{os.Getenv("PROVEO_EGRESS_PROXY_IMAGE")}),
		OllamaImage:     os.Getenv("PROVEO_OLLAMA_IMAGE"),
		PidsLimit:       rs.Docker.PidsLimit,
	})
	if err != nil {
		return err
	}

	if p.PrintOnly {
		fmt.Print(plan.Render())
		fmt.Printf("# agent\ndocker %s\n", strings.Join(runner.DockerRunArgs(agent), " "))
		return nil
	}
	if err := d.PreflightImages(plan, rs.Man, p.Image); err != nil {
		return err
	}
	if len(rs.Creds.AuthMissingAtStart) > 0 {
		credentials.PrintSubscriptionAuthHints(rs.Man, rs.Creds.AuthMissingAtStart, os.Stderr)
	}
	runErr := func() error {
		if !dockeregress.NeedsLifecycle(plan) {
			return dockeregress.ExecAgentWithProxy(agent, reviewProxy)
		}
		squidProviders := rs.Creds.Detected
		if strings.TrimSpace(rs.Man.Provider) != "" && len(rs.Creds.Brokered) == 1 {
			squidProviders = rs.Creds.Brokered
		}
		return dockeregress.Exec(rs.SquidConfig, plan, agent, rs.EgDir, squidProviders, reviewProxy)
	}()
	return runErr
}
