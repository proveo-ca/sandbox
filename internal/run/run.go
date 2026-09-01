package run

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/dind"
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
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Deps are the host- and terminal-bound capabilities a run needs. They are
// injected so this package holds no terminal wiring of its own: cmd/proveo owns
// the prompts, the gh session and the image pulls, and supplies them here. It is
// also what lets a run be driven from a test without a PTY.
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
	// The run's resolved contract. Every stage below reads and extends it; nothing
	// that outlives a stage is kept in a local any more.
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
		ui.Iconf("📝", "run log: %s", rs.Log.Path())
	}

	var err error
	rs.Man, err = d.ManifestFor(p.Target)
	if err != nil {
		return err
	}
	rs.SquidConfig = d.SquidConfig
	// Recorded after the manifest resolves, because which artifacts exist depends on
	// which backend will run and that is a property of the harness.
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

// stageDeps materialises the dependency-tree copies the mount plan named, and
// returns the function that removes them once the run is over.
//
// Docker only: selectBackend has already returned for sbx, which cannot express
// a nested overlay and strips the copies instead. Never for --print, which must
// not copy a tree to render an argv. The copies are disposable by construction
// — the seed reinstalls into an empty one — so nothing is lost by removing them,
// and leaving multi-gigabyte node_modules copies to pile up under the state dir
// until someone runs `proveo clean` is the leak this avoids.
func stageDeps(rs *Spec, p *Params) func() {
	none := func() {}
	if p.PrintOnly {
		return none
	}
	copies := rs.Workspace.WS.DepCopies()
	if len(copies) == 0 {
		return none
	}
	// Copy only when the tree can run here. A macOS-built node_modules in a Linux
	// container is foreign in every native module, and the seed would clear the
	// copy before reinstalling — so on a platform mismatch the overlays start
	// empty and the install IS the plan. The image platform is what the defs
	// build (linux/<arch>) as docker will pick it; see workspace.ImagePlatform.
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
	ui.Iconf("📦", "dependency trees isolated, host untouched: %s", why)
	ui.Iconf("📦", "  %s", strings.Join(parts, ", "))
	stage := rs.Workspace.WS.DepStage
	return func() {
		if stage != "" {
			_ = os.RemoveAll(stage)
		}
	}
}

// resolveWorkspace settles WHERE the run happens: the input dir, the repo it sits
// in, the scope the operator picked, and the mount plan those imply. The credential
// lookup is built here too — which env file answers depends on those same dirs.
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
	// Create it here rather than leaving it to the backend. `docker run -v` invents
	// a missing host path, but as ROOT — which is why callers have been creating it
	// themselves to get a dir the run-as user can write. sbx does not invent it at
	// all: it stops and asks "The selected workspace does not exist. Would you like
	// to create it? (y/N)", which is a prompt no unattended run answers.
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
		ui.Iconf("📂", "scope: %s", rs.Workspace.SubScope)
	}

	rs.Workspace.WS = workspace.MountSpec{
		Workspace: rs.Man.Workspace, OutputDir: p.Output, EgressMode: p.Mode, Credentials: p.Credentials,
		MountRootDeps: mountRootDeps(os.Getenv),
		// Under the per-run state dir: reclaimed with the run, and by `proveo clean`
		// for anything a crash leaves behind.
		DepStage: filepath.Join(rs.EgDir, "deps"),
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

// promptChoices settles WHAT posture the run carries. A cached answer only SEEDS
// the form; with no prompt to seed, the resolver owes the operator the manifest
// default, so the cache is neither read nor written (see the note inside).
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
	// A remembered answer SEEDS the prompt; it is never an authority of its own
	// (_spec/internal/agentsettings/choice-cache.puml). With no prompt to seed —
	// no TTY, wizard off, dry run — the resolver owes the operator the manifest
	// default, so the cache is neither read nor written here. Letting it apply
	// headlessly meant a run's security posture came from whatever the last
	// interactive session happened to pick: an e2e run asking for the default
	// `--credentials broker` silently got `forward` plus a `browser` image
	// variant, and then rewrote the operator's remembered posture on its way out.
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

	rs.Backend.DindScope = rs.Workspace.WS.InputDir
	if rs.Backend.DindScope == "" {
		rs.Backend.DindScope = rs.Start
	}
	rs.Backend.WantDind = false
	rs.Backend.BrowserImage = rs.Man.Images[p.Target+"-browser"] // the -browser variant, if this harness has one
	rs.Backend.DindOfferable = rs.Man.IsDind() && dind.ModeSupported(p.Mode) && dind.CredentialsSupported(p.Credentials)
	if hasAddon(p.Addons, "browser") && rs.Backend.BrowserImage != "" {
		chosen, isLocal := posture.ResolveImageChoice(rs.Backend.BrowserImage)
		if isLocal {
			ui.Iconf("📦", "image: %s (local build — newer than the published tag)", chosen)
		}
		p.Image = chosen
		ui.Iconf("🌐", "variant: browser → %s", p.Image)
	}
	if hasAddon(p.Addons, addonDind) && rs.Backend.DindOfferable {
		rs.Backend.WantDind = true
		ui.Iconf("🐳", "sidecar: DinD (same image)")
	}
	if len(p.Addons) == 0 && !p.PrintOnly {
		rs.Backend.WantDind = rs.Backend.DindOfferable && dind.ShouldStart(rs.Man.IsDind(), rs.Backend.DindScope, false, nil)
	}
	if rs.Man.IsDind() && !dind.ModeSupported(p.Mode) && dind.EnvEnabled() && dind.ScopeHasDockerfiles(rs.Backend.DindScope) {
		ui.Warnf("PROVEO_DIND is set but --egress-mode %s cannot expose a Docker daemon to the agent without defeating egress enforcement; skipping DinD (use --egress-mode broker for in-container Docker)", p.Mode)
	}

	return nil
}

// resolveCredentials settles WHAT the agent may authenticate with: the persisted
// login, sbx's stored names, the env the manifest asks for, and the mounts that
// carry them. It reports what is missing rather than refusing — except where the
// backend provably cannot recover, which selectBackend handles.
func resolveCredentials(rs *Spec, p *Params, d Deps) error {
	var err error
	rs.Creds.FileLogin, rs.Creds.LoginNeedsRefresh = credentials.PersistedLogin(p.Target, proveohome.Root(os.Getenv))
	rs.Creds.StoreHeld = sbxStoredAuth(rs.Man, p)
	rs.Creds.LoggedIn = rs.Creds.FileLogin || len(rs.Creds.StoreHeld) > 0
	// The agent renews a stale access token itself, but its FIRST turn reports
	// "Login expired · Please run /login" while it does — which reads as a dead
	// credential to the operator, who then goes looking for an auth problem that
	// resolved itself a second later. Saying it up front costs one line.
	if rs.Creds.LoginNeedsRefresh && !p.PrintOnly {
		ui.Iconf("🔑", "the login in the proveo home needs a refresh — the agent may report "+
			"\"Login expired\" on its first turn, and can only carry on if the renewal reaches the "+
			"provider from where it runs")
	}
	// Say so when a token IS exported and is being left out. The 🔓 line below only
	// fires when auth is missing, so the case that actually misbills — a token set,
	// silently overriding the mounted login — was the one nothing reported.
	if rs.Creds.FileLogin && !p.PrintOnly && strings.TrimSpace(p.AuthVar) == "" {
		if av := credentials.EffectiveAuthVar(rs.Man, p.Target, p.AuthVar, proveohome.Root(os.Getenv)); av != "" && strings.TrimSpace(rs.Creds.Lookup(av)) != "" {
			ui.Iconf("🔓", "%s is set but not injected — the login in the proveo home is the credential, and an env token would override it", av)
		}
	}
	if missing := rs.Man.MissingEnv(rs.Creds.Lookup); len(missing) > 0 && !p.PrintOnly {
		switch {
		case rs.Man.Subscription && rs.Creds.FileLogin:
			// MissingEnv only reads env vars, so a completed login sitting in the
			// proveo home read as "no auth" and produced a warning that sent an
			// operator after a token they did not need.
			ui.Iconf("🔓", "%s: using the login persisted in the proveo home", rs.Man.Name)
		case rs.Man.Subscription && len(rs.Creds.StoreHeld) > 0:
			ui.Iconf("🔓", "%s: using %s from sbx's stored credentials — proveo can see that it is there, not what it holds",
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

	// A linked worktree needs container-correct pointer files before planning, so
	// the mounts can reference them. On failure fall through to the GIT_DIR pin.
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
	reportLinks(rs.Workspace.Links)

	rs.Creds.HomePlan, err = proveohome.Prepare(rs.Man.Home, os.Getenv)
	if err != nil {
		return err
	}
	if rs.Creds.HomePlan.Root != "" {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, rs.Creds.HomePlan.Mounts...)
		ui.Iconf("🏠", "proveo home: %s (mounted at %s)", rs.Creds.HomePlan.Root, proveohome.ContainerHome)
	}

	if m, ok := credentials.GhConfigMount(os.Getenv); ok {
		rs.Workspace.Mounts = append(rs.Workspace.Mounts, m)
		ui.Iconf("🔑", "gh session: %s mounted read-only", m.Host)
	}

	rs.Creds.Detected = credentials.FilterProviders(provider.Detect(rs.Creds.Lookup), rs.Man.Capabilities)
	rs.Creds.Brokered = credentials.BrokerProviders(p.forwards(), rs.Man, rs.Creds.Detected, rs.Creds.Lookup, brokerEnabled())
	if reason := credentials.BrokerOffReason(p.forwards(), rs.Creds.Brokered, rs.Creds.Detected, brokerEnabled()); reason != "" {
		ui.Warnf("%s", reason)
	}
	if len(rs.Creds.Brokered) > 1 {
		ui.Iconf("🔐", "broker: %d providers injected at the egress layer (%s)",
			len(rs.Creds.Brokered), strings.Join(rs.Creds.Brokered, ", "))
	}
	for _, msg := range p.Roles.MissingKeys(rs.Creds.Detected) {
		ui.Warnf("%s", msg)
	}
	// ONE value, rendered twice — see internal/posture. The rows used to be
	// assembled here and the header assembled elsewhere, which is how the two
	// could disagree about the same run.
	return nil
}

// buildPosture renders ONE value that is shown twice. The rows used to be built
// here and the header built elsewhere, which is how the two could disagree about
// the same run.
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
		// Predicted from the manifest here, settled against the real backend in
		// selectBackend; the two agree except when sbx turns out unavailable, and
		// that fallback is announced where it happens.
		Workspace: posture.Workspace(predictClone(p, p.willSandbox(rs.Man), rs.Workspace.WS)),
	}
	rs.Log.Fields("resolved posture", rs.Posture.Fields())
}

// decideClone settles whether the agent edits a private in-VM clone or the
// mounted checkout, and why not when it cannot.
//
// Clone is the DEFAULT on the sbx backend (CloneDefault). It is the only shape in
// which the checkout is never written, no host-built dependency tree crosses into
// the sandbox, and — the reason it became the default — the workspace lives on the
// VM's own disk instead of the virtiofs passthrough, whose directory entries have
// been measured vanishing under a running agent and taking every Bash call with
// them (_spec/internal/sbx/virtiofs-cwd-invalidation.puml).
//
// sbx can only clone what git can: the MAIN worktree of a repository, offered as
// the primary workspace. Where the default cannot apply the run falls back to the
// mounted checkout and says why. An EXPLICIT --clone in those shapes is an error
// rather than a fallback, because it asked for a promise that cannot be kept.
func decideClone(p *Params, sbxBackend bool, ws workspace.MountSpec) (on bool, whyOff string, err error) {
	if !p.Clone {
		return false, "", nil // --clone=false or PROVEO_CLONE=off: the mounted checkout, by choice
	}
	switch {
	case !sbxBackend:
		// --clone is creation-time and sbx-only. Accepting it silently on the docker
		// backend would hand back a run that edited the checkout after promising not
		// to, which is the one failure mode the flag exists to prevent.
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone is an sbx-backend feature and this run is on docker+egress:\n" +
				"  the agent would edit your checkout directly, which is what --clone asks it not to do.\n" +
				"  Re-run without --clone, or on a target whose manifest declares `docker: sbx`")
		}
		return false, "", nil // docker has no clone; nothing to announce
	case ws.RepoRoot == "":
		// sbx clones with git, so without a repository there is nothing to clone and
		// the failure would surface inside the sandbox rather than here.
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone needs a git repository and %s is not inside one:\n"+
				"  sbx builds the sandbox workspace by cloning the host repo over a git daemon.\n"+
				"  Run it from a checkout, or drop --clone to work on the mounted directory",
				ws.InputDir)
		}
		return false, "not a git repository — sbx clones with git", nil
	case workspace.LinkedWorktree(ws.InputDir):
		// sbx documents clone mode for the main worktree only.
		if p.CloneSet {
			return false, "", fmt.Errorf("--clone cannot clone a linked git worktree (%s):\n"+
				"  sbx clones the MAIN worktree only. Run from the main checkout, or drop --clone",
				ws.InputDir)
		}
		return false, "linked git worktree — sbx clones only the main worktree", nil
	case ws.ScopeRel() != "":
		// The primary workspace is the sub-scope, and sbx clones the primary. An
		// explicit ask is honoured and left to sbx; the default stays conservative.
		if p.CloneSet {
			return true, "", nil
		}
		return false, "monorepo sub-scope — the primary workspace has to be the repository root", nil
	}
	return true, "", nil
}

// predictClone is decideClone for the posture row, before the backend is
// settled: errors are the flag's business later, not the posture's.
func predictClone(p *Params, sbxBackend bool, ws workspace.MountSpec) bool {
	on, _, err := decideClone(p, sbxBackend, ws)
	return err == nil && on
}

// assembleEnv turns the resolved credentials into the container's environment:
// real values when forwarding, sentinels when brokering, plus the git identity,
// the home plan and the scope pointers.
//
// The plan folds this into resolveCredentials. It is separate here because posture
// is rendered BETWEEN the two, and moving either across that line changes the order
// the operator reads them in — which the resolve golden pins.
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
				credentials.HydrateProcessEnv(e.Name, rs.Creds.Lookup)
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
	rs.Creds.Env = append(rs.Creds.Env, gitidentity.Resolve(os.Getenv, nil).EnvPairs()...)
	rs.Creds.Env = append(rs.Creds.Env, rs.Creds.HomePlan.Env...)
	if rel := rs.Workspace.WS.ScopeRel(); rel != "" {
		rs.Creds.Env = append(rs.Creds.Env, "PROVEO_SCOPE_REL="+rel)
	}
	// Only when the pointer overlay is unavailable: a coherent .git chain needs no
	// pin, and GIT_DIR would also capture any nested repo the agent visits.
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

// selectBackend decides between sbx and docker+egress and, when it picks sbx, runs
// it: the sbx path has no sidecars to assemble, so there is nothing left to do
// after the decision. It returns done=true when it has handled the run.
func selectBackend(rs *Spec, p *Params, d Deps) (bool, error) {
	rs.Backend.Sbx = false
	if rs.Man.IsSbx() && p.Mode != "review" && sandbox.Enabled() {
		switch ok, why := sandbox.Ready(p.PrintOnly, d.ProvisionConfirm); {
		case !p.sandboxAddonOn():
			ui.Iconf("🐳", "docker sandbox: off (add-on unchecked) — running on docker+egress")
		case !ok:
			sandbox.ReportUnavailable(why)
		default:
			rs.Backend.Sbx = true
			ui.Iconf("📦", "backend: docker sandboxes (sbx)")
			sandbox.WarnBaseline()
			if hasAddon(p.Addons, addonChrome) {
				// The picker greys this pair; a cached or scripted answer can still
				// carry both, so the run says which one it is not honouring.
				ui.Warnf("%s: skipped — a sandbox VM cannot reach the host's Claude in Chrome socket; untick docker (sandbox) to use it", addonChrome)
			}
		}
	}
	// Both backends reach here. This used to live in execute(), which is the docker
	// path only — so the backend that actually READS the .env was the one that said
	// nothing, while the backend that masks it warned.
	credentials.WarnMountedSecrets(rs.Workspace.WS.InputDir, p.Mode, rs.Backend.Sbx, rs.Creds.Lookup)
	// A `docker: sbx` harness is never offered the dind sidecar (addonOptions:
	// one entry, never two) — and it does not need one. sbx gives each sandbox its
	// OWN daemon, gated on the image label `com.docker.sandboxes.start-docker`,
	// which proveo's sbx-capable images now carry. Measured inside a sandbox on
	// proveo/claudecode: `docker version` reports Server 29.7.2 and `docker run
	// hello-world` succeeds. Nothing to warn about any more; the warning that used
	// to live here told the operator docker would fail, which is now false.
	var err error
	rs.Backend.Clone, rs.Backend.CloneOff, err = decideClone(p, rs.Backend.Sbx, rs.Workspace.WS)
	if err != nil {
		return false, err
	}
	if rs.Backend.Sbx {
		switch {
		case rs.Backend.Clone:
			ui.Iconf("\U0001f5c2", "workspace: private clone — your checkout is NOT written. "+
				"The agent's commits are fetched back at teardown under refs/proveo/%s/ (`--clone=false` edits the checkout directly)", rs.Sid)
		case rs.Backend.CloneOff != "":
			ui.Notef("workspace: mounted checkout — clone default does not apply here: %s", rs.Backend.CloneOff)
		}
	}
	// The posture row was predicted from the manifest; the backend is now known.
	rs.Posture.Workspace = posture.Workspace(rs.Backend.Clone)
	if rs.Backend.Sbx {
		// sbx mounts every path at its own HOST path and cannot say "this
		// directory, at that container path" — so the private dependency-tree
		// copies the plan named have no expression there. Passed through, each
		// would become an unrelated extra workspace while the host tree still rode
		// in inside the mirrored checkout. Drop them, and say what that means: on
		// this backend only --clone keeps host-built trees out of the sandbox.
		mounts, dropped := workspace.StripDepCopies(rs.Workspace.Mounts, rs.Workspace.WS.DepStage)
		if dropped > 0 && !rs.Backend.Clone {
			ui.Warnf("sbx mirrors the checkout, so its %d dependency tree(s) (node_modules, target, .venv …) cross into the sandbox as the host built them;\n"+
				"  the seed rebuilds a foreign tree only with PROVEO_DEPS=reinstall (it rewrites your checkout) — `--clone` keeps untracked trees out and installs fresh", dropped)
		}
		in := sandbox.Input{
			Target: p.Target, Image: p.Image, AuthVar: p.AuthVar,
			Shell: p.Shell, Clone: rs.Backend.Clone, Extra: p.Extra,
			RepoRoot: rs.Workspace.WS.RepoRoot,
			Roles:    p.Roles, Bridges: p.Bridges,
			Evidence:       p.evidenceOrDefault(),
			Forwards:       p.forwards(),
			SandboxAddonOn: p.sandboxAddonOn(),
			Man:            rs.Man, Sid: rs.Sid, EgDir: rs.EgDir,
			Mounts: mounts, Workdir: rs.Workspace.Workdir,
			Lookup:           rs.Creds.Lookup,
			Detected:         rs.Creds.Detected,
			GitEnv:           gitidentity.Resolve(os.Getenv, nil).EnvPairs(),
			HomeEnv:          rs.Creds.HomePlan.Env,
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
			// --kit is not decoration: it carries the whole posture — network
			// allowlist, brokered credential declarations, entrypoint — and sbx
			// refuses a run without it. Printing a command whose --kit path was
			// never written produces a failure that reads as an sbx bug rather
			// than a print-mode limitation, so print mode renders the spec even
			// though it executes nothing else. No secret VALUE reaches the file:
			// The Kit carries no credential at all: it is a MIXIN, and sbx rejects
			// a mixin that redeclares a service its built-in agent owns.
			if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
				return false, fmt.Errorf("write sandbox kit: %w", err)
			}
			ui.Iconf("📄", "sandbox kit: %s (removed by `proveo clean`)", filepath.Join(cfg.KitDir, "spec.yaml"))
			if len(secrets) > 0 {
				names := make([]string, 0, len(secrets))
				for _, kv := range secrets {
					names = append(names, kv[0])
				}
				// Print mode must not mutate the operator's secret store, so the
				// printed command is runnable only once these exist in it.
				ui.Warnf("printed command needs these in sbx's secret store first: %s (`sbx secret set NAME`)", strings.Join(names, ", "))
			}
			fmt.Printf("# agent\nsbx %s\n", strings.Join(sbx.RunArgs(cfg), " "))
			return true, nil
		}
		// A STALE LOGIN FILE is not the same condition as MISSING ENV, and gating
		// it behind authMissingAtStart hid it completely: an operator with
		// CLAUDE_CODE_OAUTH_TOKEN exported has nothing "missing", so the check
		// never ran and the run launched into a credential that could not renew.
		//
		// The renewal is precisely what this backend cannot do. Launching anyway
		// spends a minute of image load to reach "Failed to authenticate: OAuth
		// session expired and could not be refreshed", and the sandbox stops with
		// the agent — which reads as an infrastructure failure from outside,
		// because that is exactly what it looks like.
		// NOTE: there is deliberately no stale-login refusal here any more.
		//
		// It was added when the mounted proveo home WAS this backend's credential, so
		// a login that could not renew meant a run that could not authenticate. That
		// stopped being true when the HOME redirect went (see sandbox.Home): sbx runs its
		// own agent user and its credential proxy writes the live credential into
		// that user's home, so the file under the proveo home is not consulted and
		// its freshness decides nothing. Refusing on it would block runs that work —
		// verified by e2e/ladder_test.go, whose rung 3 carries this exact Kit.
		if len(rs.Creds.AuthMissingAtStart) > 0 {
			// On this backend the agent cannot complete a login: it reaches the
			// prompt, exits, and the sandbox stops with it — which surfaces 30s
			// later as an unrelated 137. Refusing costs nothing; launching costs a
			// minute of image load to reach a failure that was knowable up front.
			// Gated on the persisted login too, because MissingEnv alone would
			// refuse runs whose credentials are already in the proveo home.
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

// execute is the docker+egress path: preflight, the review gate, the sidecar plan,
// and the agent itself. Teardown is by defer, so a signal mid-run unwinds the same
// way a normal exit does.
func execute(rs *Spec, p *Params, d Deps) error {
	var dindSidecar *dind.Sidecar

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

	// Claude in Chrome through the operator's browser: a relay held open on the
	// host for the life of the run, named to the agent by environment. Skipped —
	// and said so — rather than failed: the add-on is a convenience over the run,
	// and the run must not die because Chrome was closed after the prompt.
	var bridge *chromebridge.Relay
	if hasAddon(p.Addons, addonChrome) {
		switch {
		case p.PrintOnly:
			ui.Iconf("🧭", "%s: the run starts a host relay and sets %s + %s on the agent (not started in print mode)", addonChrome, chromebridge.EnvAddr, chromebridge.EnvToken)
		case !dind.ModeSupported(p.Mode) || !dind.CredentialsSupported(p.credentialsOrDefault()):
			ui.Warnf("%s: skipped — needs --egress-mode open --credentials forward (the agent has no route to the host behind a sidecar)", addonChrome)
		default:
			if why := chromeUnavailable(rs.Creds.Lookup); why != "" {
				ui.Warnf("%s: skipped — %s", addonChrome, why)
				break
			}
			r, err := chromebridge.Start(chromebridge.BindAddr(), chromebridge.HostSocketDir(), ui.Warnf)
			if err != nil {
				ui.Warnf("%s: skipped — %v", addonChrome, err)
				break
			}
			bridge = r
			defer func() { _ = bridge.Close() }()
			if err := bridge.SetTokenEnv(); err != nil {
				ui.Warnf("%s: cannot export %s: %v", addonChrome, chromebridge.EnvToken, err)
			}
			rs.Creds.Env = append(rs.Creds.Env, bridge.Env()...)
			ui.Iconf("🧭", "chrome: Claude in Chrome through YOUR browser — host relay %s → %s. The agent gets your logged-in sessions; site permissions stay the extension's", bridge.Addr(), chromebridge.HostSocketDir())
		}
	}

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
		ProviderDomains: credentials.JoinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), rs.Man.Capabilities.Hosts),
		SquidImage:      os.Getenv("PROVEO_SQUID_PROXY_IMAGE"),
		ProxyImage:      os.Getenv("PROVEO_EGRESS_PROXY_IMAGE"),
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
	if rs.Backend.WantDind {
		sc, err := dind.Start(dind.ExecRunner{}, p.Target, rs.Backend.DindScope, os.Stderr)
		if err != nil {
			return err
		}
		dindSidecar = sc
		agent.ExtraArgs = append(append([]string(nil), agent.ExtraArgs...), sc.EnvArgs()...)
		if plan.AgentNetwork == "" {
			agent.ExtraArgs = append(agent.ExtraArgs, sc.LinkArgs()...)
		}
	}
	if len(rs.Creds.AuthMissingAtStart) > 0 {
		credentials.PrintSubscriptionAuthHints(rs.Man, rs.Creds.AuthMissingAtStart, os.Stderr)
	}
	runErr := func() error {
		if !dockeregress.NeedsLifecycle(plan) {
			if dindSidecar == nil {
				return dockeregress.ExecAgentWithProxy(agent, reviewProxy)
			}
			var once sync.Once
			cleanup := func() { once.Do(func() { dindSidecar.Cleanup(dind.ExecRunner{}) }) }
			defer cleanup()
			stopSig := dockeregress.OnSignalCleanup(cleanup)
			defer stopSig()
			return dockeregress.ExecAgentWithProxy(agent, reviewProxy)
		}
		squidProviders := rs.Creds.Detected
		if strings.TrimSpace(rs.Man.Provider) != "" && len(rs.Creds.Brokered) == 1 {
			squidProviders = rs.Creds.Brokered
		}
		return dockeregress.Exec(rs.SquidConfig, plan, agent, rs.EgDir, squidProviders, dindSidecar, reviewProxy)
	}()
	return runErr
}
