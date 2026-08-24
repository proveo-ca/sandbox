// Command proveo is the harness CLI.
// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/egress/teardown-and-signals.puml, _spec/_paradigms/egress-boundary.puml, _spec/internal/egress/egress-tiers.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/_conventions/design-decision-ids.puml, _spec/_paradigms/credential-boundary.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/provider/model-resolution.puml, _spec/internal/dind/dind-sidecar.puml, _spec/internal/runner/hardened-run-argv.puml, _spec/internal/workspace/mount-model.puml, _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/runlog/run-transcript.puml, _spec/internal/manifest/harness-manifest-schema.puml, _spec/_paradigms/git-identity.puml, _spec/internal/proveohome/proveo-home-components.puml, _spec/_plans/ci-pipeline.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/dind"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/engine"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/reviewgate"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

var version = "dev"

func loadManifests() ([]manifest.Manifest, error) {
	if dir := os.Getenv("PROVEO_DEFS_DIR"); dir != "" {
		return manifest.Load(dir)
	}
	return manifest.LoadFS(proveo.Manifests)
}

// loadTargets resolves the target->image map across all manifests.
func loadTargets() (map[string]string, error) {
	ms, err := loadManifests()
	if err != nil {
		return nil, err
	}
	return manifest.Targets(ms)
}

// manifestForTarget returns the manifest that owns the given runnable target.
func manifestForTarget(target string) (manifest.Manifest, error) {
	ms, err := loadManifests()
	if err != nil {
		return manifest.Manifest{}, err
	}
	for _, m := range ms {
		if _, ok := m.Images[target]; ok {
			return m, nil
		}
	}
	return manifest.Manifest{}, fmt.Errorf("no manifest for target %q", target)
}

func main() {
	var flagLS, flagInit bool
	root := &cobra.Command{
		Use:           "proveo",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		Args:          cobra.NoArgs,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagLS && flagInit {
				return fmt.Errorf("flags --ls and --init are mutually exclusive")
			}
			if flagLS {
				return doList()
			}
			if flagInit {
				return doInit()
			}
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{printf \"%s version %s\\n\" .Name .Version}}")
	root.Flags().BoolVar(&flagLS, "ls", false, "List available harness targets")
	root.Flags().BoolVar(&flagInit, "init", false, "Create a project .env from provider API keys already in the environment")
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		// Branding banner on root help only (proveo help / proveo --help).
		if !cmd.HasParent() {
			ui.WriteBrandBanner(cmd.OutOrStdout())
		}
		defaultHelp(cmd, args)
	})
	root.AddCommand(versionCmd(), lsCmd(), runCmd(), projectsCmd(), setupCmd(), initCmd(),
		updateCmd(), uninstallCmd(), cleanCmd(), targetsCmd(), buildCmd(), deployCmd(), testCmd())
	if err := root.Execute(); err != nil {
		var ae agentExitError
		if errors.As(err, &ae) {
			os.Exit(ae.code)
		}
		ui.Failf("%v", err)
		os.Exit(1)
	}
}

// agentExitError carries the agent container's own non-zero exit code.
type agentExitError struct{ code int }

func (e agentExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.code) }

// versionCmd keeps `proveo version` as an alias for `proveo --version`.
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the proveo version (alias for --version)",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { fmt.Printf("proveo version %s\n", version) },
	}
}

// lsCmd keeps `proveo ls` as an alias for `proveo --ls`.
func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List available harness targets (alias for --ls)",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return doList() },
	}
}

func doList() error {
	targets, err := loadTargets()
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(targets) {
		fmt.Printf("%-16s %s\n", name, targets[name])
	}
	return nil
}

func runCmd() *cobra.Command {
	var egressMode, credentials, localModel, input, output, scope, dataDir, imageOverride, resumeID string
	var printOnly, shellMode, contSession, listSessions bool
	cmd := &cobra.Command{
		Use:   "run <target> [-- args...]",
		Short: "Run a harness against the current repo",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			targets, err := loadTargets()
			if err != nil {
				return err
			}
			image, ok := targets[target]
			if !ok {
				return fmt.Errorf("unknown target %q (see `proveo ls`)", target)
			}
			if imageOverride != "" {
				image = imageOverride
			}
			modeSet := cmd.Flags().Changed("egress-mode")
			credsSet := cmd.Flags().Changed("credentials")
			if !egress.ValidMode(egressMode) {
				return fmt.Errorf("invalid --egress-mode %q (%s)", egressMode, strings.Join(egress.Modes(), "|"))
			}
			if canonical, aliased := egress.Canonical(egressMode); aliased {
				ui.Warnf("--egress-mode %q now means %q: modes name the NETWORK tier only, and credential "+
					"handling moved to --credentials (default broker — the key stays in the egress layer). "+
					"If you relied on %q handing the real key to the container, add --credentials forward.",
					egressMode, canonical, egressMode)
				egressMode = canonical
			}
			if !egress.ValidCredentials(credentials) {
				return fmt.Errorf("invalid --credentials %q (%s)", credentials, strings.Join(egress.CredentialModes(), "|"))
			}
			resumeArgs, err := proveohome.ResumeArgs(target, resumeID, contSession, listSessions)
			if err != nil {
				return err
			}
			extra := args[1:]
			if len(resumeArgs) > 0 {
				extra = append(append([]string{}, resumeArgs...), extra...)
			}
			return doRun(runParams{
				target: target, image: image, mode: egressMode, credentials: credentials,
				modeSet: modeSet, credsSet: credsSet, localModel: localModel,
				input: input, output: output, scope: scope, dataDir: dataDir,
				shell: shellMode, printOnly: printOnly, extra: extra,
			})
		},
	}
	cmd.Flags().StringVar(&egressMode, "egress-mode", "allowlist", strings.Join(egress.Modes(), "|")+
		" — network tier, cumulative (default allowlist: open adds no allowlist, review adds connection prompts)")
	cmd.Flags().StringVar(&credentials, "credentials", "broker",
		strings.Join(egress.CredentialModes(), "|")+" (default broker: the key stays in the egress layer and is injected at the proxy)")
	cmd.Flags().StringVar(&localModel, "local-model", "", "Ollama model to serve locally")
	cmd.Flags().StringVar(&input, "input", "", "input dir to mount read-only (default: cwd)")
	cmd.Flags().StringVar(&output, "output", "", "output dir to mount read-write (default: <input>/reports)")
	cmd.Flags().StringVar(&scope, "scope", "", "monorepo sub-project to open (repo-relative; omit for an interactive picker)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "extra directory to mount read-only at /workspace/data")
	cmd.Flags().StringVar(&imageOverride, "image", "", "override the image for the target")
	cmd.Flags().StringVar(&resumeID, "resume", "", "resume a prior agent session by id (harness-specific)")
	cmd.Flags().BoolVar(&contSession, "continue", false, "continue the most recent session for this workspace")
	cmd.Flags().BoolVar(&listSessions, "ls", false, "list resumable sessions (cursor/claude) and exit into the tool picker")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "open a shell in the container instead of the agent")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the docker plan instead of executing")
	return cmd
}

type runParams struct {
	target, image, mode, credentials, localModel, input, output, scope, dataDir string
	modeSet, credsSet                                                           bool
	addons                                                                      []string
	addonsAnswered                                                              bool // a cached or prompted answer exists; default-on add-ons stop defaulting
	roles                                                                       provider.Roles
	authVar                                                                     string
	evidence                                                                    string
	shell, printOnly                                                            bool
	extra                                                                       []string
}

func (p runParams) forwards() bool { return p.credentials == "forward" }

func (p runParams) credentialsOrDefault() string {
	if p.credentials == "" {
		return "broker"
	}
	return p.credentials
}

func (p runParams) intercepts() bool { return p.mode != "open" || !p.forwards() }

// Agent evidence: how much of its own work the harness narrates.
const (
	evidenceLabel   = "agent evidence"
	evidenceVar     = "PROVEO_AGENT_EVIDENCE"
	evidenceDefault = "default"
	evidenceVerbose = "verbose"
)

func (p runParams) evidenceOrDefault() string {
	if p.evidence == evidenceDefault {
		return evidenceDefault
	}
	return evidenceVerbose
}

func doRun(p runParams) error {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	sid := fmt.Sprintf("proveo-%d-%d", time.Now().Unix(), os.Getpid())
	egDir := filepath.Join(stateDir(), "egress", sid)

	rl, logErr := runlog.Open(sid)
	if logErr != nil {
		ui.Warnf("run log unavailable: %v", logErr)
	} else {
		ui.TeeTo(rl.Writer())
		defer rl.Close()
		rl.Artifacts(egDir)
		ui.Iconf("📝", "run log: %s", rl.Path())
	}

	man, err := manifestForTarget(p.target)
	if err != nil {
		return err
	}

	if p.target == "cursor" && p.localModel != "" {
		return fmt.Errorf("cursor has no --local-model path (inference is vendor-pinned); unset it or use another harness")
	}

	start := orWD(p.input)
	ws := workspace.Resolve(start)
	repoRoot := start
	if ws.IsRepo {
		repoRoot = ws.Root
	}
	if p.output == "" {
		p.output = filepath.Join(repoRoot, "reports")
	}

	subScope := strings.Trim(p.scope, "/")
	if subScope == "" && !p.printOnly && isStdinTTY() && wizardEnabled() && ws.IsRepo {
		if projs := workspace.DiscoverProjects(repoRoot); len(projs) > 0 {
			subScope = pickProject(projs, os.Stdin, os.Stderr)
		}
	}
	if subScope != "" {
		ui.Iconf("📂", "scope: %s", subScope)
	}

	wsSpec := workspace.MountSpec{
		Workspace: man.Workspace, OutputDir: p.output, EgressMode: p.mode, Credentials: p.credentials,
		MountRootDeps: mountRootDeps(os.Getenv),
	}
	var workdir string
	if wsSpec.Layout == "input-output" {
		wsSpec.InputDir = repoRoot // whole repo mounted read-only
		if subScope != "" {
			workdir = "/workspace/input/" + subScope
		}
	} else { // app layout: the scope dir drives the /app mount path
		if subScope != "" {
			wsSpec.InputDir = filepath.Join(repoRoot, subScope)
		} else {
			wsSpec.InputDir = start
		}
		if ws.IsRepo {
			wsSpec.RepoRoot = repoRoot
		}
	}

	invocationWD, _ := os.Getwd()
	hostEnvFile := strings.TrimSpace(os.Getenv("PROVEO_EGRESS_ENV_FILE"))
	if hostEnvFile == "" {
		hostEnvFile = workspace.EnvFileSource(invocationWD, wsSpec.InputDir, wsSpec.RepoRoot)
	}
	lookup := providerLookup(hostEnvFile)

	evidenceSet := false
	if v := strings.ToLower(strings.TrimSpace(lookup(evidenceVar))); v != "" {
		if v != evidenceDefault && v != evidenceVerbose {
			ui.Warnf("%s=%q is not %s|%s — using %s", evidenceVar, v, evidenceDefault, evidenceVerbose, evidenceVerbose)
			v = evidenceVerbose
		}
		p.evidence, evidenceSet = v, true
	}

	settingsRoot := proveohome.Root(os.Getenv)
	settings, err := agentsettings.Load(settingsRoot)
	if err != nil {
		ui.Warnf("%v — continuing without cached settings", err)
	}
	promptable := cacheApplies(p.printOnly, isStdinTTY())
	if err := p.applyCapabilities(man.Capabilities); err != nil {
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
	if promptable {
		if cached, ok := settings.Lookup(p.target, man.Capabilities); ok {
			p.seedFromCache(cached, lookup, evidenceSet)
		}
	}
	if p.roles == nil {
		p.roles = provider.RolesFrom(lookup)
	}
	if promptable {
		if err := p.promptChoices(man, lookup, gitRootOrEmpty(ws, repoRoot), settingsRoot); err != nil {
			return err
		}
	}
	if err := p.applyCapabilities(man.Capabilities); err != nil {
		return err
	}
	if promptable {
		settings.Remember(p.target, man.Capabilities, agentsettings.Choice{
			Egress: p.mode, Credentials: p.credentialsOrDefault(), Addons: p.addons, AuthVar: p.authVar,
			Evidence: p.evidenceOrDefault(), Models: p.roles.Canonical(),
		})
		if err := settings.Save(settingsRoot); err != nil {
			ui.Warnf("%v", err)
		}
	}
	wsSpec.EgressMode, wsSpec.Credentials = p.mode, p.credentials

	if p.mode == "review" {
		if ok, why := reviewSupported(os.Getenv); !ok {
			ui.Warnf("--egress-mode review is %s: the consent gate cannot be reached from the "+
				"inspector on this host, so every new connection will be DENIED without a prompt", why)
		}
	}
	if p.target == "cursor" && p.intercepts() {
		ui.Warnf("cursor + --egress-mode %s --credentials %s: cursor-agent pins its TLS, so any intercepting tier "+
			"breaks it (it reports \"invalid API key\") — use --egress-mode open --credentials forward",
			p.mode, p.credentialsOrDefault())
	}

	dindScope := wsSpec.InputDir
	if dindScope == "" {
		dindScope = start
	}
	wantDind := false
	browserImage := man.Images[p.target+"-browser"] // the -browser variant, if this harness has one
	dindOfferable := man.IsDind() && dind.ModeSupported(p.mode) && dind.CredentialsSupported(p.credentials)
	if hasAddon(p.addons, "browser") && browserImage != "" {
		p.image = browserImage
		ui.Iconf("🌐", "variant: browser → %s", browserImage)
	}
	if hasAddon(p.addons, addonDind) && dindOfferable {
		wantDind = true
		ui.Iconf("🐳", "sidecar: DinD (same image)")
	}
	if len(p.addons) == 0 && !p.printOnly {
		wantDind = dindOfferable && dind.ShouldStart(man.IsDind(), dindScope, false, nil)
	}
	if man.IsDind() && !dind.ModeSupported(p.mode) && dind.EnvEnabled() && dind.ScopeHasDockerfiles(dindScope) {
		ui.Warnf("PROVEO_DIND is set but --egress-mode %s cannot expose a Docker daemon to the agent without defeating egress enforcement; skipping DinD (use --egress-mode broker for in-container Docker)", p.mode)
	}

	var authMissingAtStart []manifest.EnvVar
	if missing := man.MissingEnv(lookup); len(missing) > 0 && !p.printOnly {
		if man.Subscription {
			authMissingAtStart = append([]manifest.EnvVar(nil), missing...)
			ui.Warnf("no auth present for subscription agent %s — running anyway; the agent will handle login", man.Name)
		} else if isStdinTTY() && wizardEnabled() {
			for name, v := range promptEnv(p.target, missing, os.Stdin, os.Stderr, termSecret) {
				_ = os.Setenv(name, v)
			}
			missing = man.MissingEnv(lookup)
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
	worktreeLinks, err := wsSpec.PrepareWorktreeLinks(proveohome.Root(os.Getenv))
	if err != nil {
		ui.Warnf("git worktree: %v; falling back to GIT_DIR pinning", err)
	}
	wsSpec.WorktreeLinkDir = worktreeLinks

	mounts, planWorkdir, links := wsSpec.Plan()
	if planWorkdir != "" {
		workdir = planWorkdir
	}
	reportLinks(links)

	homePlan, err := proveohome.Prepare(man.Home, os.Getenv)
	if err != nil {
		return err
	}
	if homePlan.Root != "" {
		mounts = append(mounts, homePlan.Mounts...)
		ui.Iconf("🏠", "proveo home: %s (mounted at %s)", homePlan.Root, proveohome.ContainerHome)
	}

	if m, ok := ghConfigMount(os.Getenv); ok {
		mounts = append(mounts, m)
		ui.Iconf("🔑", "gh session: %s mounted read-only", m.Host)
	}

	detected := filterProviders(provider.Detect(lookup), man.Capabilities)
	brokered := brokerProviders(p.forwards(), man, detected, lookup, brokerEnabled())
	if reason := brokerOffReason(p.forwards(), brokered, detected, brokerEnabled()); reason != "" {
		ui.Warnf("%s", reason)
	}
	if len(brokered) > 1 {
		ui.Iconf("🔐", "broker: %d providers injected at the egress layer (%s)",
			len(brokered), strings.Join(brokered, ", "))
	}
	for _, msg := range p.roles.MissingKeys(detected) {
		ui.Warnf("%s", msg)
	}
	rl.Fields("resolved posture", map[string]string{
		"target":          p.target,
		"egress tier":     p.mode,
		"credentials":     p.credentialsOrDefault(),
		"add-ons":         strings.Join(p.addons, ","),
		"agent evidence":  p.evidenceOrDefault(),
		"detected keys":   strings.Join(detected, ","),
		"brokered":        strings.Join(brokered, ","),
		"reachable hosts": strings.Join(reachableHosts(detected), ","),
		"harness hosts":   strings.Join(man.Capabilities.Hosts, ","),
		"auth var":        p.authVar,
		"local model":     p.localModel,
		"observability":   observability(p.mode, p.credentialsOrDefault()),
		"model roles":     rolesLine(p.roles),
		"role providers":  strings.Join(p.roles.Providers(), ","),
	})

	var brokerFile string
	if len(brokered) > 0 {
		if p.printOnly {
			brokerFile = filepath.Join(egDir, "inject", "broker.env") // path only in dry-run
		} else if f, err := writeBrokerEnv(filepath.Join(egDir, "inject"), lookup); err == nil {
			brokerFile = f
		} else {
			ui.Warnf("broker secret file: %v", err)
		}
	}

	var modelsDir string
	var hostOllama, ollamaGPU bool
	if p.localModel != "" {
		modelsDir = ollamaModelsDir()
		hostOllama = preferHostOllama()
		ollamaGPU = sidecarOllamaGPU()
	}

	var env []string
	var brokerKeyNames []string
	for _, e := range man.Env {
		if strings.TrimSpace(lookup(e.Name)) == "" {
			continue
		}
		if e.Secret {
			if p.forwards() {
				env = append(env, e.Name)
				hydrateProcessEnv(e.Name, lookup)
			} else {
				env = append(env, e.Name+"="+entrypoint.DefaultSentinel)
				brokerKeyNames = append(brokerKeyNames, e.Name)
			}
			continue
		}
		env = append(env, e.Name)
	}
	if !p.forwards() {
		for _, k := range provider.KeyVars() {
			if strings.TrimSpace(lookup(k)) == "" {
				continue
			}
			already := false
			for _, n := range brokerKeyNames {
				if n == k {
					already = true
					break
				}
			}
			if !already {
				env = append(env, k+"="+entrypoint.DefaultSentinel)
				brokerKeyNames = append(brokerKeyNames, k)
			}
		}
		if len(brokerKeyNames) > 0 {
			env = append(env, "PROVEO_CREDENTIAL_BROKER_KEYS="+strings.Join(brokerKeyNames, ","))
		}
	}
	for _, k := range configVarsFor(man) {
		if v := strings.TrimSpace(lookup(k)); v != "" {
			env = append(env, k+"="+v)
			warnUnknownModel(k, v, p.localModel)
		}
	}
	env = append(env, evidenceVar+"="+p.evidenceOrDefault())
	env = append(env, gitidentity.Resolve(os.Getenv, nil).EnvPairs()...)
	env = append(env, homePlan.Env...)
	if rel := wsSpec.ScopeRel(); rel != "" {
		env = append(env, "PROVEO_SCOPE_REL="+rel)
	}
	// Only when the pointer overlay is unavailable: a coherent .git chain needs no
	// pin, and GIT_DIR would also capture any nested repo the agent visits.
	if wsSpec.WorktreeLinkDir == "" {
		env = append(env, wsSpec.WorktreeEnv()...)
	}

	if !p.printOnly {
		if k := resolveGitHubTokenEnv(hostGhAuth(), isStdinTTY() && wizardEnabled(), os.Stdin, os.Stderr); k != "" {
			env = append(env, k)
		}
	}

	sbxBackend := false
	if man.IsSbx() && p.mode != "review" {
		switch ok, why := sbxReady(p.printOnly); {
		case !p.sandboxAddonOn():
			ui.Iconf("🐳", "docker sandbox: off (add-on unchecked) — running on docker+egress")
		case !ok:
			reportSbxUnavailable(why)
		default:
			sbxBackend = true
			ui.Iconf("📦", "backend: docker sandboxes (sbx)")
		}
	}
	if sbxBackend {
		in := runSandboxInput{
			params: p, man: man, sid: sid, egDir: egDir,
			mounts: mounts, workdir: workdir,
			lookup:           lookup,
			detected:         detected,
			gitEnv:           gitidentity.Resolve(os.Getenv, nil).EnvPairs(),
			homeEnv:          homePlan.Env,
			scopeRel:         wsSpec.ScopeRel(),
			worktreeFallback: wsSpec.WorktreeLinkDir == "",
			worktreeEnv:      wsSpec.WorktreeEnv(),
			dataDir:          p.dataDir,
		}
		if p.printOnly {
			cfg, _, _ := sandboxSpec(in)
			fmt.Printf("# agent\nsbx %s\n", strings.Join(sbx.RunArgs(cfg), " "))
			return nil
		}
		if len(authMissingAtStart) > 0 {
			printSubscriptionAuthHints(man, authMissingAtStart, os.Stderr)
		}
		return runSandbox(in)
	}

	var dindSidecar *dind.Sidecar

	if !p.printOnly {
		if err := preflightImages(egress.Plan{}, man, p.image); err != nil {
			return err
		}
	}
	host := runner.DetectHost(p.image)
	browser := runner.IsBrowserImage(p.image)
	ov, ovSet := runner.ParsePidsOverride(os.Getenv("PROVEO_PIDS_LIMIT"))
	if err := runner.EnsurePidsCapability(host, browser, ov, ovSet); err != nil {
		return err
	}
	pidsLimit := runner.ResolvePidsLimit(host, browser, ov, ovSet)

	reviewGate, reviewProxy, stopReview := startReviewGate(p.mode, egDir)
	defer stopReview()
	reviewSocket := ""
	if reviewGate != nil {
		reviewSocket = reviewgate.Path(filepath.Join(egDir, "review"))
	}

	plan, agent, err := assemble(assembleInput{
		params: p, sid: sid, egDir: egDir, uid: uid, gid: gid,
		reviewSocket:  reviewSocket,
		writeHosts:    reachableHosts(detected),
		providerHosts: policyProviderHosts(detected, man.Capabilities),
		modelsDir:     modelsDir, providers: brokered, brokerFile: brokerFile,
		hostOllama: hostOllama, ollamaGPU: ollamaGPU,
		mounts: mounts, workdir: workdir, env: env,
		providerDomains: joinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), man.Capabilities.Hosts),
		squidImage:      os.Getenv("PROVEO_SQUID_PROXY_IMAGE"),
		proxyImage:      os.Getenv("PROVEO_EGRESS_PROXY_IMAGE"),
		ollamaImage:     os.Getenv("PROVEO_OLLAMA_IMAGE"),
		pidsLimit:       pidsLimit,
	})
	if err != nil {
		return err
	}

	if p.printOnly {
		fmt.Print(plan.Render())
		fmt.Printf("# agent\ndocker %s\n", strings.Join(runner.DockerRunArgs(agent), " "))
		return nil
	}
	if err := preflightImages(plan, man, p.image); err != nil {
		return err
	}
	if wantDind {
		sc, err := dind.Start(dind.ExecRunner{}, p.target, dindScope, os.Stderr)
		if err != nil {
			return err
		}
		dindSidecar = sc
		agent.ExtraArgs = append(append([]string(nil), agent.ExtraArgs...), sc.EnvArgs()...)
		if plan.AgentNetwork == "" {
			agent.ExtraArgs = append(agent.ExtraArgs, sc.LinkArgs()...)
		}
	}
	warnMountedSecrets(wsSpec.InputDir, p.mode, lookup)
	if len(authMissingAtStart) > 0 {
		printSubscriptionAuthHints(man, authMissingAtStart, os.Stderr)
	}
	runErr := func() error {
		if !needsLifecycle(plan) {
			if dindSidecar == nil {
				return execAgentWithProxy(agent, reviewProxy)
			}
			var once sync.Once
			cleanup := func() { once.Do(func() { dindSidecar.Cleanup(dind.ExecRunner{}) }) }
			defer cleanup()
			stopSig := onSignalCleanup(cleanup)
			defer stopSig()
			return execAgentWithProxy(agent, reviewProxy)
		}
		squidProviders := detected
		if strings.TrimSpace(man.Provider) != "" && len(brokered) == 1 {
			squidProviders = brokered
		}
		return execWithEgress(plan, agent, egDir, squidProviders, dindSidecar, reviewProxy)
	}()
	return runErr
}

func (p *runParams) applyCapabilities(c manifest.Capabilities) error {
	if !c.AllowsEgress(p.mode) {
		if p.modeSet {
			return fmt.Errorf("%s does not support --egress-mode %s (allowed: %s)",
				p.target, p.mode, strings.Join(c.Egress, "|"))
		}
		p.mode = c.Egress[0]
	}
	if !c.AllowsCredentials(p.credentialsOrDefault()) {
		if p.credsSet {
			return fmt.Errorf("%s does not support --credentials %s (allowed: %s)",
				p.target, p.credentialsOrDefault(), strings.Join(c.Credentials, "|"))
		}
		p.credentials = c.Credentials[0]
	}
	return nil
}

func joinDomains(env string, hosts []string) string {
	parts := strings.Fields(env)
	parts = append(parts, hosts...)
	return strings.Join(parts, " ")
}

func reachableHosts(detected []string) []string {
	var out []string
	for _, name := range detected {
		if e, ok := provider.Lookup(name); ok {
			out = append(out, e.Hosts...)
		}
	}
	return out
}

// policyProviderHosts names the endpoints the egress DLP must treat as
// on-provider: where a credential this run legitimately holds is allowed to go.
// It unions the detected providers' hosts with the ones the manifest declares
// the harness can use, because the second set is the only one a SUBSCRIPTION
// harness has — it logs in inside the sandbox, so no key is detectable
// host-side, yet the token it mints still has to reach the vendor.
func policyProviderHosts(detected []string, c manifest.Capabilities) []string {
	seen, out := map[string]bool{}, []string{}
	for _, h := range append(reachableHosts(detected), reachableHosts(c.Providers)...) {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// cacheApplies reports whether a remembered answer may take part in this run.
// tty is passed in rather than probed so the rule is testable without a PTY.
func cacheApplies(printOnly, tty bool) bool { return !printOnly && wizardEnabled() && tty }

// seedFromCache fills the axes the operator did not state explicitly from a
// remembered answer. Whether the cache applies at all is the caller's decision.
func (p *runParams) seedFromCache(cached agentsettings.Choice, lookup func(string) string, evidenceSet bool) {
	if !p.modeSet && cached.Egress != "" {
		p.mode = cached.Egress
	}
	if !p.credsSet && cached.Credentials != "" {
		p.credentials = cached.Credentials
	}
	p.addons, p.addonsAnswered = normalizeAddons(cached.Addons), true
	if p.authVar == "" {
		p.authVar = cached.AuthVar
	}
	if !evidenceSet && cached.Evidence != "" {
		p.evidence = cached.Evidence
	}
	p.roles = mergeRoles(provider.RolesFrom(lookup), cached.Models)
}

func filterProviders(detected []string, c manifest.Capabilities) []string {
	if len(c.Providers) == 0 {
		return detected
	}
	out := make([]string, 0, len(detected))
	for _, d := range detected {
		if c.AllowsProvider(d) {
			out = append(out, d)
		}
	}
	return out
}

func (p *runParams) promptChoices(man manifest.Manifest, lookup func(string) string, repoRoot, homeRoot string) error {
	sbxBackend, sbxWhy := false, ""
	if man.IsSbx() {
		sbxBackend, sbxWhy = sbx.Available()
	}
	sandboxOn := sbxBackend && p.sandboxAddonOn()
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("run %s — confirm or change this run", p.target),
		Header: buildHeader(man, lookup, p.roles, repoRoot, p.input, homeRoot),
		Rows: applicableRows(
			reviewAvailability(axisRow("egress", egress.Modes(), man.Capabilities.Egress, p.mode), sandboxOn),
			axisRow("credentials", egress.CredentialModes(), man.Capabilities.Credentials, p.credentialsOrDefault()),
		),
	}
	if auth := availableAuthVars(man, lookup); len(auth) > 1 {
		form.Rows = append(form.Rows, applicableRows(
			axisRow("auth", auth, auth, orElseFirst(p.authVar, auth)),
		)...)
	}
	if addons := addonOptions(man); len(addons) > 0 {
		form.Rows = append(form.Rows, applicableRows(choiceui.Row{
			Label: "add-ons", Options: addons, Multi: true, On: p.addonDefaults(addons),
		})...)
	}
	form.Rows = append(form.Rows, evidenceRow(p.evidenceOrDefault()))
	form.OnChange = func(f *choiceui.Form) {
		gateAddons(f, p.mode, p.credentialsOrDefault(), sbxWhy)
		// Toggling the sandbox add-on moves the review tier with it: the consent
		// gate has no sbx transport, so review is reachable only off that backend.
		gateReview(f, hasAddon(f.Selections("add-ons"), addonSandbox))
		gateEvidence(f)
	}
	form.OnChange(form)

	ok, err := form.Run()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cancelled at the choice prompt")
	}
	if v := form.Selection("egress"); v != "" && !p.modeSet {
		p.mode = v
	}
	if v := form.Selection("credentials"); v != "" && !p.credsSet {
		p.credentials = v
	}
	p.addons, p.addonsAnswered = form.Selections("add-ons"), true
	if v := form.Selection("auth"); v != "" {
		p.authVar = v
	}
	p.evidence = evidenceFrom(form.Selections(evidenceLabel))
	return nil
}

// evidenceRow offers the two levels as checkboxes with verbose ticked.
func evidenceRow(current string) choiceui.Row {
	opts := []string{evidenceDefault, evidenceVerbose}
	on := make([]bool, len(opts))
	for i, o := range opts {
		on[i] = o == current
	}
	return choiceui.Row{Label: evidenceLabel, Options: opts, Multi: true, On: on}
}

// gateEvidence keeps the two evidence boxes exclusive.
func gateEvidence(f *choiceui.Form) {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != evidenceLabel {
			continue
		}
		if r.Selected < 0 || r.Selected >= len(r.On) || !r.On[r.Selected] {
			return
		}
		for j := range r.On {
			if j != r.Selected {
				r.On[j] = false
			}
		}
		return
	}
}

func evidenceFrom(selected []string) string {
	for _, v := range selected {
		if v == evidenceVerbose {
			return evidenceVerbose
		}
	}
	return evidenceDefault
}

// availableAuthVars lists the credentials the operator holds for the provider
// this run will pin.
func availableAuthVars(man manifest.Manifest, lookup func(string) string) []string {
	detected := filterProviders(provider.Detect(lookup), man.Capabilities)
	if len(detected) != 1 {
		if pin := strings.TrimSpace(man.Provider); pin != "" {
			detected = []string{pin}
		} else {
			return nil
		}
	}
	var out []string
	for _, v := range provider.AuthVars(detected[0]) {
		if strings.TrimSpace(lookup(v)) != "" {
			out = append(out, v)
		}
	}
	return out
}

func orElseFirst(v string, opts []string) string {
	if v != "" {
		return v
	}
	if len(opts) > 0 {
		return opts[0]
	}
	return ""
}

func gateAddons(f *choiceui.Form, tierFallback, credsFallback, sbxWhy string) {
	tier := f.Selection("egress")
	if tier == "" {
		tier = tierFallback
	}
	creds := f.Selection("credentials")
	if creds == "" {
		creds = credsFallback
	}
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != "add-ons" {
			continue
		}
		r.Off = make([]bool, len(r.Options))
		r.Reason = ""
		for j, opt := range r.Options {
			if opt == addonSandbox {
				// Offered, but only checkable on a host that can actually run it.
				if sbxWhy != "" {
					r.Off[j] = true
					r.Reason = "docker sandbox: " + sbxWhy
				}
				continue
			}
			if opt != addonDind {
				continue
			}
			if !dind.ModeSupported(tier) || !dind.CredentialsSupported(creds) {
				r.Off[j] = true
				r.Reason = addonDind + " needs egress open + credentials forward"
			}
		}
	}
}

// gateReview re-greys the review tier whenever the sandbox add-on is toggled,
// so the egress row keeps telling the truth about what this run can reach.
func gateReview(f *choiceui.Form, sandboxOn bool) {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != "egress" {
			continue
		}
		if len(r.Off) != len(r.Options) {
			r.Off = make([]bool, len(r.Options))
		}
		for j, opt := range r.Options {
			if opt != "review" {
				continue
			}
			reason := ""
			switch {
			case sandboxOn:
				reason = "review: not supported on the docker sandbox backend"
			default:
				if ok, why := reviewSupported(os.Getenv); !ok {
					reason = "review: " + why
				}
			}
			r.Off[j] = reason != ""
			switch {
			case reason != "":
				r.Reason = reason
			case strings.HasPrefix(r.Reason, "review:"):
				r.Reason = ""
			}
			if r.Off[j] && r.Selected == j {
				r.Selected = firstSelectableIn(r)
			}
		}
	}
}

// firstSelectableIn is the fallback selection when the current one is greyed out.
func firstSelectableIn(r *choiceui.Row) int {
	for i := range r.Options {
		if i >= len(r.Off) || !r.Off[i] {
			return i
		}
	}
	return 0
}

func axisRow(label string, all, allowed []string, preselect string) choiceui.Row {
	opts := all
	if len(allowed) > 0 {
		opts = allowed
	}
	r := choiceui.Row{Label: label, Options: opts}
	for i, o := range opts {
		if strings.EqualFold(o, preselect) {
			r.Selected = i
		}
	}
	return r
}

// reviewSupported reports whether the review tier's consent gate can work here.
func reviewSupported(getenv func(string) string) (ok bool, why string) {
	if runtime.GOOS != "linux" {
		return false, "linux only"
	}
	if h := strings.TrimSpace(getenv("DOCKER_HOST")); h != "" && !strings.HasPrefix(h, "unix://") {
		return false, "needs a local docker daemon"
	}
	return true, ""
}

// reviewAvailability greys the review option out on hosts whose transport
// cannot carry the gate.
func reviewAvailability(r choiceui.Row, sandboxBackend bool) choiceui.Row {
	if sandboxBackend {
		return comingSoon(r, "review", "review: not supported on the docker sandbox backend")
	}
	if ok, why := reviewSupported(os.Getenv); !ok {
		return comingSoon(r, "review", "review: "+why)
	}
	return r
}

// comingSoon greys an option out and moves the selection off it.
func comingSoon(r choiceui.Row, option, reason string) choiceui.Row {
	r.Off = make([]bool, len(r.Options))
	for i, o := range r.Options {
		if o != option {
			continue
		}
		r.Off[i] = true
		r.Reason = reason
		if r.Selected == i {
			r.Selected = firstEnabled(r)
		}
	}
	return r
}

func firstEnabled(r choiceui.Row) int {
	for i := range r.Options {
		if i >= len(r.Off) || !r.Off[i] {
			return i
		}
	}
	return 0
}

// applicableRows drops axes with nothing to decide.
func applicableRows(rows ...choiceui.Row) []choiceui.Row {
	out := make([]choiceui.Row, 0, len(rows))
	for _, r := range rows {
		if r.Multi || len(r.Options) > 1 {
			out = append(out, r)
		}
	}
	return out
}

// The docker add-ons: one row entry per way a harness can hand the agent a
// Docker daemon. Both are CHECKED by default wherever the manifest declares
// them — the picker shows what the run is about to do, and unchecking is how an
// operator opts out (sandbox → docker+egress; dind → no sidecar). Each is still
// subject to its own gate, so an entry can be checked and greyed at once, with
// the reason on the row.
const (
	addonSandbox = "docker (sandbox)"
	addonDind    = "docker (dind)"
)

func addonOptions(man manifest.Manifest) []string {
	var opts []string
	for target := range man.Images {
		if strings.HasSuffix(target, "-browser") {
			opts = append(opts, "browser")
			break
		}
	}
	// One entry, never two: the manifest's docker mode IS the choice, so the
	// picker cannot offer a harness both daemons.
	switch man.Docker {
	case manifest.DockerSbx:
		opts = append(opts, addonSandbox)
	case manifest.DockerDind:
		opts = append(opts, addonDind)
	}
	return opts
}

// sandboxAddonOn reports whether this run takes the sandbox backend. It is
// default-ON: only a remembered or prompted answer can turn it off, so a first
// run — and every non-interactive one — still gets the sandbox.
func (p *runParams) sandboxAddonOn() bool {
	return hasAddon(p.addons, addonSandbox) || !p.addonsAnswered
}

// addonDefaults is the picker's initial checkbox state: a remembered answer
// wins, and absent one BOTH docker add-ons start checked — the run is going to
// use them, so the box that says so is ticked before the operator is asked.
func (p *runParams) addonDefaults(opts []string) []bool {
	on := make([]bool, len(opts))
	for i, a := range opts {
		on[i] = hasAddon(p.addons, a) ||
			((a == addonSandbox || a == addonDind) && !p.addonsAnswered)
	}
	return on
}

// normalizeAddons upgrades the names a previous version remembered, so a cached
// choice keeps meaning what the operator picked.
func normalizeAddons(addons []string) []string {
	out := make([]string, 0, len(addons))
	for _, a := range addons {
		if a == "dind" {
			a = addonDind
		}
		out = append(out, a)
	}
	return out
}

func reportLinks(links []workspace.Link) {
	for _, l := range links {
		switch l.Action {
		case workspace.LinkMounted:
			ui.Iconf("🔗", "%s → %s (symlink leaves the workspace; target mounted)", l.Rel, l.Target)
		case workspace.LinkRefused:
			target := l.Target
			if target == "" {
				target = "(unresolved)"
			}
			ui.Warnf("%s → %s is not available inside the sandbox: %s", l.Rel, target, l.Reason)
		default:
			ui.Logf("%s: %s", l.Rel, l.Reason)
		}
	}
}

func ghConfigMount(getenv func(string) string) (runner.Mount, bool) {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_MOUNT_GH_CONFIG"))) {
	case "0", "off", "no", "false":
		return runner.Mount{}, false
	}
	dir := strings.TrimSpace(getenv("GH_CONFIG_DIR"))
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			return runner.Mount{}, false
		}
		dir = filepath.Join(home, ".config", "gh")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return runner.Mount{}, false
	}
	return runner.Mount{
		Host:      dir,
		Container: proveohome.ContainerHome + "/.config/gh",
		ReadOnly:  true,
	}, true
}

func mountRootDeps(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_MOUNT_ROOT_DEPS"))) {
	case "0", "off", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

func hasAddon(addons []string, name string) bool {
	for _, a := range addons {
		if a == name {
			return true
		}
	}
	return false
}

func buildHeader(man manifest.Manifest, lookup func(string) string, roles provider.Roles, repoRoot, inputDir, homeRoot string) []string {
	if inputDir == "" {
		inputDir = repoRoot
	}
	h := gitHeader(repoRoot)
	h = append(h, choiceui.EnvHeader(loadedSecretNames(man, lookup), loadedSettings(man, lookup))...)
	h = append(h, workspaceHeader(man, inputDir, repoRoot, homeRoot)...)
	if line := rolesLine(roles); line != "" {
		h = append(h, "🧠 "+line)
	}
	return h
}

func loadedSecretNames(man manifest.Manifest, lookup func(string) string) []string {
	seen, out := map[string]bool{}, []string{}
	add := func(k string) {
		if k != "" && !seen[k] && strings.TrimSpace(lookup(k)) != "" {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, e := range man.Env {
		if e.Secret {
			add(e.Name)
		}
	}
	for _, name := range provider.Names() {
		if !man.Capabilities.AllowsProvider(name) {
			continue
		}
		e, _ := provider.Lookup(name)
		for _, k := range e.Detect {
			add(k)
		}
	}
	return out
}

func loadedSettings(man manifest.Manifest, lookup func(string) string) map[string]string {
	out := map[string]string{}
	for _, k := range configVarsFor(man) {
		if v := strings.TrimSpace(lookup(k)); v != "" {
			out[k] = v
		}
	}
	return out
}

func gitRootOrEmpty(ws workspace.Scope, repoRoot string) string {
	if !ws.IsRepo {
		return ""
	}
	return repoRoot
}

func gitHeader(repoRoot string) []string {
	if repoRoot == "" {
		return []string{"git:      (not a repository)"}
	}
	branch := "detached"
	if out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			branch = b
		}
	}
	dirty := ""
	if out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		dirty = " (uncommitted changes)"
	}
	return []string{fmt.Sprintf("git:      %s on %s%s", filepath.Base(repoRoot), branch, dirty)}
}

var toolingMarkers = []wsscan.Marker{
	{Label: "go", Names: []string{"go.mod", "go.work"}, Suffixes: []string{".go"}},
	{Label: "node", Names: []string{"package.json"}},
	{Label: "nx", Names: []string{"nx.json"}},
	{Label: "turbo", Names: []string{"turbo.json"}},
	{Label: "mise", Names: []string{"mise.toml", ".mise.toml", ".tool-versions"}},
	{Label: "python", Names: []string{"pyproject.toml", "requirements.txt"}, Suffixes: []string{".py"}},
	{Label: "rust", Names: []string{"Cargo.toml"}},
	{Label: "docker", Names: []string{"Dockerfile", "compose.yml", "docker-compose.yml"}},
}

var lspMarkers = []wsscan.Marker{
	{Label: "gopls", Names: []string{"go.mod"}, Suffixes: []string{".go"}},
	{Label: "typescript-language-server", Names: []string{"tsconfig.json", "package.json"}, Suffixes: []string{".ts", ".tsx"}},
	{Label: "pyright-langserver", Names: []string{"pyproject.toml"}, Suffixes: []string{".py"}},
	{Label: "bash-language-server", Suffixes: []string{".sh"}},
	{Label: "docker-langserver", Names: []string{"Dockerfile"}},
	{Label: "yaml-language-server", Suffixes: []string{".yml", ".yaml"}},
}

func ToolingLabels() []string {
	out := make([]string, 0, len(toolingMarkers))
	for _, m := range toolingMarkers {
		out = append(out, m.Label)
	}
	return out
}

func workspaceHeader(man manifest.Manifest, inputDir, repoRoot, homeRoot string) []string {
	if inputDir == "" {
		return nil
	}
	var out []string
	tools := wsscan.Scan(inputDir, repoRoot, toolingMarkers, 0)
	if labels := tools.Labels(toolingMarkers); len(labels) > 0 {
		out = append(out, "tooling:  "+strings.Join(labels, "  "))
	}
	lsp := wsscan.Scan(inputDir, repoRoot, lspMarkers, 0)
	if labels := lsp.Labels(lspMarkers); len(labels) > 0 {
		out = append(out, "lsp:      will start "+strings.Join(labels, "  "))
	}
	if tools.Truncated || lsp.Truncated {
		ui.Warnf("workspace scan hit its entry budget under %s — tooling/LSP lines may be incomplete", inputDir)
	}
	if n := countAgents(man, inputDir, homeRoot); n > 0 {
		out = append(out, fmt.Sprintf("subagents: %d definition(s)", n))
	}
	if hooks := detectHooks(man, inputDir, homeRoot); len(hooks) > 0 {
		out = append(out, "hooks:    "+strings.Join(hooks, "  "))
	}
	return out
}

func agentDirs(man manifest.Manifest, inputDir, homeRoot string) []string {
	var dirs []string
	if cd := man.Workspace.ConfigDir; cd != "" {
		dirs = append(dirs, filepath.Join(inputDir, cd, "agents"))
	}
	for _, m := range man.Home.Mounts {
		dirs = append(dirs, filepath.Join(homeRoot, m.Host, "agents"))
	}
	return dirs
}

func countAgents(man manifest.Manifest, inputDir, homeRoot string) int {
	n := 0
	for _, d := range agentDirs(man, inputDir, homeRoot) {
		m, _ := filepath.Glob(filepath.Join(d, "*.md"))
		n += len(m)
	}
	return n
}

func detectHooks(man manifest.Manifest, inputDir, homeRoot string) []string {
	var out []string
	if cd := man.Workspace.ConfigDir; cd != "" {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			if b, err := os.ReadFile(filepath.Join(inputDir, cd, f)); err == nil && strings.Contains(string(b), `"hooks"`) {
				out = append(out, cd+"/"+f)
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(inputDir, ".git", "hooks")); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sample") {
				out = append(out, "git/"+e.Name())
			}
		}
	}
	return out
}

func brokerProviders(forwards bool, man manifest.Manifest, detected []string, lookup func(string) string, brokerOn bool) []string {
	if forwards || !brokerOn {
		return nil
	}
	if pin := strings.TrimSpace(man.Provider); pin != "" {
		e, ok := provider.Lookup(pin)
		if !ok {
			return nil
		}
		for _, v := range e.Detect {
			if strings.TrimSpace(lookup(v)) != "" {
				return []string{pin}
			}
		}
		return nil
	}
	return detected
}

func warnUnknownModel(key, value, localModel string) {
	if localModel != "" || !strings.HasSuffix(key, "_MODEL") {
		return
	}
	if known, ok := provider.CheckModel(value); ok && !known {
		ui.Warnf("%s=%q is not a model id this proveo build recognizes — if it is a typo the agent will "+
			"fail on every call; if it is newer than this binary, ignore this.", key, value)
	}
}

func configVarsFor(man manifest.Manifest) []string {
	out := append([]string(nil), entrypoint.ConfigVars...)
	seen := make(map[string]bool, len(out))
	for _, k := range out {
		seen[k] = true
	}
	for _, k := range man.Config {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func brokerOffReason(forwards bool, routed []string, detected []string, brokerOn bool) string {
	if forwards || len(routed) > 0 || len(detected) == 0 {
		return ""
	}
	if !brokerOn {
		return fmt.Sprintf("credential broker disabled (PROVEO_CREDENTIAL_BROKER) — the agent gets the %q "+
			"sentinel, not a working key", entrypoint.DefaultSentinel)
	}
	return fmt.Sprintf("credential broker OFF: %d key(s) detected (%s) but none is broker-injectable — "+
		"the agent will receive the %q sentinel and the provider will reject it. Use --credentials forward "+
		"to hand the real key to the container.",
		len(detected), strings.Join(detected, ", "), entrypoint.DefaultSentinel)
}

// needsLifecycle reports whether the plan created any network/sidecar, so the run
// must go through the egress lifecycle rather than a bare `docker run`.
func needsLifecycle(p egress.Plan) bool {
	return len(p.Networks) > 0 || len(p.Sidecars) > 0
}

// assembleInput is the fully-resolved, side-effect-free input to assemble.
type assembleInput struct {
	params                              runParams
	sid, egDir                          string
	uid, gid                            string
	modelsDir, brokerFile               string
	hostOllama, ollamaGPU               bool
	mounts                              []runner.Mount
	workdir                             string
	env                                 []string // declared env var names to forward (bare -e)
	providerDomains                     string
	squidImage, proxyImage, ollamaImage string
	pidsLimit                           int      // host/tier-resolved --pids-limit
	reviewSocket                        string   // review tier: host path of the consent gate socket
	providers                           []string // every provider the broker holds a route for
	writeHosts                          []string // endpoints of every provider the allowlist admits
	providerHosts                       []string // endpoints the DLP treats as on-provider
}

func assemble(in assembleInput) (egress.Plan, runner.Config, error) {
	plan, err := egress.BuildPlan(egress.Options{
		Mode: in.params.mode, Credentials: in.params.credentials,
		SessionID: in.sid, AgentName: in.params.target, UID: in.uid, GID: in.gid,
		LocalModel: in.params.localModel, ModelsDir: in.modelsDir, Providers: in.providers, BrokerEnvFile: in.brokerFile,
		HostOllama: in.hostOllama, OllamaGPU: in.ollamaGPU,
		ProviderDomains: in.providerDomains,
		ReviewSocket:    in.reviewSocket,
		AuthVar:         in.params.authVar,
		WriteHosts:      in.writeHosts,
		ProviderHosts:   in.providerHosts,
		ConfDir:         filepath.Join(in.egDir, "mitmproxy", "confdir"),
		FlowsDir:        filepath.Join(in.egDir, "mitmproxy", "flows"),
		SquidConfigDir:  filepath.Join(in.egDir, "squid", "config"),
		SquidLogDir:     filepath.Join(in.egDir, "squid", "logs"),
		SquidImage:      in.squidImage, ProxyImage: in.proxyImage, OllamaImage: in.ollamaImage,
	})
	if err != nil {
		return egress.Plan{}, runner.Config{}, err
	}
	agent := runner.Config{
		Interactive: true, Remove: true, User: in.uid + ":" + in.gid,
		Mounts:    in.mounts,
		Workdir:   in.workdir,
		Env:       in.env,
		ExtraArgs: plan.AgentArgs, Image: in.params.image, Command: in.params.extra,
		PidsLimit: in.pidsLimit,
	}
	if in.params.dataDir != "" {
		agent.Mounts = append(agent.Mounts, runner.Mount{Host: in.params.dataDir, Container: "/workspace/data", ReadOnly: true})
	}
	if in.params.shell {
		agent.Entrypoint = "bash" // open a shell instead of launching the agent
	}
	return plan, agent, nil
}

// reportSbxUnavailable warns that the run fell back, naming the host engine.
func reportSbxUnavailable(why string) {
	ui.Warnf("docker sandbox unavailable (%s) — falling back to docker+egress", why)
	if eng := engine.Detect(); eng.Kind != engine.Unknown {
		ui.Notef("    engine: %s (%s)", eng.Label(), eng.Isolation())
	}
	if cmd := sbx.InstallCmd(sbx.Installed()); cmd != "" {
		if sbx.Installed() {
			ui.Notef("    proveo targets sbx %s or newer:", sbx.MinVersion)
		} else {
			ui.Notef("    sbx is standalone and does not need Docker Desktop:")
		}
		ui.Notef("      %s", cmd)
	}
}

// ensureSbx brings the sbx CLI up to the version this build drives, so the
// operator is not the one tracking a pre-GA tool's releases. It returns whether
// the backend is usable and, when not, why.
//
// The install is CONFIRMED, never silent: it mutates the host outside proveo's
// own state, so it follows the same gate as a missing sidecar image
// (PROVEO_AUTO_PROVISION, else a TTY prompt, else declined). Declining is not an
// error — the run falls back to docker+egress and says so.
func ensureSbx() (bool, string) {
	ok, why := sbx.Available()
	if ok {
		return true, ""
	}
	install := sbx.InstallCmd(sbx.Installed())
	if install == "" {
		return false, why // nothing to offer on this platform
	}
	verb := "install"
	if sbx.Installed() {
		verb = "upgrade"
	}
	if !provisionConfirm(fmt.Sprintf("%s the docker sandboxes CLI (%s)?", verb, install)) {
		return false, why
	}
	ui.Iconf("📦", "%sing sbx: %s", verb, install)
	c := exec.Command("bash", "-lc", install)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return false, fmt.Sprintf("%s failed: %v", verb, err)
	}
	return sbx.Available()
}

// sbxReady resolves the backend for a real run, provisioning the CLI when the
// operator allows it. A dry run only ever REPORTS: --print must not install
// anything, or `--print` stops being a way to inspect a plan safely.
func sbxReady(printOnly bool) (bool, string) {
	if printOnly {
		return sbx.Available()
	}
	return ensureSbx()
}

// runSandboxInput is the resolved input to the sbx backend.
type runSandboxInput struct {
	params           runParams
	man              manifest.Manifest
	sid, egDir       string
	mounts           []runner.Mount
	workdir          string
	lookup           func(string) string
	detected         []string
	gitEnv           []string
	homeEnv          []string
	scopeRel         string
	worktreeFallback bool
	worktreeEnv      []string
	dataDir          string
}

// sandboxSpec resolves the sbx invocation: RunConfig, Kit, and host-side secrets.
func sandboxSpec(in runSandboxInput) (sbx.RunConfig, sbx.Kit, [][2]string) {
	p := in.params

	hosts := map[string]bool{}
	for _, d := range strings.Fields(joinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), in.man.Capabilities.Hosts)) {
		hosts[d] = true
	}
	for _, h := range reachableHosts(in.detected) {
		hosts[h] = true
	}
	allow := make([]string, 0, len(hosts))
	for h := range hosts {
		allow = append(allow, h)
	}
	sort.Strings(allow)

	var secrets [][2]string
	addSecret := func(name string) {
		v := in.lookup(name)
		if v == "" {
			return
		}
		for _, kv := range secrets {
			if kv[0] == name {
				return
			}
		}
		secrets = append(secrets, [2]string{name, v})
	}
	forwards := p.forwards()
	var forwarded []string
	addForward := func(name string) {
		if in.lookup(name) == "" {
			return
		}
		for _, n := range forwarded {
			if n == name {
				return
			}
		}
		forwarded = append(forwarded, name)
	}
	for _, e := range in.man.Env {
		if !e.Secret {
			continue
		}
		if forwards {
			addForward(e.Name)
			continue
		}
		addSecret(e.Name)
	}
	for _, k := range provider.KeyVars() {
		if forwards {
			addForward(k)
			continue
		}
		addSecret(k)
	}

	var env []string
	env = append(env, forwarded...)
	for _, e := range in.man.Env {
		if e.Secret {
			continue
		}
		if v := strings.TrimSpace(in.lookup(e.Name)); v != "" {
			env = append(env, e.Name+"="+v)
		}
	}
	for _, k := range configVarsFor(in.man) {
		if v := strings.TrimSpace(in.lookup(k)); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, evidenceVar+"="+p.evidenceOrDefault())
	env = append(env, in.gitEnv...)
	env = append(env, in.homeEnv...)
	if in.scopeRel != "" {
		env = append(env, "PROVEO_SCOPE_REL="+in.scopeRel)
	}
	if in.worktreeFallback {
		env = append(env, in.worktreeEnv...)
	}

	var mounts []sbx.Mount
	for _, m := range in.mounts {
		mounts = append(mounts, sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly})
	}
	if in.dataDir != "" {
		mounts = append(mounts, sbx.Mount{Host: in.dataDir, Container: "/workspace/data", ReadOnly: true})
	}

	command := p.extra
	if p.shell {
		command = []string{"bash"} // open a shell instead of launching the agent
	}
	cfg := sbx.RunConfig{
		Name: in.sid,
		// The Kit path is resolved HERE, not at write time, so --print renders the
		// same argv the run executes. Deriving it only inside runSandbox left the
		// dry run silently missing --kit — the posture the Kit carries (allowlist,
		// brokered credentials) was invisible in exactly the output an operator
		// inspects to check that posture.
		KitDir:  filepath.Join(in.egDir, "sbx", "kit"),
		Image:   p.image,
		Mounts:  mounts,
		Env:     env,
		Workdir: in.workdir,
		Command: command,
	}
	kit := sbx.Kit{
		Name:           p.target,
		Image:          p.image,
		Network:        sbx.KitNet{AllowedDomains: allow},
		CredentialsEnv: secretNames(secrets),
	}
	return cfg, kit, secrets
}

func secretNames(secrets [][2]string) []string {
	out := make([]string, 0, len(secrets))
	for _, kv := range secrets {
		out = append(out, kv[0])
	}
	return out
}

// runSandbox renders the Kit, injects credentials, runs the agent, tears down.
func runSandbox(in runSandboxInput) error {
	cfg, kit, secrets := sandboxSpec(in)
	// sandboxSpec already put the Kit path on cfg so --print renders the argv this
	// run will actually use; here the file behind that path gets written.
	if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
		return err
	}
	// The sandbox runtime keeps its OWN image store, so the harness image has to
	// be handed over before the Kit can name it. Local and per-user on purpose:
	// each operator has their own docker login and their own sbx config, so this
	// stays a pipe between the two stores rather than a registry round-trip.
	if err := sbx.EnsureTemplate(cfg.Image, func(f string, a ...any) {
		ui.Iconf("📦", f, a...)
	}); err != nil {
		return err
	}
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") {
			hydrateProcessEnv(e, in.lookup)
		}
	}
	for _, kv := range secrets {
		ui.Iconf("🔐", "sandbox secret: %s (host-side injection)", kv[0])
		if err := sbx.SecretSet(kv[0], kv[1]); err != nil {
			return fmt.Errorf("sandbox secret %s: %w", kv[0], err)
		}
	}
	args := sbx.RunArgs(cfg)
	c := exec.Command(sbx.Binary, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := c.Run()
	defer func() {
		if rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput(); rmErr != nil {
			ui.Warnf("sandbox teardown failed (%v): %s", rmErr, strings.TrimSpace(string(rmOut)))
		}
	}()
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return agentExitError{code: ee.ExitCode()}
	}
	return runErr
}

func captureSidecarLogs(r egress.ExecRunner, egDir string, plan egress.Plan) {
	for name, file := range map[string]string{
		plan.ProxyContainer:  "inspector.log",
		plan.SquidContainer:  "squid.log",
		plan.OllamaContainer: "ollama.log",
	} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		out, err := exec.Command("docker", "logs", name).CombinedOutput()
		if err != nil && len(out) == 0 {
			continue
		}
		_ = os.WriteFile(filepath.Join(egDir, file), out, 0o600)
	}
}

// observability describes what evidence a posture can produce.
func observability(mode, credentials string) string {
	if mode == "open" && credentials == "forward" {
		return "none — plain bridge, no MITM, no Squid: provider errors are NOT proveo denials"
	}
	if mode == "open" {
		return "flows.ndjson (MITM only, no allowlist)"
	}
	return "flows.ndjson + squid access.log"
}

func mergeRoles(explicit provider.Roles, remembered map[string]string) provider.Roles {
	out := provider.Roles{}
	for k, v := range explicit {
		out[k] = v
	}
	for k, v := range provider.RolesFromCanonical(remembered) {
		if _, set := out[k]; !set {
			out[k] = v
		}
	}
	return out
}

// rolesLine renders the role assignment for the transcript and the prompt header.
func rolesLine(r provider.Roles) string {
	var parts []string
	for _, kv := range r.Sorted() {
		if p := provider.ModelProvider(kv[1]); p != "" {
			parts = append(parts, fmt.Sprintf("%s=%s (%s)", kv[0], kv[1], p))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", kv[0], kv[1]))
	}
	return strings.Join(parts, " ")
}

func execWithEgress(plan egress.Plan, agent runner.Config, egDir string, providers []string, dindSidecar *dind.Sidecar, reviewProxy *ptyproxy.Proxy) error {
	r := egress.ExecRunner{Stderr: true}
	rq := egress.ExecRunner{}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			captureSidecarLogs(rq, egDir, plan)
			plan.Teardown(rq)
			dindSidecar.Cleanup(dind.ExecRunner{})
			_ = os.RemoveAll(filepath.Join(egDir, "inject"))
		})
	}
	defer cleanup()
	stopSig := onSignalCleanup(cleanup)
	defer stopSig()

	if plan.UsesSquid {
		squidCfg := filepath.Join(egDir, "squid", "config")
		if err := egress.StageSquidConfig(proveo.SquidConfig, squidCfg, providers, os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS")); err != nil {
			return err
		}
		logs := filepath.Join(egDir, "squid", "logs")
		if err := os.MkdirAll(logs, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(logs, 0o777); err != nil {
			return err
		}
	}
	if plan.CAWaitPath != "" {
		for _, d := range []string{filepath.Join(egDir, "mitmproxy", "confdir"), filepath.Join(egDir, "mitmproxy", "flows")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
	}

	if err := plan.Apply(r); err != nil {
		return err
	}
	if dindSidecar != nil && plan.AgentNetwork != "" {
		if err := dindSidecar.ConnectNetwork(dind.ExecRunner{}, plan.AgentNetwork); err != nil {
			return fmt.Errorf("attach dind to agent network: %w", err)
		}
	}
	if plan.SquidContainer != "" {
		if err := egress.WaitSquidReady(rq, plan.SquidContainer, 30*time.Second); err != nil {
			return fmt.Errorf("squid upstream not ready: %w", err)
		}
	}
	if plan.CAWaitPath != "" {
		if err := waitForFile(plan.CAWaitPath, 20*time.Second); err != nil {
			return fmt.Errorf("inspector CA not ready: %w", err)
		}
	}
	if plan.OllamaContainer != "" {
		if err := egress.WaitOllamaReady(rq, plan.OllamaContainer, 60*time.Second); err != nil {
			return fmt.Errorf("ollama sidecar not ready: %w", err)
		}
	}
	return execAgentWithProxy(agent, reviewProxy)
}

func execAgentWithProxy(agent runner.Config, proxy *ptyproxy.Proxy) error {
	c := exec.Command("docker", runner.DockerRunArgs(agent)...)
	var err error
	if proxy != nil {
		err = proxy.Run(c)
	} else {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = c.Run()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return agentExitError{code: ee.ExitCode()}
	}
	return err
}

func startReviewGate(mode, egDir string) (*reviewgate.Gate, *ptyproxy.Proxy, func()) {
	if mode != "review" {
		return nil, nil, func() {}
	}
	if !ptyproxy.Usable(os.Stdin, os.Stdout) {
		ui.Warnf("review tier without a terminal: no way to ask, so every new connection will be denied")
		return nil, nil, func() {}
	}
	proxy := ptyproxy.New(os.Stdin, os.Stdout)
	gate := reviewgate.New(func(host, port string) bool {
		allowed := false
		if err := proxy.Overlay(func(in io.Reader, _ io.Writer) error {
			var derr error
			allowed, derr = choiceui.Consent(func() (tcell.Screen, error) {
				return proxy.OverlayScreen(in)
			}, host, port)
			return derr
		}); err != nil {
			ui.Warnf("review prompt unavailable (%v): denying %s:%s", err, host, port)
			return false
		}
		return allowed
	})
	dir := filepath.Join(egDir, "review")
	if err := gate.Listen(dir); err != nil {
		ui.Warnf("%v — connections will be denied", err)
		return nil, proxy, func() {}
	}
	return gate, proxy, func() {
		_ = gate.Close()
		if d := gate.Decisions(); len(d) > 0 {
			var allowed, denied int
			for _, v := range d {
				if v == reviewgate.Allow {
					allowed++
				} else {
					denied++
				}
			}
			ui.Iconf("🛂", "review: %d host(s) allowed, %d denied", allowed, denied)
		}
	}
}

func onSignalCleanup(cleanup func()) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		if _, ok := <-sigs; ok {
			cleanup()
			os.Exit(130) // 128 + SIGINT
		}
	}()
	return func() { signal.Stop(sigs) }
}

// ollamaModelsDir resolves the host Ollama model store: PROVEO_OLLAMA_MODELS_DIR
// else $HOME/.ollama/models (mirrors defs/lib/egress.sh).
func ollamaModelsDir() string {
	if d := os.Getenv("PROVEO_OLLAMA_MODELS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

// preferHostOllama reports whether --local-model should target the host's Ollama
// (host.docker.internal) instead of a sidecar.
func preferHostOllama() bool {
	if os.Getenv("PROVEO_LOCAL_MODEL_SIDECAR") == "1" {
		return false
	}
	return runtime.GOOS == "darwin"
}

// sidecarOllamaGPU reports whether the Ollama sidecar can be GPU-accelerated:
// Linux with the NVIDIA container runtime registered in Docker.
func sidecarOllamaGPU() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("docker", "info", "--format", "{{json .Runtimes}}").Output()
	return err == nil && strings.Contains(string(out), "nvidia")
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- helpers ---------------------------------------------------------------

func projectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List monorepo sub-projects discoverable from the current repo",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root := workspace.Resolve(orWD("")).Root
			projs := workspace.DiscoverProjects(root)
			if len(projs) == 0 {
				ui.Notef("no monorepo sub-projects found (not a monorepo, or no workspace members)")
				return nil
			}
			for _, p := range projs {
				fmt.Printf("%-34s %s\n", p.Path, p.Tool)
			}
			return nil
		},
	}
}

func setupCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Add the proveo binary's directory to your shell PATH",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return doSetup(printOnly) },
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "show the change without writing it")
	return cmd
}

func doSetup(printOnly bool) error {
	sh, ok := shell.Detect(os.Getenv("SHELL"))
	if !ok {
		return fmt.Errorf("unrecognized shell %q; add the proveo dir to PATH manually", os.Getenv("SHELL"))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	binDir := filepath.Dir(exe)
	home, _ := os.UserHomeDir()
	rc := sh.RCFile(runtime.GOOS, home)
	line := sh.PathLine(binDir)

	if !sh.Supported {
		ui.Notef("%s is not auto-configured. Add this to %s manually:\n  %s", sh.Name, rc, line)
		return nil
	}
	if onPath(binDir) {
		ui.Okf("%s is already on PATH", binDir)
		return nil
	}
	content, _ := os.ReadFile(rc) // missing rc is fine
	if shell.AlreadyConfigured(string(content), binDir) {
		ui.Okf("%s already configures PATH — restart your shell", rc)
		return nil
	}
	if printOnly {
		fmt.Printf("would append to %s:\n%s", rc, sh.Block(binDir))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(rc, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(sh.Block(binDir)); err != nil {
		return err
	}
	ui.Okf("added %s to PATH in %s — restart your shell or run: source %s", binDir, rc, rc)
	return nil
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// isStdinTTY gates every interactive prompt (scope picker, env wizard).
func isStdinTTY() bool { return isReaderTTY(os.Stdin) }

// isReaderTTY reports whether r is an *os.File attached to a terminal.
func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// pickProject returns the chosen monorepo scope ("" = repo root).
func pickProject(projs []workspace.Project, in io.Reader, out io.Writer) string {
	if isReaderTTY(in) {
		return fuzzyPickProject(projs)
	}
	return pickProjectNumbered(projs, in, out)
}

// fuzzyPickProject shows an interactive finder with "<repo root>" as entry 0.
func fuzzyPickProject(projs []workspace.Project) string {
	labels := make([]string, 0, len(projs)+1)
	labels = append(labels, "<repo root>")
	for _, p := range projs {
		labels = append(labels, p.Path)
	}
	idx, err := fuzzyfinder.Find(labels, func(i int) string { return labels[i] },
		fuzzyfinder.WithPromptString("scope> "))
	if err != nil || idx <= 0 { // ErrAbort, finder failure, or "<repo root>"
		return ""
	}
	return projs[idx-1].Path
}

func pickProjectNumbered(projs []workspace.Project, in io.Reader, out io.Writer) string {
	fmt.Fprintln(out, "Monorepo detected — choose a scope:")
	fmt.Fprintln(out, "   0) <repo root>")
	for i, p := range projs {
		fmt.Fprintf(out, "  %2d) %s\n", i+1, p.Path)
	}
	fmt.Fprint(out, "scope [0]: ")
	s, _ := bufio.NewReader(in).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > len(projs) {
		return ""
	}
	return projs[n-1].Path
}

func sortedKeys(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func orWD(p string) string {
	if p != "" {
		return p
	}
	wd, _ := os.Getwd()
	return wd
}

func warnMountedSecrets(dir, mode string, lookup func(string) string) {
	if dir == "" {
		return
	}
	switch strings.ToLower(mode) {
	case "allowlist", "review":
		return
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		return
	}
	if len(provider.Detect(lookup)) == 0 {
		return
	}
	ui.Warnf("%s/.env is mounted and a provider key is set — the agent can read it directly; use --egress-mode firewall so egress DLP blocks the key from leaving", dir)
}

func brokerEnabled() bool {
	switch strings.ToLower(os.Getenv("PROVEO_CREDENTIAL_BROKER")) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

func stateDir() string {
	if x := os.Getenv("PROVEO_EGRESS_ROOT"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "proveo")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "proveo")
}

func hydrateProcessEnv(name string, lookup func(string) string) {
	if strings.TrimSpace(os.Getenv(name)) != "" {
		return
	}
	if v := strings.TrimSpace(lookup(name)); v != "" {
		_ = os.Setenv(name, v)
	}
}

// providerLookup prefers the process env, then a host-side KEY=VALUE file
// (project .env / PROVEO_EGRESS_ENV_FILE) for detection and broker.env writing.
func providerLookup(envFile string) func(string) string {
	fileVals := parseEnvFile(envFile)
	return func(k string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return fileVals[k]
	}
}

// parseEnvFile reads a KEY=VALUE env file (project .env shape). Missing => empty.
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// writeBrokerEnv writes present provider keys to a 0600 file the egress proxy
// mounts. lookup may include host-side .env values not in the process env.
func writeBrokerEnv(dir string, lookup func(string) string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "broker.env")
	var b strings.Builder
	for _, name := range provider.KeyVars() {
		if v := strings.TrimSpace(lookup(name)); v != "" {
			b.WriteString(name + "=" + v + "\n")
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no provider key in host env")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
