// Command proveo is the harness CLI.
// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/egress/teardown-and-signals.puml, _spec/_paradigms/egress-boundary.puml, _spec/internal/egress/egress-tiers.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/_conventions/design-decision-ids.puml, _spec/_paradigms/credential-boundary.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/provider/model-resolution.puml, _spec/internal/dind/dind-sidecar.puml, _spec/internal/runner/hardened-run-argv.puml, _spec/internal/workspace/mount-model.puml, _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/runlog/run-transcript.puml, _spec/internal/manifest/harness-manifest-schema.puml, _spec/_paradigms/git-identity.puml, _spec/internal/proveohome/proveo-home-components.puml, _spec/_plans/ci-pipeline.puml
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/proveo-ca/proveo/internal/maintain"

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
	var printOnly, shellMode, contSession, listSessions, cloneMode bool
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
				image = imageOverride // an explicit --image is a decision, not a default
			} else {
				image = reportImageChoice(image)
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
				shell: shellMode, printOnly: printOnly, extra: extra, clone: cloneMode,
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
	cmd.Flags().BoolVar(&cloneMode, "clone", false,
		"run the agent on a private in-container CLONE of the repo (sbx only): the workspace is never written, "+
			"and changes come back with `git fetch sandbox-<name>`")
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
	bridges                                                                     provider.BridgeTable
	authVar                                                                     string
	evidence                                                                    string
	shell, printOnly                                                            bool
	extra                                                                       []string
	clone                                                                       bool
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
		ui.Iconf("📝", "run log: %s", rl.Path())
	}

	man, err := manifestForTarget(p.target)
	if err != nil {
		return err
	}
	// Recorded after the manifest resolves, because which artifacts exist depends on
	// which backend will run and that is a property of the harness.
	rl.Artifacts(egDir, p.willSandbox(man))

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
	// Create it here rather than leaving it to the backend. `docker run -v` invents
	// a missing host path, but as ROOT — which is why callers have been creating it
	// themselves to get a dir the run-as user can write. sbx does not invent it at
	// all: it stops and asks "The selected workspace does not exist. Would you like
	// to create it? (y/N)", which is a prompt no unattended run answers.
	if !p.printOnly && p.output != "" {
		if err := os.MkdirAll(p.output, 0o755); err != nil {
			return fmt.Errorf("output dir %s: %w", p.output, err)
		}
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
	{ // one layout: the scope dir drives the /app mount path
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
	if p.bridges == nil {
		if tab, err := provider.LoadBridges(proveo.ModelBridges); err == nil {
			p.bridges = tab
		} else {
			ui.Warnf("model bridge tables unreadable (%v); the header will list role variables instead of resolved slots", err)
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
		p.image = reportImageChoice(browserImage)
		ui.Iconf("🌐", "variant: browser → %s", p.image)
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
	loggedIn, loginNeedsRefresh := persistedLogin(p.target, proveohome.Root(os.Getenv))
	// The agent renews a stale access token itself, but its FIRST turn reports
	// "Login expired · Please run /login" while it does — which reads as a dead
	// credential to the operator, who then goes looking for an auth problem that
	// resolved itself a second later. Saying it up front costs one line.
	if loginNeedsRefresh && !p.printOnly {
		ui.Iconf("🔑", "the login in the proveo home needs a refresh — the agent may report "+
			"\"Login expired\" on its first turn, and can only carry on if the renewal reaches the "+
			"provider from where it runs")
	}
	// Say so when a token IS exported and is being left out. The 🔓 line below only
	// fires when auth is missing, so the case that actually misbills — a token set,
	// silently overriding the mounted login — was the one nothing reported.
	if loggedIn && !p.printOnly && strings.TrimSpace(p.authVar) == "" {
		if av := effectiveAuthVar(man, p.target, p.authVar, proveohome.Root(os.Getenv)); av != "" && strings.TrimSpace(lookup(av)) != "" {
			ui.Iconf("🔓", "%s is set but not injected — the login in the proveo home is the credential, and an env token would override it", av)
		}
	}
	if missing := man.MissingEnv(lookup); len(missing) > 0 && !p.printOnly {
		if man.Subscription && loggedIn {
			// MissingEnv only reads env vars, so a completed login sitting in the
			// proveo home read as "no auth" and produced a warning that sent an
			// operator after a token they did not need.
			ui.Iconf("🔓", "%s: using the login persisted in the proveo home", man.Name)
		} else if man.Subscription {
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
		"enforced by":     enforcedBy(p.willSandbox(man)),
		"image":           imagePosture(p.image),
		"model roles":     rolesLine(p.bridges, p.target, p.roles),
		"role providers":  strings.Join(p.roles.Providers(), ","),
		"sbx mcp gateway": mcpGatewayPosture(p.willSandbox(man)),
		"workspace":       workspacePosture(p.clone),
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
	suppressedAuth := authSuppressor(man, p.target, p.authVar, proveohome.Root(os.Getenv))
	for _, e := range man.Env {
		if strings.TrimSpace(lookup(e.Name)) == "" {
			continue
		}
		if e.Secret {
			if suppressedAuth(e.Name) {
				continue
			}
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
	if man.IsSbx() && p.mode != "review" && sbxEnabled() {
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
	// A `docker: sbx` harness is never offered the dind sidecar (addonOptions:
	// one entry, never two) — and it does not need one. sbx gives each sandbox its
	// OWN daemon, gated on the image label `com.docker.sandboxes.start-docker`,
	// which proveo's sbx-capable images now carry. Measured inside a sandbox on
	// proveo/claudecode: `docker version` reports Server 29.7.2 and `docker run
	// hello-world` succeeds. Nothing to warn about any more; the warning that used
	// to live here told the operator docker would fail, which is now false.
	// --clone is creation-time and sbx-only. Accepting it silently on the docker
	// backend would hand back a run that edited the checkout after promising not
	// to, which is the one failure mode this flag exists to prevent.
	if p.clone && !sbxBackend {
		return fmt.Errorf("--clone is an sbx-backend feature and this run is on docker+egress:\n" +
			"  the agent would edit your checkout directly, which is what --clone asks it not to do.\n" +
			"  Re-run without --clone, or on a target whose manifest declares `docker: sbx`")
	}
	// sbx clones with git, so without a repository there is nothing to clone and
	// the failure surfaces inside the sandbox rather than here.
	if p.clone && wsSpec.RepoRoot == "" {
		return fmt.Errorf("--clone needs a git repository and %s is not inside one:\n"+
			"  sbx builds the sandbox workspace by cloning the host repo over a git daemon.\n"+
			"  Run it from a checkout, or drop --clone to work on the mounted directory",
			wsSpec.InputDir)
	}
	if sbxBackend && p.clone {
		ui.Iconf("\U0001f5c2", "workspace: private clone — your checkout is NOT written. "+
			"Retrieve the agent's commits afterwards with `git fetch sandbox-%s`", sid)
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
			memory:           sbx.MemoryLimit(),
			homeRoot:         homePlan.Root,
			runLog:           rl.Path(),
		}
		if p.printOnly {
			cfg, kit, secrets := sandboxSpec(in)
			// --kit is not decoration: it carries the whole posture — network
			// allowlist, brokered credential declarations, entrypoint — and sbx
			// refuses a run without it. Printing a command whose --kit path was
			// never written produces a failure that reads as an sbx bug rather
			// than a print-mode limitation, so print mode renders the spec even
			// though it executes nothing else. No secret VALUE reaches the file:
			// kitCredentials declares names and headers only.
			if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
				return fmt.Errorf("write sandbox kit: %w", err)
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
			return nil
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
		// stopped being true when the HOME redirect went (see sbxHome): sbx runs its
		// own agent user and its credential proxy writes the live credential into
		// that user's home, so the file under the proveo home is not consulted and
		// its freshness decides nothing. Refusing on it would block runs that work —
		// verified by tests/e2e/ladder_test.go, whose rung 3 carries this exact Kit.
		if len(authMissingAtStart) > 0 {
			// On this backend the agent cannot complete a login: it reaches the
			// prompt, exits, and the sandbox stops with it — which surfaces 30s
			// later as an unrelated 137. Refusing costs nothing; launching costs a
			// minute of image load to reach a failure that was knowable up front.
			// Gated on the persisted login too, because MissingEnv alone would
			// refuse runs whose credentials are already in the proveo home.
			if man.Subscription && !loggedIn {
				printSubscriptionAuthHints(man, authMissingAtStart, os.Stderr)
				sh, _ := shell.Detect(os.Getenv("SHELL"))
				return fmt.Errorf("%s needs a subscription login and the sbx backend cannot complete one:\n"+
					"  the agent exits at its login prompt and the sandbox stops with it.\n"+
					"  Mint a token on the host and export it:\n"+
					"      claude setup-token\n"+
					"      %s\n"+
					"  Or use --egress-mode review, which runs on the docker backend where a login persists",
					man.Name, sh.ExportLine("CLAUDE_CODE_OAUTH_TOKEN", "<token>"))
			}
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
		// PROVEO_SBX is consulted HERE as well as at backend selection, or the two
		// disagree: the picker offered a ticked "docker (sandbox)" while the run took
		// the docker backend, so the prompt described a posture the run did not have.
		if !sbxEnabled() {
			sbxWhy = "PROVEO_SBX is off"
		} else {
			sbxBackend, sbxWhy = sbx.Available()
		}
	}
	sandboxOn := sbxBackend && p.sandboxAddonOn()
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("run %s — confirm or change this run", p.target),
		Header: buildHeader(man, lookup, p.roles, p.bridges, repoRoot, p.input, homeRoot),
		Rows: applicableRows(
			sbxEgressReality(reviewAvailability(axisRow("egress", egress.Modes(), man.Capabilities.Egress, p.mode), sandboxOn), sandboxOn),
			axisRow("credentials", egress.CredentialModes(), man.Capabilities.Credentials, p.credentialsOrDefault()),
		),
	}
	if auth := availableAuthVarsIn(man, lookup, p.target, homeRoot); len(auth) > 1 {
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
// authVarLogin names the credential the operator already established as a FILE in
// the proveo home. It is offered beside the environment variables because it is a
// third way to authenticate and, until it was listed, an unnameable one: the row
// showed only env vars, so a remembered answer naming one of them outranked a login
// the operator had made later — and proveo forwarded a token the API refused while
// a working subscription sat mounted and unread.
const authVarLogin = "login (proveo home)"

// availableAuthVars lists the credentials the operator holds for this harness, the
// persisted login first when there is one: it is the answer that needs no value
// exported, so it belongs where a default lands.
func availableAuthVars(man manifest.Manifest, lookup func(string) string) []string {
	return availableAuthVarsIn(man, lookup, "", "")
}

func availableAuthVarsIn(man manifest.Manifest, lookup func(string) string, target, homeRoot string) []string {
	out := envAuthVars(man, lookup)
	if hasPersistedLogin(target, homeRoot) {
		return append([]string{authVarLogin}, out...)
	}
	return out
}

func envAuthVars(man manifest.Manifest, lookup func(string) string) []string {
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
		if len(r.On) != len(r.Options) {
			r.On = make([]bool, len(r.Options))
		}
		r.Reason = ""
		for j, opt := range r.Options {
			if opt == addonSandbox {
				// Offered, but only checkable on a host that can actually run it.
				if sbxWhy != "" {
					r.Off[j] = true
					// Greyed AND unticked: a ticked box that cannot be honoured is
					// worse than an absent one, because the operator reads it as the
					// posture of the run rather than as a thing they cannot have.
					r.On[j] = false
					r.Reason = "docker sandbox: " + sbxWhy
				}
				continue
			}
			if opt != addonDind {
				continue
			}
			if !dind.ModeSupported(tier) || !dind.CredentialsSupported(creds) {
				r.Off[j] = true
				r.On[j] = false
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
// sbxEgressReality greys the tiers the sbx backend cannot honour. sandboxSpec derives
// the Kit allowlist from the harness capabilities and the detected providers and never
// consults the tier, so "open" and "allowlist" produce an identical sandbox: the row
// would otherwise present a risk axis on which nothing moves. Only "review" still does
// something — it selects the docker backend — and gateReview already owns that.
//
// The option is greyed rather than removed, for the reason comingSoon exists: hiding it
// would misrepresent an unenforced tier as an unavailable one.
func sbxEgressReality(r choiceui.Row, sandboxOn bool) choiceui.Row {
	if !sandboxOn {
		return r
	}
	return comingSoon(r, "open", "open: sbx always enforces the Kit allowlist")
}

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

// sbxEnabled reports whether the sandbox backend may be selected at all.
//
// PROVEO_SBX=off pins the docker+egress path, and it exists because nothing else
// can say that non-interactively. The add-on is default-ON and only a
// remembered or prompted answer turns it off — but a headless run no longer
// reads the choice cache (that cache seeds a prompt, and there is none), so
// without this knob a host with sbx installed has no way to run on docker: not
// for a script, not for CI, and not for a test that needs to inspect the docker
// plan it is asserting against.
func sbxEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
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

// ansiSeq matches the escape sequences a TUI writes constantly. They are stripped
// from the retained tail because a replayed cursor-move is noise, and a tail full of
// escapes is harder to read than no tail at all.
var ansiSeq = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\\\\-_]")

// agentStdio picks the writers handed to the sandbox agent. On a terminal it
// returns out and err UNCHANGED — os/exec only passes the child the real tty when
// the field still holds that *os.File, and any wrapper silently becomes a pipe.
// Off a terminal the stream is already redirected, so teeing costs nothing and the
// tail becomes the only record of what the agent said.
func agentStdio(out, err io.Writer, tty bool) (io.Writer, io.Writer, *tailWriter) {
	tail := newTailWriter(24)
	if tty {
		// The child keeps the real terminal: os/exec hands it a tty only when the
		// field holds an *os.File, and an io.MultiWriter substitutes a pipe — the
		// agent then cannot read the window size and draws one character per line.
		//
		// The tail is still returned. It stays empty on a bare exec, but the pty
		// proxy can fill it from the master side at no cost to the child, which is
		// what finally gives an INTERACTIVE run a record of its last words. See
		// ptyproxy.Proxy.OutTap.
		return out, err, tail
	}
	return io.MultiWriter(out, tail), io.MultiWriter(err, tail), tail
}

// tailWriter keeps the last n non-empty lines written through it. It is a tee, not
// a filter: everything still reaches the terminal untouched, and only the retained
// copy is cleaned up for replay.
//
// The mutex is load-bearing. os/exec copies stdout and stderr on separate
// goroutines, and both are teed into ONE tailWriter, so two Writes interleave on the
// same buffer: one goroutine computes an index, the other reslices under it, and the
// next reslice panics with bounds out of range. That is not a rare race — it took
// out the first real run this shipped in.
type tailWriter struct {
	mu    sync.Mutex
	n     int
	buf   []byte
	lines []string
}

func newTailWriter(n int) *tailWriter { return &tailWriter{n: n} }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexAny(t.buf, "\n\r")
		if i < 0 {
			break
		}
		t.push(string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	// A TUI may never emit a newline; do not let the pending fragment grow forever.
	if len(t.buf) > 8192 {
		t.push(string(t.buf))
		t.buf = nil
	}
	return len(p), nil
}

func (t *tailWriter) push(line string) {
	line = strings.TrimSpace(ansiSeq.ReplaceAllString(line, ""))
	line = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, line)
	if line == "" {
		return
	}
	if n := len(t.lines); n > 0 && t.lines[n-1] == line {
		return // a redrawn status line is one line, not many
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
}

// Lines returns the retained tail, including any unterminated fragment.
func (t *tailWriter) Lines() []string {
	if t == nil {
		return nil // interactive run: the terminal was handed over instead
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > 0 {
		t.push(string(t.buf))
		t.buf = nil
	}
	return t.lines
}

// agentTranscriptDirs are where a harness writes its session transcripts inside the
// mounted proveo home, relative to it. Keyed by target: each CLI chooses its own.
var agentTranscriptDirs = map[string][]string{
	"claudecode": {".claude/projects"},
}

// agentTranscript names the session transcript written during this run, if any.
//
// It is better evidence than a captured tail. A tail holds what reached the
// terminal; the transcript holds what the agent received and said — which is where
// "Credit balance is too low" appeared after a run showed nothing but a stopped
// sandbox.
//
// The window is closed at BOTH ends, and the upper bound is not defensive tidiness.
// The copy-out that brings a transcript to the host runs on the failure path, and
// on a stopped sandbox `sbx exec` restarts the VM to do it — which re-runs the
// seed. That restart writes files of its own. One run reported
// "66523790-…jsonl" as what the agent said: zero bytes, created at 15:12:13.694,
// seventeen seconds AFTER the agent it was supposed to be quoting had already died,
// belonging to no session that ever ran (the run's own session was 1a972241, and it
// has no transcript anywhere). Ranking by mtime alone, it was the newest file in
// the home and therefore won.
//
// Empty is not evidence either. A zero-byte file satisfied "a transcript exists",
// which suppressed the credential hint written for exactly the failure that leaves
// nothing behind — so the one run that most needed an explanation got a path to an
// empty file instead.
func agentTranscript(target, homeRoot string, since, until time.Time) string {
	if homeRoot == "" {
		return ""
	}
	newest, newestAt := "", since
	for _, rel := range agentTranscriptDirs[target] {
		root := filepath.Join(homeRoot, rel)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil //nolint:nilerr // an unreadable home is not a run failure
			}
			fi, err := d.Info()
			switch {
			case err != nil, fi.Size() == 0:
				return nil
			case !fi.ModTime().After(newestAt):
				return nil
			case !until.IsZero() && fi.ModTime().After(until):
				return nil // written after the run ended: the harvest's, not the run's
			}
			newest, newestAt = p, fi.ModTime()
			return nil
		})
	}
	return newest
}

// subscriptionLoginFiles are where a completed login persists inside the proveo
// home the sandbox mounts. Keyed by target because each harness stores its own.
//
// An operator may authenticate on the HOST before launching — `claude setup-token`,
// or a normal interactive login — and that credential reaches the container because
// HOME points at the proveo home, which is mounted. When it is present it is the
// operator's answer, and proveo must not hand sbx a competing API key that its
// proxy would inject instead (which is how a subscription run silently billed per
// token).
//
// cursor is absent deliberately: its manifest declares only CURSOR_API_KEY, and its
// CLI keeps no credential file we have established — ~/.cursor/cli-config.json is
// configuration, not auth. Add it here once the location is known rather than
// guessing, or a missing file will read as "no login" forever.
var subscriptionLoginFiles = map[string][]string{
	"claudecode": {".claude/.credentials.json"},
}

// effectiveAuthVar is the credential the run should authenticate with: the row the
// operator answered if there was one, otherwise the manifest's own secret when a
// host login is already sitting in the proveo home.
func effectiveAuthVar(man manifest.Manifest, target, chosen, homeRoot string) string {
	if v := strings.TrimSpace(chosen); v != "" && v != authVarLogin {
		return v
	}
	if !hasPersistedLogin(target, homeRoot) {
		return ""
	}
	for _, e := range man.Env {
		if e.Secret {
			return e.Name // the harness's declared subscription credential
		}
	}
	return ""
}

// hasPersistedLogin reports whether a login already exists for target under the
// proveo home. It is the half of the auth picture MissingEnv cannot see: the env
// var is one way to be authenticated and the credential file is the other.
func hasPersistedLogin(target, homeRoot string) bool {
	ok, _ := persistedLogin(target, homeRoot)
	return ok
}

// persistedLogin reports whether the credential can still authenticate, and
// whether it must be refreshed first.
//
// Existence is NOT validity, and the difference is the whole point of the guard
// this feeds. A dead credential is a file of exactly the same size as a live
// one, so stat-ing it let an expired login satisfy the check that exists to stop
// a run the agent cannot complete — it reaches the login prompt, exits, and the
// sandbox stops with it, which surfaces as an infrastructure failure rather than
// as "your login ran out".
func persistedLogin(target, homeRoot string) (ok, needsRefresh bool) {
	if homeRoot == "" {
		return false, false
	}
	for _, rel := range subscriptionLoginFiles[target] {
		if usable, refresh := loginUsable(filepath.Join(homeRoot, rel), time.Now()); usable {
			return true, refresh
		}
	}
	return false, false
}

// oauthCredential is the shape claudecode persists. The stamps say whether the
// credential is live; the tokens are read only for EMPTINESS, never for value —
// a stamp cannot tell you the token beside it was taken away.
type oauthCredential struct {
	ClaudeAIOauth struct {
		// Pointers because ABSENT and ZERO mean opposite things here: a missing
		// field is a shape we do not understand (assume usable), while an explicit
		// 0 is a stamp that has been cleared — a token deliberately invalidated,
		// which is exactly the state a failed refresh leaves behind.
		ExpiresAt             *int64 `json:"expiresAt"`             // ms since epoch
		RefreshTokenExpiresAt *int64 `json:"refreshTokenExpiresAt"` // ms since epoch
		// Pointers for the same reason, and it is load-bearing: logging in on
		// macOS moves the credential to the KEYCHAIN and rewrites this file with
		// its tokens blanked, leaving every stamp in place. An absent field is a
		// shape we do not judge; an explicit "" is a token that was removed.
		AccessToken  *string `json:"accessToken"`
		RefreshToken *string `json:"refreshToken"`
	} `json:"claudeAiOauth"`
}

// tokenCleared reports whether a token field is present and blank — removed,
// rather than belonging to a shape this check does not recognise.
func tokenCleared(tok *string) bool { return tok != nil && *tok == "" }

// loginUsable classifies one credential file.
//
// An UNRECOGNISED file is reported usable on purpose. This check exists to catch
// a credential that is definitely dead; inferring "expired" from a shape we
// cannot parse would refuse runs that work, and a false refusal is worse than
// the failure it was meant to prevent — the operator has no way to argue with it.
func loginUsable(path string, now time.Time) (usable, needsRefresh bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return false, false
	}
	var c oauthCredential
	if json.Unmarshal(b, &c) != nil {
		return true, false // presence is all this file lets us honestly assert
	}
	o := &c.ClaudeAIOauth
	// A file with its tokens BLANKED is not a login, however live the stamps look.
	// This is the ordinary state of the proveo home on macOS: `claude` there writes
	// the credential to the Keychain and leaves the file with "" tokens and every
	// stamp intact. Reading the stamps alone reported that as a login needing a
	// refresh, so the run announced itself authenticated, suppressed the env token
	// that would have worked, and the agent died with nothing to send.
	if tokenCleared(o.AccessToken) {
		return false, false
	}
	if o.ExpiresAt == nil {
		return true, false
	}
	if now.Before(time.UnixMilli(*o.ExpiresAt)) {
		return true, false
	}
	// A stale access token beside a LIVE refresh token is still a login: the
	// agent renews it at startup with no prompt, which is exactly what happened
	// on the run that reported "Login expired" and then carried on working. A
	// CLEARED stamp (0) lands here too, which is correct — it needs the same
	// refresh, and saying so is what tells the operator why the agent stalled.
	//
	// A blanked refresh token is the exception: there is nothing to renew WITH, so
	// the stamp describes a renewal that cannot happen.
	if r := o.RefreshTokenExpiresAt; r != nil && !tokenCleared(o.RefreshToken) && now.Before(time.UnixMilli(*r)) {
		return true, true
	}
	return false, false
}

// stdinFilterEnabled reports whether the terminal-report filter is on.
// PROVEO_STDIN_FILTER=off restores byte-for-byte passthrough, for an operator
// whose terminal is well-behaved and who wants nothing between it and the agent.
func stdinFilterEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_STDIN_FILTER"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

// stdinTracer opens the file named by PROVEO_TRACE_STDIN and returns a tap for
// ptyproxy plus its closer. Returns (nil, no-op) when the variable is unset, so
// the default path is byte-for-byte the untraced one.
//
// Each read is one line: when it arrived, how many bytes, whether the filter
// forwarded it, the printable rendering, and the hex. Control bytes are what
// matter here — a terminal answering a device-attributes query looks like text
// in a transcript and like an escape sequence in the hex, and only the second
// tells the truth.
func stdinTracer(path string) (func([]byte, bool), func()) {
	if strings.TrimSpace(path) == "" {
		return nil, func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		ui.Warnf("stdin trace: cannot open %s (%v); continuing untraced", path, err)
		return nil, func() {}
	}
	ui.Iconf("🔎", "stdin trace → %s (every byte the agent is sent)", path)
	fmt.Fprintf(f, "=== trace opened %s ===\n", time.Now().Format(time.RFC3339Nano))
	var mu sync.Mutex
	tap := func(b []byte, forwarded bool) {
		// Copy before returning: the pump reuses its buffer on the next read.
		c := append([]byte(nil), b...)
		mu.Lock()
		defer mu.Unlock()
		verdict := "sent"
		if !forwarded {
			verdict = "DROPPED"
		}
		fmt.Fprintf(f, "%s  n=%-4d %-7s %-40q %s\n",
			time.Now().Format("15:04:05.000"), len(c), verdict, renderControl(c), hexBytes(c))
	}
	return tap, func() {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(f, "=== trace closed %s ===\n", time.Now().Format(time.RFC3339Nano))
		_ = f.Close()
	}
}

// renderControl makes escape sequences legible without hiding them: ESC becomes
// a caret form rather than vanishing into %q's \x1b, which is what makes a
// terminal's reply recognisable at a glance.
func renderControl(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == 0x1b:
			sb.WriteString("<ESC>")
		case c == '\r':
			sb.WriteString("<CR>")
		case c == '\n':
			sb.WriteString("<LF>")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, "<%02X>", c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func hexBytes(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02x", c)
	}
	return sb.String()
}

// imagePosture records WHICH image a run took, because "proveo/claudecode:latest"
// alone does not distinguish the build under test from a registry artifact that may
// be weeks older.
func imagePosture(ref string) string {
	if maintain.RefTag(ref) == maintain.LocalTag {
		return ref + " (local build)"
	}
	return ref + " (published)"
}

// dockerImageCreated reports when the host built or pulled an image.
var dockerImageCreated = func(ref string) (time.Time, bool) {
	out, err := exec.Command("docker", "image", "inspect", ref, "--format", "{{.Created}}").Output()
	if err != nil {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// reportImageChoice resolves :latest against a local build and NAMES the winner.
// The naming is the point: an image silently resolving to a published artifact
// instead of the build under test is invisible until something behaves like code
// nobody wrote, and reading it off one header line is the whole difference.
func reportImageChoice(ref string) string {
	chosen, isLocal := maintain.ResolveImage(ref, dockerImageCreated)
	if isLocal {
		ui.Iconf("📦", "image: %s (local build — newer than the published tag)", chosen)
	}
	return chosen
}

// authSuppressor reports which auth vars must NOT be injected for this run.
//
// Two ways of being authenticated compete, and they do not merge: an env token
// OVERRIDES a credential file rather than sitting beside it. So when the operator's
// login already exists as a file under the mounted proveo home, that file IS the
// credential and every auth var for its provider is suppressed — otherwise a
// setup-token exported on the host authenticates the run as the API while the
// mounted subscription login goes unread, which is what "Claude API" on a
// subscription run was reporting. When the operator answered the auth row instead,
// their answer stands and only its alternatives are suppressed.
func authSuppressor(man manifest.Manifest, target, chosen, homeRoot string) func(string) bool {
	chosen = strings.TrimSpace(chosen)
	// The login is the credential — either named outright, or the only answer when
	// the operator gave none and one exists. Then NO variable for its providers may
	// be injected: an env token supersedes the file rather than joining it.
	// A login only outranks an env token while it can still AUTHENTICATE. A file
	// that needs a renewal this backend cannot perform is not the credential — it
	// is a dead one, and suppressing a working token in its favour leaves the run
	// with no credential at all. That is not hypothetical: on macOS the host's
	// login lives in the KEYCHAIN, so the file under the proveo home is written
	// only by the container and can go stale with no host-side way to refresh it.
	usableLogin, staleLogin := persistedLogin(target, homeRoot)
	usableLogin = usableLogin && !staleLogin
	if chosen == authVarLogin || (chosen == "" && usableLogin) {
		// Scoped to the providers the login actually authenticates — read off the
		// harness's own declared secrets. Scoping it to the manifest's capabilities
		// instead reached too far: a manifest that declares none allows every
		// provider, so an anthropic login would have suppressed the openai key too
		// and quietly removed reach the harness legitimately has.
		owned := map[string]bool{}
		for _, e := range man.Env {
			if e.Secret {
				if prov := providerOfKeyVar(e.Name); prov != "" {
					owned[prov] = true
				}
			}
		}
		return func(k string) bool {
			prov := providerOfKeyVar(k)
			return prov != "" && owned[prov]
		}
	}
	auth := effectiveAuthVar(man, target, chosen, homeRoot)
	return func(k string) bool { return losesToChosenAuth(k, auth) }
}

// losesToChosenAuth reports whether key var k is a rejected alternative to the auth
// var the operator picked. Only vars of the SAME provider compete: an anthropic
// choice says nothing about openai, and dropping an unrelated key would silently
// remove reach the harness legitimately has.
func losesToChosenAuth(k, chosen string) bool {
	chosen = strings.TrimSpace(chosen)
	if chosen == "" || k == chosen {
		return false
	}
	prov := providerOfKeyVar(chosen)
	if prov == "" || providerOfKeyVar(k) != prov {
		return false
	}
	for _, alt := range provider.AuthVars(prov) {
		if alt == k {
			return true // same provider, different auth: the operator chose the other
		}
	}
	return false
}

func buildHeader(man manifest.Manifest, lookup func(string) string, roles provider.Roles, bridges provider.BridgeTable, repoRoot, inputDir, homeRoot string) []string {
	if inputDir == "" {
		inputDir = repoRoot
	}
	h := gitHeader(repoRoot)
	h = append(h, choiceui.EnvHeader(loadedSecretNames(man, lookup), loadedSettings(man, lookup))...)
	h = append(h, workspaceHeader(man, inputDir, repoRoot, homeRoot, glyphModeFrom(lookup))...)
	if line := rolesLine(bridges, man.Name, roles); line != "" {
		h = append(h, "llms:     "+line)
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

// glyphMode selects what decorates the lsp: row. Nerd is the default and ASCII is the
// fallback an operator selects when their font stops at the Powerline range, because a
// terminal offers no way to ask whether its font carries a codepoint.
type glyphMode int

const (
	glyphsNerd glyphMode = iota // default
	glyphsASCII
	glyphsOff
)

// lspNerd maps an LSP server to its Nerd Font devicon — per-language identity, since
// a logo is recognised before it is read.
var lspNerd = map[string]string{
	"gopls":                      "\ue627",
	"typescript-language-server": "\ue628",
	"pyright-langserver":         "\ue73c",
	"bash-language-server":       "\ue795",
	"docker-langserver":          "\ue7b0",
	"yaml-language-server":       "\ue60b",
}

// lspASCII maps an LSP server to a category marker, deliberately coarser than the
// devicons: an ASCII symbol has to be decoded rather than recognised, so per-language
// distinctions it cannot carry are not worth inventing. Every marker is padded to two
// columns so the server names stay aligned whichever category they fall in.
//
// The set avoids "[", "(" and ">" on purpose. choiceui.go draws "[x] "/"[ ] " for
// checkboxes, "(•) "/"( ) " for radios, and "◀ riskier"/"safer ▶" for the legend, so
// a "[]" before a server name would read as an unchecked add-on rather than a glyph.
var lspASCII = map[string]string{
	"gopls":                      "<>",
	"typescript-language-server": "<>",
	"pyright-langserver":         "<>",
	"bash-language-server":       "$ ",
	"docker-langserver":          "# ",
	"yaml-language-server":       "{}",
}

// glyphModeFrom reads PROVEO_GLYPHS through lookup, so a project .env can set it once
// per repo. Unset means nerd; an unrecognised value also means nerd rather than off,
// so a typo degrades to the default rather than silently stripping the row.
func glyphModeFrom(lookup func(string) string) glyphMode {
	switch strings.ToLower(strings.TrimSpace(lookup("PROVEO_GLYPHS"))) {
	case "ascii":
		return glyphsASCII
	case "off", "0", "false", "no", "none":
		return glyphsOff
	}
	return glyphsNerd
}

// withGlyphs prefixes each label with its glyph. Nerd mode falls back to the ASCII
// category marker for a server with no devicon, so adding a language to lspMarkers
// degrades to a category rather than to a ragged column. A server in neither table is
// left bare: an invented placeholder would read as a language nobody has.
func withGlyphs(labels []string, mode glyphMode) []string {
	if mode == glyphsOff {
		return labels
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		g, ok := "", false
		if mode == glyphsNerd {
			g, ok = lspNerd[l]
		}
		if !ok {
			g, ok = lspASCII[l]
		}
		if !ok {
			out = append(out, l)
			continue
		}
		out = append(out, g+" "+l)
	}
	return out
}

func ToolingLabels() []string {
	out := make([]string, 0, len(toolingMarkers))
	for _, m := range toolingMarkers {
		out = append(out, m.Label)
	}
	return out
}

func workspaceHeader(man manifest.Manifest, inputDir, repoRoot, homeRoot string, mode glyphMode) []string {
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
		out = append(out, "lsp:      "+strings.Join(withGlyphs(labels, mode), "  "))
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

// workspaceBinds is every bind sbx can take as a positional workspace.
//
// sbx accepts several workspaces and mounts each at its own HOST path, so the
// workspace, the output dir, a --data-dir and proveo home all travel: their
// container path was never load-bearing on its own. Home comes with a condition —
// HOME has to be repointed at the host path, which sbxHome does below — because
// the harness finds its config through HOME rather than through /proveo-home
// literally.
//
// What cannot travel is a bind NESTED under home: the gh config sits at
// /proveo-home/.config/gh on docker, and as its own positional workspace it would
// land at its own host path instead, nowhere the harness looks. It is dropped
// rather than mounted somewhere useless — the same conclusion
// PROVEO_MOUNT_GH_CONFIG=0 reaches deliberately.
// sbxStateHome reports the host path the run publishes for resume state, or ""
// when this run has no proveo home to persist into.
func sbxStateHome(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, sbx.StateHomeVar+"="); ok {
			return v
		}
	}
	return ""
}

// sbxRun executes one sbx invocation and returns its combined output. Injectable
// so the copy-out below is testable without a daemon.
func sbxRun(args ...string) (string, error) {
	out, err := exec.Command(sbx.Binary, args...).CombinedOutput()
	return string(out), err
}

// saveSandboxState copies the sandbox-local resume state — transcripts included —
// into the mounted proveo home.
//
// Both exits need it, for different reasons. On the way out of a SUCCESSFUL run it
// has to happen before teardown, because `sbx rm` takes the volumes with it. On a
// FAILED run it is the only way the transcript reaches the host at all: the copy-out
// used to live on the success path alone, so `agentTranscript` searched a home the
// failed session had never written to and reported "no evidence" every time.
//
// No-op rather than an error when there is nothing to do: a docker run keeps its
// home on the host and needs no copy, and a run that died before sbx created
// anything has no volumes to read.
func saveSandboxState(name string, env []string, exists bool, run func(...string) (string, error)) (string, error) {
	if name == "" || !exists || sbxStateHome(env) == "" {
		return "", nil
	}
	return run(sbx.SaveStateArgs(name)...)
}

// keptSandboxLines is what proveo says about a failed run after the evidence
// channels have had their turn: how to look inside the sandbox, how to clean it up,
// and where the run's own transcript is.
//
// The run log is named HERE and not only at startup. It holds every line the run
// printed — the resolved posture, the credential warnings — and by the time an agent
// dies those have scrolled off a terminal nobody redirected. The macOS run whose
// login file had blanked tokens said so in its twelfth line and was diagnosed from
// scrollback that no longer existed.
func keptSandboxLines(name, runLog string) []string {
	lines := []string{fmt.Sprintf(
		"sandbox %s kept for diagnosis (the run failed) — `sbx exec %s -- sh`, then `sbx rm --force %s`",
		name, name, name)}
	if strings.TrimSpace(runLog) != "" {
		lines = append(lines,
			fmt.Sprintf("every line this run printed, posture and warnings included: %s", runLog))
	}
	return lines
}

func workspaceBinds(mounts []sbx.Mount) []sbx.Mount {
	var out []sbx.Mount
	seen := map[string]bool{}
	for _, m := range mounts {
		if strings.HasPrefix(m.Container, proveohome.ContainerHome+"/") {
			continue // nested under home; its nesting cannot be reproduced
		}
		// A workspace is a DIRECTORY. docker binds a single file happily — the
		// project .env arrives as one, and the credential policy masks it with a
		// /dev/null bind — but sbx refuses: "workspace path exists but is not a
		// directory". Those binds are dropped, which also means a project .env does
		// not reach an sbx sandbox; on the brokered tiers it was being masked away
		// anyway, and the credentials it would have carried are declared in the Kit.
		if fi, err := os.Stat(m.Host); err != nil || !fi.IsDir() {
			continue
		}
		// A linked worktree contributes only <repo>/.git, and sbx MIRRORS host
		// paths rather than remapping them — so the <repo> parent has to be
		// synthesized inside the VM, where the runtime creates it root-owned. It
		// then looks exactly like the operator's repo root and is not one: it is an
		// empty stub nobody can write to. Tools that resolve the main repository
		// directory (nx keeps its shared task DB there) die on a permission error
		// naming a path the operator knows is theirs, which sends the diagnosis
		// looking for a phantom root-promotion.
		//
		// Mounting the PARENT makes it a real bind, uid-mapped like every other
		// workspace and writable from the host and the agent alike. Measured on the
		// same worktree: root:root and writes DENIED before, claude:claude and
		// writes OK after, with `git rev-parse` still resolving the worktree.
		//
		// Docker keeps the narrow mount: it remaps container paths freely, so no
		// parent is ever synthesized and nothing is shadowed.
		if m.Container == workspace.ContainerGitCommonDir && filepath.Base(m.Host) == ".git" {
			m.Host = filepath.Dir(m.Host)
		}
		if seen[m.Host] {
			continue // the repo root may already be a workspace in its own right
		}
		seen[m.Host] = true
		out = append(out, m)
	}
	return out
}

// sbxHome rewrites HOME from the container path docker used to the HOST path sbx
// mounts proveo home at, so ~/.claude and the resume state persist across runs
// instead of landing in a sandbox-local directory that dies with the VM.
func sbxHome(env []string, mounts []sbx.Mount) []string {
	host := ""
	for _, m := range mounts {
		if m.Container == proveohome.ContainerHome {
			host = m.Host
			break
		}
	}
	if host == "" {
		return env
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "PROVEO_HOME=") {
			continue
		}
		out = append(out, e)
	}
	// NEITHER is set on this backend, and the deletion is the fix.
	//
	// Redirecting HOME to the mounted host path was written for the docker backend,
	// where proveo runs the agent as the HOST's uid and the image's passwd entry is
	// therefore wrong. sbx does not do that: it runs its own agent user, uid 1000,
	// and its built-in kit both mounts the session volumes under that user's home
	// and has its credential proxy write `.credentials.json` there. Pointing HOME
	// somewhere else did not move those — it ORPHANED them, so the agent read a
	// stale mounted credential instead of the live proxy-managed one and reported
	// "Not logged in" (tests/e2e/ladder_test.go, rung 3).
	//
	// PROVEO_HOME existed only to survive that redirect: sbx resets HOME from
	// /etc/passwd for setup.startup, so a seed reading $HOME wrote to a different
	// place than the agent read. With no redirect there is no divergence — the seed
	// and the agent both resolve the image's home, which the Dockerfiles now pin to
	// /home/agent to match sbx's kit.
	//
	// The proveo home stays MOUNTED (it is a workspace bind), so nothing that reads
	// it by path is lost; only the env redirect goes.
	//
	// What the redirect DID buy is resume: with HOME on the mounted host dir, the
	// agent's transcripts survived the run. Without it they land in the volumes sbx
	// mounts under the image's home, and teardown removes "VM + images + volumes",
	// so `--resume` had nothing to offer on the next run. PROVEO_STATE_HOME hands
	// the seed and the teardown the host path to copy those directories to and
	// from, which restores persistence WITHOUT moving HOME away from the credential
	// the sbx proxy writes there.
	return append(out, sbx.StateHomeVar+"="+host)
}

// firstHost is the host path of the first bind, which is where sbx puts the cwd.
func firstHost(mounts []sbx.Mount) string {
	if len(mounts) == 0 {
		return ""
	}
	return mounts[0].Host
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
	// memory is the -m limit for the sandbox, resolved by the caller so that
	// sandboxSpec stays pure and --print renders the same argv the run executes.
	memory string
	// homeRoot is the proveo home, passed in for the same reason: a host login
	// living there decides which credential the run authenticates with, and
	// sandboxSpec must not reach for the real filesystem to find that out.
	homeRoot string
	// runLog is where every line this run printed was tee'd. Carried in so the
	// failure path can name it: by the time an agent dies the posture and the
	// credential warnings have scrolled off, and the operator has no way back to
	// them from a terminal they did not redirect.
	runLog string
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
	suppressedAuth := authSuppressor(in.man, p.target, p.authVar, in.homeRoot)
	// A suppressed credential is OMITTED, not stated as empty — the same as the
	// docker path does.
	//
	// This used to render `-e VAR=` on the theory that sbx's global secret store
	// would otherwise inject a stale value in front of the mounted login. Probed
	// against sbx v0.39.0 with both ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN
	// sitting in the global store and no -e flag: both arrive UNSET. sbx does not
	// export service secrets as environment variables — its proxy attaches them as
	// headers, and it materialises the harness credential as ~/.claude/.credentials.json
	// instead. So there was nothing in front of the login to override.
	//
	// The empty value was not merely useless, it was the failure. An agent reads a
	// SET variable as a chosen credential regardless of its value: claudecode ranks
	// ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN above the login it would find on
	// disk, so a blank one occupied the slot the login needed and asked an unattended
	// run to approve a key that could authenticate nothing.
	for _, e := range in.man.Env {
		if !e.Secret {
			continue
		}
		if suppressedAuth(e.Name) {
			continue
		}
		if forwards {
			addForward(e.Name)
			continue
		}
		addSecret(e.Name)
	}
	// Filtered by what the harness declares it can USE, the same way detection is
	// on the docker path. Unfiltered, a claudecode sandbox asked sbx for cursor,
	// google, openai and xai credentials too — and sbx shows that list to the
	// operator for approval, so an over-declared Kit is not merely untidy: it asks
	// consent for reach the harness has no way to exercise.
	for _, k := range provider.KeyVars() {
		if !in.man.Capabilities.AllowsProvider(providerOfKeyVar(k)) {
			continue
		}
		// A provider with two ways to authenticate gets only the one the operator
		// chose. Handing sbx both put an API key and a subscription token in the
		// same store, and its proxy injected the key — so a subscription run
		// silently billed per token and the auth row the operator answered was
		// decided for them somewhere they could not see.
		if suppressedAuth(k) {
			continue
		}
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

	// --shell selects sbx's OWN shell agent rather than substituting a command: the
	// built-in agent owns the launch, so a trailing "bash" reached the harness's
	// agent as an argument and the shell never opened.
	command, agent := p.extra, sbx.BuiltinAgent(p.target)
	if p.shell {
		command, agent = nil, sbx.ShellAgent
	}
	cfg := sbx.RunConfig{
		Name: in.sid,
		// The Kit path is resolved HERE, not at write time, so --print renders the
		// same argv the run executes. Deriving it only inside runSandbox left the
		// dry run silently missing --kit — the posture the Kit carries (allowlist,
		// brokered credentials) was invisible in exactly the output an operator
		// inspects to check that posture.
		KitDir: filepath.Join(in.egDir, "sbx", "kit"),
		Image:  p.image,
		// sbx would otherwise size the sandbox from HOST memory, which on any VM-backed
		// daemon can exceed the VM itself — see sbx.MemoryLimit.
		Memory: in.memory,
		// Clone mode is why a bind-mounted node_modules stops ping-ponging: the
		// clone carries only TRACKED files, so a host-built (macOS) tree never
		// arrives, the seed installs Linux deps into the clone, and the operator's
		// checkout is never written. Measured: origin points at
		// /run/sandbox/source and node_modules is absent from the workspace.
		Clone: p.clone,
		// A kind: sandbox Kit DEFINES an agent, and sbx requires the positional to
		// match its name — "agent name X does not match agent kit name Y". So the
		// two are one value, and there is no separate agent to declare: an earlier
		// attempt to name one in the manifest (sbxAgent: claude) was refused for
		// exactly this reason. It is namespaced because sbx also refuses to let a
		// Kit shadow a built-in agent, and `cursor` and `opencode` are both built
		// in — see sbx.BuiltinAgent.
		Agent: agent,
		// Only the WORKSPACE binds survive onto an sbx run: it mounts each
		// positional path at its own host path, so a bind with a container-side
		// target — proveo home at /proveo-home, the gh config under it — has no way
		// to be expressed. They are dropped rather than silently mounted somewhere
		// else, and PROVEO_WORKDIR below tells the harness where it actually landed.
		Mounts: workspaceBinds(mounts),
		// The bridge is applied HERE, not in the container: its output goes into the
		// Kit's environment block, where it reaches the agent. Left to a setup hook
		// it would be computed in a process the agent never inherits.
		// The decline is declared twice on purpose. The Kit's environment block is
		// the posture proveo publishes; the -e flag is what sandboxd applies when
		// it CREATES the sandbox, which is the moment its own injection happens.
		// Either alone may lose that race; neither is harmful when the other wins.
		Env: declineMCPGateway(sbxHome(append(append(env, resolvedModelEnv(in)...),
			"PROVEO_WORKDIR="+firstHost(workspaceBinds(mounts))), mounts)),
		Command: command,
	}
	// The Kit is a MIXIN composed onto sbx's own agent: it declares no agent, no
	// image and no credentials. The image arrives via -t, the agent is sbx's, and
	// credentials belong to the built-in agent's kit — repeating a service there is
	// rejected outright ("defined in both"), and its proxy already injects.
	kit := sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          p.target + "-posture",
		DisplayName:   "proveo posture (" + p.target + ")",
		Description:   "Reachability, host-resolved environment and the seed step for a proveo run.",
		Permissions:   sbx.KitPermissions{Network: sbx.KitNet{Allow: allow}},
		Environment:   &sbx.KitEnv{Variables: withMCPGatewayPolicy(kitEnvVars(cfg.Env))},
		Setup:         &sbx.KitSetup{Startup: []sbx.KitCommand{sbx.SeedCommand(p.target)}},
	}
	return cfg, kit, secrets
}

// workspacePosture says whether the agent edits the operator's checkout or a
// private clone of it. It is a posture row because it changes WHERE the work
// lands, which is the one thing an operator must not have to guess.
func workspacePosture(clone bool) string {
	if clone {
		return "in-container clone (--clone) — the checkout is never written; changes come back with `git fetch`"
	}
	return "mounted checkout — the agent edits it directly"
}

// mcpGatewayPosture reports what the run does about sbx's MCP gateway.
//
// It earns a posture row because it is a CAPABILITY the agent gets, decided
// outside the Kit: sbx registers the gateway from its own agent kit, into a HOME
// proveo mounts read-write. A posture that lists reachable hosts and credentials
// but not an MCP server the agent is told to call is not describing the run.
func mcpGatewayPosture(sandbox bool) string {
	switch {
	case !sandbox:
		return "n/a (docker backend)"
	case sbxMCPGatewayAllowed():
		return "allowed (PROVEO_SBX_MCP=on) — sbx registers it into the proveo home, --scope user"
	}
	return "declined (" + MCPGatewayVar + " empty; PROVEO_SBX_MCP=on to allow)"
}

// MCPGatewayVar is the variable sbx's built-in agent kits gate their MCP
// registration on. Their step is `[ -n "$MCP_GATEWAY_URL" ] || exit 0`, so
// declaring it EMPTY is the supported way to decline — no patching, no race with
// a step that runs inside the sandbox.
const MCPGatewayVar = "MCP_GATEWAY_URL"

// withMCPGatewayPolicy declines sbx's MCP gateway unless the operator asks for it.
//
// The registration is `claude mcp add mcp-gateway <url> --scope user`, run inside
// the sandbox by sbx's own kit. USER scope is the problem: proveo points HOME at
// the proveo home and mounts it read-write, so an entry meant to live and die with
// a disposable sandbox lands in the operator's real home and outlives every run
// that wrote it. Nothing in `proveo run --print` ever named it, which makes it a
// third source of agent capability beside the Kit and the credential store.
//
// Declining costs nothing today: `sbx mcp ls` registers no servers, so the
// gateway aggregates an empty set. PROVEO_SBX_MCP=on restores it for an operator
// who has registered servers of their own and wants the agent to reach them.
func withMCPGatewayPolicy(vars map[string]string) map[string]string {
	if sbxMCPGatewayAllowed() {
		return vars
	}
	if vars == nil {
		vars = map[string]string{}
	}
	// Written explicitly rather than through kitEnvVars, which drops empty values:
	// here the empty string IS the instruction.
	vars[MCPGatewayVar] = ""
	return vars
}

// declineMCPGateway adds the empty MCP_GATEWAY_URL to the -e set. kitEnvVars
// drops empty values, so this pair never duplicates the Kit's own declaration.
func declineMCPGateway(env []string) []string {
	if sbxMCPGatewayAllowed() {
		return env
	}
	for _, e := range env {
		if strings.HasPrefix(e, MCPGatewayVar+"=") {
			return env
		}
	}
	return append(env, MCPGatewayVar+"=")
}

func sbxMCPGatewayAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX_MCP"))) {
	case "on", "1", "yes", "true", "enable", "enabled":
		return true
	}
	return false
}

// resolvedModelEnv is the model bridge, applied host-side, as KEY=VALUE pairs.
func resolvedModelEnv(in runSandboxInput) []string {
	var out []string
	for k, v := range in.params.bridges.ResolvedEnv(in.params.target, in.params.roles) {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // a Kit is written to disk and diffed; order must not churn
	return out
}

// kitEnvVars turns the resolved KEY=VALUE pairs into the Kit's environment block.
//
// Only pairs that already carry a value are declared: a bare NAME in cfg.Env means
// "forward whatever the host holds", which is a -e concern and cannot be written
// into a spec file. Resolving here is the point of the design — the agent receives
// ANTHROPIC_MODEL decided, rather than a table and a bridge to recompute it.
func kitEnvVars(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// kitCredentials declares proveo's broker as the Kit's credential policy: one
// entry per secret actually being injected, proxy-managed so the value never
// enters the VM, and an inject rule per host naming the header it may ride.
//
// It is derived from the SECRETS, not from the detected providers, because those
// are not the same set: a manifest-declared credential (claudecode's
// CLAUDE_CODE_OAUTH_TOKEN) is injected host-side whether or not provider
// detection saw it. Keying on detection left the Kit silently empty for exactly
// that case — declaring no brokering while proveo went on to broker.
//
// No value is written into the Kit. The env var names it and the header says
// where it may go; the secret itself travels only over `sbx secret set` on stdin.
// Nothing is declared under --credentials forward, where the agent holds its own
// key and there is no brokering to describe.
func kitCredentials(secrets [][2]string, forwards bool) []sbx.KitCredential {
	if forwards {
		return nil
	}
	var out []sbx.KitCredential
	seen := map[string]bool{}
	for _, kv := range secrets {
		svc, opt, ok := providerForAuthVar(kv[0])
		// No header means nothing to attach a credential to: a signed-request
		// provider (bedrock, vertex) or one authenticating by query parameter cannot
		// be expressed as an injected header.
		if !ok || opt.Header == "" {
			continue
		}
		// One entry per SERVICE. anthropic accepts two credentials
		// (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY) and both can be present, but
		// a service is one identity — declaring it twice leaves which one wins up to
		// sbx's map order. First wins, and the order is meaningful: manifest-declared
		// secrets come before the detected provider keys, so the credential the
		// harness actually announces is the one that stands.
		if seen[svc] {
			continue
		}
		seen[svc] = true
		format := "%s"
		if opt.Bearer {
			format = "Bearer %s"
		}
		e, _ := provider.Lookup(svc)
		var inject []sbx.KitInject
		for _, h := range e.Hosts {
			inject = append(inject, sbx.KitInject{Domain: h, Header: opt.Header, Format: format})
		}
		out = append(out, sbx.KitCredential{
			Service: svc,
			APIKey:  sbx.KitAPIKey{Name: kv[0], ProxyManaged: true, Inject: inject},
		})
	}
	return out
}

// providerOfKeyVar names the provider a credential env var belongs to, or the var
// itself when the registry does not claim it — so an unknown key is judged by the
// capability list rather than silently dropped.
func providerOfKeyVar(envVar string) string {
	if name, _, ok := providerForAuthVar(envVar); ok {
		return name
	}
	return envVar
}

// providerForAuthVar maps a credential env var back to the provider that accepts
// it and the auth option describing how — the reverse of the registry's usual
// direction, and what lets a Kit name a service for a secret proveo only knows by
// variable name.
func providerForAuthVar(envVar string) (string, provider.AuthOption, bool) {
	for _, n := range provider.Names() {
		e, ok := provider.Lookup(n)
		if !ok {
			continue
		}
		for _, a := range e.Auth {
			if strings.EqualFold(a.EnvVar, envVar) {
				return e.Name, a, true
			}
		}
	}
	return "", provider.AuthOption{}, false
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
	// sbx has no `logs` command and the sandbox may be gone by the time anyone
	// asks, so the only reliable copy of what the agent said is the one taken as
	// it streams past. "agent exited with code 137" is sbx's auto-stop code,
	// arriving 30s after the agent itself exited; the tail is what actually
	// explains the run — a credit-balance error, a login prompt, a stack trace.
	//
	// The tail can only be taken when stdout is ALREADY redirected. os/exec gives
	// the child the real terminal only when the field holds an *os.File; wrapping
	// it in an io.MultiWriter substitutes a pipe, so the agent sees no tty, cannot
	// read the window size, and draws its TUI one character per line. Interactive
	// runs keep the terminal and forgo the tail — the operator is watching anyway.
	stdout, stderr, tail := agentStdio(os.Stdout, os.Stderr, isWriterTTY(os.Stdout))
	// PROVEO_TRACE_STDIN answers the one question a transcript cannot: an agent
	// that exits on its own, having answered prompts the operator never typed,
	// is being driven by SOMETHING on stdin — a multiplexer replying to a
	// terminal query, a wrapper feeding a script, a stray paste. The transcript
	// records what the agent RECEIVED; only a tap records what arrived.
	traceIn, stopTrace := stdinTracer(os.Getenv("PROVEO_TRACE_STDIN"))
	defer stopTrace()
	// An interactive run goes through the pty proxy so the operator's terminal is
	// FILTERED before it reaches the agent. sbx drives the agent through its
	// agent-session API, where the far end reads input as a prompt stream rather
	// than as terminal input — so a report with no query to belong to (a
	// multiplexer answering Device Attributes twice, an unsolicited focus event)
	// is not ignored there, it is enqueued and answered as a user message nobody
	// typed. See ptyproxy.inputFilter.
	//
	// The proxy gives the child its own pty, so this costs the agent nothing:
	// wrapping os.Stdin instead would make os/exec substitute a pipe and take the
	// terminal away entirely.
	filtered := ptyproxy.Usable(os.Stdin, os.Stdout) && stdinFilterEnabled()
	run := func() error {
		c := exec.Command(sbx.Binary, args...)
		if filtered || (traceIn != nil && ptyproxy.Usable(os.Stdin, os.Stdout)) {
			px := ptyproxy.New(os.Stdin, os.Stdout)
			px.DisableFilter = !filtered
			px.Tap = traceIn
			// The child's output reaches the terminal through the pty master either
			// way, so copying it into the tail costs the agent nothing and gives an
			// interactive run the one channel it never had. Without this, the run
			// that dies at its prompt has no tail AND no transcript, and "sandbox
			// was stopped" is the entire report.
			if tail != nil {
				px.OutTap = func(b []byte) { _, _ = tail.Write(b) }
			}
			return px.Run(c)
		}
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
		return c.Run()
	}
	startedAt := time.Now()
	runErr := run()
	// Closed the moment the child returned, BEFORE anything proveo does to collect
	// evidence. Everything written after this instant belongs to the harvest, not
	// to the run — including a restarted sandbox's own fresh session files.
	endedAt := time.Now()
	if runErr != nil && !sbx.Exists(cfg.Name) {
		// The template sbx holds has gone bad. Hand the image over again and try
		// once more; if it fails the same way the second time, it is not the
		// template and the original error stands.
		if err := sbx.ReloadTemplate(cfg.Image, func(f string, a ...any) {
			ui.Iconf("📦", f, a...)
		}); err == nil {
			ui.Iconf("↻", "the sandbox did not start — retrying once on a freshly loaded template")
			runErr = run()
			endedAt = time.Now()
		}
	}
	defer func() {
		// A sandbox that failed is the only copy of why. sbx has no `logs` command, so
		// force-removing it here destroyed the evidence for two consecutive 137s before
		// anyone could read it. Keep it, and say how to look and how to clean up.
		if runErr != nil {
			said := false
			if lines := tail.Lines(); len(lines) > 0 {
				said = true
				fmt.Fprintf(os.Stderr, "\n── last output from the agent ──\n")
				for _, l := range lines {
					fmt.Fprintf(os.Stderr, "  %s\n", l)
				}
				fmt.Fprintf(os.Stderr, "───────────────────────────────\n")
			}
			// The transcript is written into sandbox-LOCAL volumes and only the
			// SUCCESS path below copied it out, so the one channel that explains a
			// failed sbx run was structurally always empty — agentTranscript read a
			// host home the session had never reached. Copy it out first.
			//
			// On a STOPPED sandbox this starts the VM to do it, which re-runs the
			// seed and so re-runs `proveo_sync_state restore`. The claim that used to
			// stand here — that this is safe because restore is `cp -a` with no
			// deletes — was wrong twice over, and both halves cost real data:
			//
			//   restore then ran CONCURRENTLY with this save, in the opposite
			//   direction over the same files, and `cp` truncates its destination in
			//   place. Seven of the operator's transcripts were rewritten short
			//   inside one second, at exact 256 KiB multiples, cut mid-JSON, with the
			//   short copy propagated to both sides. `proveo_sync_state` now holds a
			//   lock and renames into place, so neither direction can leave a partial
			//   file (packages/lib/entrypoint-lib.sh).
			//
			//   and "its mtime postdates startedAt" is satisfied by the restart's OWN
			//   artifacts. A zero-byte session file the restart created 17s after the
			//   agent died was reported as what the agent said. agentTranscript is
			//   now bounded above by endedAt and rejects empty files.
			//
			// Whether the harvest had to wake a stopped sandbox is worth saying out
			// loud: it is why the home holds files newer than the run.
			restarted := !sbx.Running(cfg.Name)
			_, _ = saveSandboxState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), sbxRun)
			if t := agentTranscript(in.params.target, in.homeRoot, startedAt, endedAt); t != "" {
				said = true
				ui.Iconf("📄", "what the agent actually said is in %s", t)
			} else if restarted && sbx.Exists(cfg.Name) {
				ui.Iconf("📄", "no transcript from this run — the sandbox had already stopped, "+
					"so state was copied out after it ended and anything newer than the run is the harvest's own")
			}
			// Both evidence channels came up empty, which is its own diagnosis: the
			// agent died before its first turn. Naming the credential here is what
			// stops the operator spending the next hour inside `sbx exec` looking
			// for a cause that is on the host.
			if !said {
				if hint := noCredentialHint(in.man, in.params.target, in.homeRoot, cfg.Env, secrets,
					sbx.StoredSecretNames(), in.lookup); len(hint) > 0 {
					ui.Iconf("🔑", "%s", hint[0])
					for _, l := range hint[1:] {
						fmt.Fprintf(os.Stderr, "%s\n", l)
					}
				}
			}
			kept := keptSandboxLines(cfg.Name, in.runLog)
			ui.Warnf("%s", kept[0])
			for _, l := range kept[1:] {
				ui.Iconf("📝", "%s", l)
			}
			return
		}
		// Resume state lives in per-sandbox volumes that the next line destroys, so
		// it has to come out FIRST. Best-effort by design: a sandbox that never
		// started has nothing to copy, and losing yesterday's transcripts is not a
		// reason to leave a VM running.
		if out, err := saveSandboxState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), sbxRun); err != nil {
			ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
		}
		rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput()
		// A run that never got as far as creating the sandbox has nothing to tear
		// down, and reporting that as a teardown failure sends the operator after
		// the wrong thing — the real error is the one `sbx run` already printed.
		if rmErr != nil && !sbx.NotFound(string(rmOut)) {
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
// willSandbox adds the ADD-ON to the free function's host-capability test.
// Unticking "docker (sandbox)" and PROVEO_SBX=off reach the same backend, but only
// the env var was visible to willSandbox — so an unticked run reported "enforced by
// sbx" while actually running on docker+egress, naming a boundary holder that was
// not there.
func (p *runParams) willSandbox(man manifest.Manifest) bool {
	return willSandbox(man) && p.sandboxAddonOn()
}

// willSandbox reports whether this run will take the sbx backend, using the same
// conditions as the branch that selects it. It is consulted before that branch so the
// transcript and the prompt describe the backend that will actually run.
func willSandbox(man manifest.Manifest) bool {
	if !man.IsSbx() || !sbxEnabled() {
		return false
	}
	ok, _ := sbx.Available()
	return ok
}

// enforcedBy names who holds the boundary. proveo's tiers describe its own Squid and
// MITM sidecars; under sbx neither runs, and the Kit hands enforcement to the sandbox
// runtime instead. Printing a tier without naming the enforcer reads as a proveo
// guarantee that proveo is not, on that backend, in a position to make.
func enforcedBy(sandboxed bool) string {
	if sandboxed {
		return "sbx — Kit network allowlist + credential proxy (proveo runs no Squid or MITM)"
	}
	return "proveo — squid + mitmproxy sidecars on the session network"
}

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

// rolesLine renders the role assignment for the transcript and the prompt header,
// naming the slots the harness will actually fill rather than the role vars the
// operator happened to set. A harness that reads two of the three must not be shown
// advertising the third: the header is the only pre-launch view of that resolution.
func rolesLine(bridges provider.BridgeTable, harness string, r provider.Roles) string {
	var parts []string
	for _, s := range bridges.EffectiveSlots(harness, r) {
		if p := provider.ModelProvider(s.Model); p != "" {
			parts = append(parts, fmt.Sprintf("%s=%s (%s)", s.Name, s.Model, p))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", s.Name, s.Model))
	}
	return strings.Join(parts, "  ")
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
		// Attached is not the same as ready: the daemon inside the sidecar starts
		// seconds after the container does, and the agent must not be handed a shell
		// whose first `docker` call races it.
		if err := dindSidecar.WaitReady(dind.ExecRunner{}, 90*time.Second, nil, nil); err != nil {
			return err
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

// isWriterTTY reports whether w is an *os.File attached to a terminal. A child
// process only inherits the terminal when the exec field holds that same *os.File,
// so this also answers "may I wrap this writer?".
func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

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
