// Command proveo is the harness CLI. It composes the shared
// hardened docker-run builder (internal/runner), the egress orchestration
// (internal/egress), and provider detection (internal/provider) into one typed
// binary — replacing the triplicated Bash run logic. Distributed as a single
// checksummed static binary (see .goreleaser.yaml, dist/install.sh).
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
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/reviewgate"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

// version is overridden at build time via -ldflags "-X main.version=...".
// Dev installs (mise run build-cli) stamp "dev@<git-short-sha>"; releases use the semver tag.
var version = "dev"

// loadManifests reads the harness manifests embedded in the binary, or a
// working-tree defs dir when PROVEO_DEFS_DIR is set (dev iteration without a
// rebuild).
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
		Use: "proveo",
		// Tagline is rendered once, dimmed, under the banner by WriteBrandBanner
		// (see SetHelpFunc below); leaving Short empty avoids printing it twice.
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		Args:          cobra.NoArgs,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true, // no `proveo completion` — not a consumer surface
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
		// The agent's own non-zero exit is not a proveo error — propagate its code
		// verbatim, without the "error:" prefix (C6). Only the agent's: a failed
		// helper subprocess (docker pull, build.sh) wraps an ExitError too and
		// must still be reported, so execAgent marks its exit with a named type.
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
			// Cursor exception: its inference is vendor-pinned and its TLS to Cursor's
			// backend is not MITM-brokerable, so only broker mode (which forwards the
			// real key to the container) authenticates it. Default cursor to broker
			// unless the user explicitly chose a mode.
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
	authVar                                                                     string
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

func doRun(p runParams) error {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	sid := fmt.Sprintf("proveo-%d-%d", time.Now().Unix(), os.Getpid())
	egDir := filepath.Join(stateDir(), "egress", sid)

	// The harness's mount model comes from its manifest (workspace layout).
	man, err := manifestForTarget(p.target)
	if err != nil {
		return err
	}

	// Cursor CLI has no local-model path — all inference transits Cursor's backend.
	if p.target == "cursor" && p.localModel != "" {
		return fmt.Errorf("cursor has no --local-model path (inference is vendor-pinned); unset it or use another harness")
	}
	// Cursor authenticates only in broker mode: cursor-agent's TLS to api2.cursor.sh
	// is not MITM-brokerable, so firewall hands it the "proveo-brokered" sentinel and
	// proxy withholds the key — either way cursor-agent reports "invalid API key".
	// broker mode forwards the real CURSOR_API_KEY to the container. (This branch only
	// fires when a non-broker mode was explicitly chosen; cursor defaults to broker.)

	// Monorepo scope: the repo root gives full git/workspace context.
	start := orWD(p.input)
	ws := workspace.Resolve(start)
	repoRoot := start
	if ws.IsRepo {
		repoRoot = ws.Root
	}
	if p.output == "" {
		p.output = filepath.Join(repoRoot, "reports")
	}

	// Sub-project scope: an explicit --scope, else an interactive picker when in a
	// monorepo, on a TTY, and not just printing. PROVEO_WIZARD=off suppresses the
	// picker (same switch as the env wizard) so a PTY-driven, non-interactive
	// caller — the agent E2E suite, CI — never blocks on a keypress.
	subScope := strings.Trim(p.scope, "/")
	if subScope == "" && !p.printOnly && isStdinTTY() && wizardEnabled() && ws.IsRepo {
		if projs := workspace.DiscoverProjects(repoRoot); len(projs) > 0 {
			subScope = pickProject(projs, os.Stdin, os.Stderr)
		}
	}
	if subScope != "" {
		ui.Iconf("📂", "scope: %s", subScope)
	}

	// Build the mount plan from the manifest's workspace model (embedded whole —
	// no field-by-field copy to keep in sync).
	wsSpec := workspace.MountSpec{Workspace: man.Workspace, OutputDir: p.output, EgressMode: p.mode, Credentials: p.credentials}
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

	// Host-side .env for broker ingestion (never mounted into the agent in
	// proxy/firewall). Explicit PROVEO_EGRESS_ENV_FILE wins. Resolve before
	// missing-env prompts so keys present only in a project .env are visible.
	invocationWD, _ := os.Getwd()
	hostEnvFile := strings.TrimSpace(os.Getenv("PROVEO_EGRESS_ENV_FILE"))
	if hostEnvFile == "" {
		hostEnvFile = workspace.EnvFileSource(invocationWD, wsSpec.InputDir, wsSpec.RepoRoot)
	}
	lookup := providerLookup(hostEnvFile)

	settingsRoot := proveohome.Root(os.Getenv)
	settings, err := agentsettings.Load(settingsRoot)
	if err != nil {
		ui.Warnf("%v — continuing without cached settings", err)
	}
	// Settle the axes against the def BEFORE anything reads them: the prompt must
	// pre-select and gate from what this harness actually supports, not from the
	// flag defaults. Re-applied after the prompt so a chosen value is re-validated.
	if err := p.applyCapabilities(man.Capabilities); err != nil {
		return err
	}
	// A cached answer seeds the selection; it does not replace the prompt. Silently
	// entering a remembered tier hides the security posture of the run — the
	// operator must always see, and be able to change, what they are launching.
	if cached, ok := settings.Lookup(p.target, man.Capabilities); ok {
		if !p.modeSet && cached.Egress != "" {
			p.mode = cached.Egress
		}
		if !p.credsSet && cached.Credentials != "" {
			p.credentials = cached.Credentials
		}
		p.addons = cached.Addons
		if p.authVar == "" {
			p.authVar = cached.AuthVar
		}
	}
	if !p.printOnly && wizardEnabled() && isStdinTTY() {
		if err := p.promptChoices(man, lookup, gitRootOrEmpty(ws, repoRoot), settingsRoot); err != nil {
			return err
		}
	}
	if err := p.applyCapabilities(man.Capabilities); err != nil {
		return err
	}
	if !p.printOnly {
		settings.Remember(p.target, man.Capabilities, agentsettings.Choice{
			Egress: p.mode, Credentials: p.credentialsOrDefault(), Addons: p.addons, AuthVar: p.authVar,
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

	// Optional add-ons, offered before the env wizard as a Tab-multiselect on a TTY
	// (the wizard may attach a bufio.Reader to stdin, which would starve an interactive
	// picker). Browser is an image variant; DinD is a sidecar attached to the same
	// base image. Non-interactively: -browser target + PROVEO_DIND (below).
	dindScope := wsSpec.InputDir
	if dindScope == "" {
		dindScope = start
	}
	wantDind := false
	browserImage := man.Images[p.target+"-browser"]                                                     // the -browser variant, if this harness has one
	dindOfferable := man.Dind && dind.ModeSupported(p.mode) && dind.CredentialsSupported(p.credentials) // DinD needs broker egress (see ModeSupported)
	if hasAddon(p.addons, "browser") && browserImage != "" {
		p.image = browserImage
		ui.Iconf("🌐", "variant: browser → %s", browserImage)
	}
	if hasAddon(p.addons, "dind") && dindOfferable {
		wantDind = true
		ui.Iconf("🐳", "sidecar: DinD (same image)")
	}
	if len(p.addons) == 0 && !p.printOnly {
		// Non-interactive: DinD stays env-gated (PROVEO_DIND); the browser variant is
		// selected by running `proveo run <target>-browser` explicitly.
		wantDind = dindOfferable && dind.ShouldStart(man.Dind, dindScope, false, nil)
	}
	// Warn (rather than silently no-op) if DinD was explicitly requested in a mode
	// that cannot expose a daemon without defeating egress enforcement.
	if man.Dind && !dind.ModeSupported(p.mode) && dind.EnvEnabled() && dind.ScopeHasDockerfiles(dindScope) {
		ui.Warnf("PROVEO_DIND is set but --egress-mode %s cannot expose a Docker daemon to the agent without defeating egress enforcement; skipping DinD (use --egress-mode broker for in-container Docker)", p.mode)
	}

	// Declared-but-missing env: subscription harnesses warn and let the agent
	// handle login (no ahead-of-time key prompt). Other harnesses prompt on a
	// TTY (DinD-prompt-style wizard), else warn. Runs before provider detection
	// so a prompted key feeds the broker + forwarding.
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

	mounts, planWorkdir := wsSpec.Plan()
	if planWorkdir != "" {
		workdir = planWorkdir
	}

	// Durable proveo home (~/.proveo): session transcripts + seeded policy, not
	// host IDE credentials. Scrubs deny-listed auth files before each run.
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

	// Credential broker: gated by brokerProvider (firewall + a resolved provider +
	// not disabled). Vendor-pinned harnesses (manifest provider:) win over the
	// "exactly one detected key" rule so a multi-provider .env does not block
	// cursor when CURSOR_API_KEY lives only in the host env. Write secrets up front.
	detected := filterProviders(provider.Detect(lookup), man.Capabilities)
	providerName := brokerProvider(p.forwards(), man, detected, lookup, brokerEnabled())
	if reason := brokerOffReason(p.forwards(), providerName, detected, brokerEnabled()); reason != "" {
		ui.Warnf("%s", reason)
	}
	var brokerFile string
	if providerName != "" {
		if p.printOnly {
			brokerFile = filepath.Join(egDir, "inject", "broker.env") // path only in dry-run
		} else if f, err := writeBrokerEnv(filepath.Join(egDir, "inject"), lookup); err == nil {
			brokerFile = f
		} else {
			ui.Warnf("broker secret file: %v", err)
		}
	}

	// Local-model sidecar is an opt-in add-on: resolve its (config-driven) host
	// models dir only when --local-model is requested. Alongside it, decide where
	// inference runs: the host's GPU Ollama (macOS, where a container can't reach
	// the GPU) or a sidecar, GPU-accelerated when the Docker host supports it.
	var modelsDir string
	var hostOllama, ollamaGPU bool
	if p.localModel != "" {
		modelsDir = ollamaModelsDir()
		hostOllama = preferHostOllama()
		ollamaGPU = sidecarOllamaGPU()
	}

	// Declared env: bare `-e NAME` for non-secrets. Secrets: broker forwards real
	// value; firewall injects sentinel + PROVEO_CREDENTIAL_BROKER_KEYS; proxy withholds.
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
	// Non-secret model/UI preferences, forwarded by value from the host env or the
	// host-side .env. The entrypoints bridge these into tool-specific vars
	// (OPENCODE_MODEL, CECLI_MODEL, ANTHROPIC_MODEL, …); they must arrive as env
	// because .env is masked in proxy/firewall and unmounted in the input-output
	// layout. --local-model overrides them: its -e pairs land in plan.AgentArgs,
	// which docker applies after Env. The shared baseline plus whatever this
	// harness declares in `config:` (secrets belong in `env:`, which is brokered).
	for _, k := range configVarsFor(man) {
		if v := strings.TrimSpace(lookup(k)); v != "" {
			env = append(env, k+"="+v)
			warnUnknownModel(k, v, p.localModel)
		}
	}
	env = append(env, gitidentity.Resolve(os.Getenv, nil).EnvPairs()...)
	env = append(env, homePlan.Env...)

	var dindSidecar *dind.Sidecar

	// On macOS/Windows, pid_max is probed from the Docker VM using a local
	// image. Ensure the agent image exists first so the probe has something
	// to run (--pull=never); full sidecar preflight still happens below.
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
		reviewSocket: reviewSocket,
		modelsDir:    modelsDir, provider: providerName, brokerFile: brokerFile,
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
		// Point the agent's docker client at the daemon; the reachability
		// mechanism depends on where the agent runs. Default bridge (broker
		// without a local model): a legacy --link. User-defined network (broker
		// with a local model): the daemon is attached to that network by alias
		// once it exists (execWithEgress).
		agent.ExtraArgs = append(append([]string(nil), agent.ExtraArgs...), sc.EnvArgs()...)
		if plan.AgentNetwork == "" {
			agent.ExtraArgs = append(agent.ExtraArgs, sc.LinkArgs()...)
		}
		// Teardown (incl. on Ctrl-C, which skips defers) is owned by the exec path
		// below — execWithEgress for the lifecycle path, the signal-safe branch just
		// below for the bare path — so it survives signals. One of the two always
		// runs after a successful Start (no early return in between).
	}
	warnMountedSecrets(wsSpec.InputDir, p.mode, lookup)
	// Before launch, not after: a hint about how to authenticate is useless once the
	// operator is already in a session running on the other credential.
	if len(authMissingAtStart) > 0 {
		printSubscriptionAuthHints(man, authMissingAtStart, os.Stderr)
	}
	runErr := func() error {
		if !needsLifecycle(plan) {
			if dindSidecar == nil {
				return execAgentWithProxy(agent, reviewProxy)
			}
			// DinD is running but there's no egress topology (broker without a local
			// model): no lifecycle teardown, but the privileged sidecar must still come
			// down on SIGINT/SIGTERM. A single once-guarded cleanup backs both the defer
			// and the signal handler — Cleanup is not safe to call concurrently.
			var once sync.Once
			cleanup := func() { once.Do(func() { dindSidecar.Cleanup(dind.ExecRunner{}) }) }
			defer cleanup()
			stopSig := onSignalCleanup(cleanup)
			defer stopSig()
			return execAgentWithProxy(agent, reviewProxy)
		}
		// The broker pins ONE provider for injection; the allowlist covers every one
		// the harness can reach. Collapsing the allowlist onto the pin would block a
		// session that switches model mid-run to another provider — reach and
		// injection are different questions. A vendor-locked harness (manifest
		// provider:) is the exception: its allowlist is deliberately just its vendor.
		squidProviders := detected
		if strings.TrimSpace(man.Provider) != "" && providerName != "" {
			squidProviders = []string{providerName}
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

// joinDomains merges the operator's extra domains with the harness's own
// infrastructure endpoints into the single space-separated list the egress layer
// consumes for both the Squid ACL and the policy's write allowlist.
func joinDomains(env string, hosts []string) string {
	parts := strings.Fields(env)
	parts = append(parts, hosts...)
	return strings.Join(parts, " ")
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
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("run %s — confirm or change this run", p.target),
		Header: buildHeader(man, lookup, repoRoot, p.input, homeRoot),
		Rows: applicableRows(
			reviewAvailability(axisRow("egress", egress.Modes(), man.Capabilities.Egress, p.mode)),
			axisRow("credentials", egress.CredentialModes(), man.Capabilities.Credentials, p.credentialsOrDefault()),
		),
	}
	// A provider that accepts more than one credential (anthropic: API key OR
	// subscription token) would otherwise have the choice made by the declared
	// order. Offer it only when the operator actually holds more than one.
	if auth := availableAuthVars(man, lookup); len(auth) > 1 {
		form.Rows = append(form.Rows, applicableRows(
			axisRow("auth", auth, auth, orElseFirst(p.authVar, auth)),
		)...)
	}
	if addons := addonOptions(man); len(addons) > 0 {
		on := make([]bool, len(addons))
		for i, a := range addons {
			on[i] = hasAddon(p.addons, a)
		}
		form.Rows = append(form.Rows, applicableRows(choiceui.Row{
			Label: "add-ons", Options: addons, Multi: true, On: on,
		})...)
		form.OnChange = func(f *choiceui.Form) { gateAddons(f, p.mode, p.credentialsOrDefault()) }
		form.OnChange(form)
	}

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
	p.addons = form.Selections("add-ons")
	if v := form.Selection("auth"); v != "" {
		p.authVar = v
	}
	return nil
}

// availableAuthVars lists the credentials the operator holds for the provider this
// run will pin. Fewer than two means there is nothing to decide.
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

func gateAddons(f *choiceui.Form, tierFallback, credsFallback string) {
	// A filtered-out axis has no row to read, so fall back to the value already
	// resolved from the manifest. Without this, hiding cursor's single-option rows
	// would make the gate see empty strings and wrongly disable dind on the one
	// harness whose fixed tier can host it.
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
			if opt == "dind" && (!dind.ModeSupported(tier) || !dind.CredentialsSupported(creds)) {
				r.Off[j] = true
				r.Reason = "dind needs egress open + credentials forward"
			}
		}
	}
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
// The gate is a unix socket bind-mounted into the inspector, and a Linux container
// cannot connect() to one across a Docker Desktop / OrbStack mount — so the tier
// needs a Linux host AND a local daemon. A remote DOCKER_HOST puts the mount on
// another machine, where the socket the gate created does not exist at all.
func reviewSupported(getenv func(string) string) (ok bool, why string) {
	if runtime.GOOS != "linux" {
		return false, "linux only"
	}
	if h := strings.TrimSpace(getenv("DOCKER_HOST")); h != "" && !strings.HasPrefix(h, "unix://") {
		return false, "needs a local docker daemon"
	}
	return true, ""
}

// reviewAvailability greys the review option out on hosts whose transport cannot
// carry the gate, naming the actual reason rather than a blanket 'coming soon'.
func reviewAvailability(r choiceui.Row) choiceui.Row {
	if ok, why := reviewSupported(os.Getenv); !ok {
		return comingSoon(r, "review", "review: "+why)
	}
	return r
}

// comingSoon greys an option out and moves the selection off it. The row still
// shows the option with its reason: hiding it would misrepresent the tier as
// nonexistent rather than unfinished. The --egress-mode flag still accepts it, so
// development can drive the tier while operators cannot pick it by accident.
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

// applicableRows drops axes with nothing to decide. A single-option row is not a
// choice, and rendering it invites the operator to reason about a control that
// cannot move — cursor, pinned to open+forward, would otherwise show two inert
// rows. Axes where a choice DOES exist keep every option, with unavailable ones
// greyed and explained (see gateAddons).
func applicableRows(rows ...choiceui.Row) []choiceui.Row {
	out := make([]choiceui.Row, 0, len(rows))
	for _, r := range rows {
		if r.Multi || len(r.Options) > 1 {
			out = append(out, r)
		}
	}
	return out
}

func addonOptions(man manifest.Manifest) []string {
	var opts []string
	for target := range man.Images {
		if strings.HasSuffix(target, "-browser") {
			opts = append(opts, "browser")
			break
		}
	}
	if man.Dind {
		opts = append(opts, "dind")
	}
	return opts
}

// ghConfigMount binds the host's gh CLI config read-only so `gh` inside the
// container reuses the operator's existing session instead of asking for a token.
//
// A deliberate exception to keeping host credentials out of the container:
// hosts.yml holds an OAuth token the agent can read. Read-only prevents rewriting
// it, not reading it — the container boundary and the egress allowlist are what
// bound the damage. Opt out with PROVEO_MOUNT_GH_CONFIG=0.
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

func hasAddon(addons []string, name string) bool {
	for _, a := range addons {
		if a == name {
			return true
		}
	}
	return false
}

func buildHeader(man manifest.Manifest, lookup func(string) string, repoRoot, inputDir, homeRoot string) []string {
	if inputDir == "" {
		inputDir = repoRoot
	}
	h := gitHeader(repoRoot)
	h = append(h, choiceui.EnvHeader(loadedSecretNames(man, lookup), loadedSettings(man, lookup))...)
	return append(h, workspaceHeader(man, inputDir, repoRoot, homeRoot)...)
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
	// Only keys for providers this harness can actually use: claudecode declares
	// providers:[anthropic], so listing OPENAI/XAI/GEMINI implied it could reach
	// them. The auth row already filters correctly; this line did not.
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

// gitRootOrEmpty is the repository root regardless of workspace layout.
// wsSpec.RepoRoot is only populated for the app layout, so reading it made an
// input-output harness report a real repository as absent.
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
	{Label: "pyright", Names: []string{"pyproject.toml"}, Suffixes: []string{".py"}},
	{Label: "bash-language-server", Suffixes: []string{".sh"}},
	{Label: "dockerfile-language-server", Names: []string{"Dockerfile"}},
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

// brokerProvider returns the provider to broker for this run, or "" for none:
// firewall mode only (the sole mode whose MITM consumes it) and the broker not
// disabled. A manifest provider pin (vendor-pinned harness) is used when its
// detect key is present; otherwise exactly one detected provider is required.
func brokerProvider(forwards bool, man manifest.Manifest, detected []string, lookup func(string) string, brokerOn bool) string {
	if forwards || !brokerOn {
		return ""
	}
	if pin := strings.TrimSpace(man.Provider); pin != "" {
		e, ok := provider.Lookup(pin)
		if !ok {
			return ""
		}
		for _, v := range e.Detect {
			if strings.TrimSpace(lookup(v)) != "" {
				return pin
			}
		}
		return ""
	}
	if len(detected) == 1 {
		return detected[0]
	}
	return modelPinnedProvider(detected, lookup)
}

// modelPinnedProvider resolves an ambiguous multi-key host by asking the
// configured model which provider will actually be called. This is not a guess:
// a model id names its provider, so a host holding five keys but pointed at
// claude-opus-5 is unambiguously an anthropic run. Without it the broker refuses
// to pin, the agent gets the sentinel, and the provider answers 401/403 — which
// reads as a bad key rather than a proveo decision.
func modelPinnedProvider(detected []string, lookup func(string) string) string {
	inDetected := func(name string) bool {
		for _, d := range detected {
			if d == name {
				return true
			}
		}
		return false
	}
	var found string
	for _, key := range []string{"ARCHITECT_MODEL", "EDITOR_MODEL", "SMALL_MODEL"} {
		name := provider.ModelProvider(lookup(key))
		if name == "" || !inDetected(name) {
			continue
		}
		if found != "" && found != name {
			return "" // the models disagree; pinning either would be a guess
		}
		found = name
	}
	return found
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

func brokerOffReason(forwards bool, providerName string, detected []string, brokerOn bool) string {
	if forwards || providerName != "" || len(detected) == 0 {
		return ""
	}
	if !brokerOn {
		return fmt.Sprintf("credential broker disabled (PROVEO_CREDENTIAL_BROKER) — the agent gets the %q "+
			"sentinel, not a working key", entrypoint.DefaultSentinel)
	}
	return fmt.Sprintf("credential broker OFF: %d providers detected (%s) and the broker pins exactly one — "+
		"the agent will receive the %q sentinel and the provider will reject it. Unset the keys you are not using "+
		"for this run, or use --credentials forward to hand the real key to the container.",
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
	modelsDir, provider, brokerFile     string
	hostOllama, ollamaGPU               bool
	mounts                              []runner.Mount
	workdir                             string
	env                                 []string // declared env var names to forward (bare -e)
	providerDomains                     string
	squidImage, proxyImage, ollamaImage string
	pidsLimit                           int    // host/tier-resolved --pids-limit
	reviewSocket                        string // review tier: host path of the consent gate socket
}

// assemble builds the egress plan and the agent's docker-run config from resolved
// inputs. Pure (no env/filesystem/exec), so the topology + config wiring is
// unit-testable without Docker (D2).
func assemble(in assembleInput) (egress.Plan, runner.Config, error) {
	plan, err := egress.BuildPlan(egress.Options{
		Mode: in.params.mode, Credentials: in.params.credentials,
		SessionID: in.sid, AgentName: in.params.target, UID: in.uid, GID: in.gid,
		LocalModel: in.params.localModel, ModelsDir: in.modelsDir, Provider: in.provider, BrokerEnvFile: in.brokerFile,
		HostOllama: in.hostOllama, OllamaGPU: in.ollamaGPU,
		ProviderDomains: in.providerDomains,
		ReviewSocket:    in.reviewSocket,
		AuthVar:         in.params.authVar,
		ConfDir:         filepath.Join(in.egDir, "mitmproxy", "confdir"),
		FlowsDir:        filepath.Join(in.egDir, "mitmproxy", "flows"),
		SquidConfigDir:  filepath.Join(in.egDir, "squid", "config"),
		SquidLogDir:     filepath.Join(in.egDir, "squid", "logs"),
		// Image overrides (pin by digest in production; enforcement images are the trust root).
		SquidImage: in.squidImage, ProxyImage: in.proxyImage, OllamaImage: in.ollamaImage,
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

// execWithEgress stages only what the plan needs (C7), brings up the egress
// topology, waits for readiness, runs the agent, then tears the topology down —
// including on SIGINT/SIGTERM (C4), and removes the broker secret (C2).
func execWithEgress(plan egress.Plan, agent runner.Config, egDir string, providers []string, dindSidecar *dind.Sidecar, reviewProxy *ptyproxy.Proxy) error {
	r := egress.ExecRunner{Stderr: true}
	// rq is the quiet runner for best-effort teardown and readiness probes: those
	// legitimately hit transient docker errors — "No such container" once a --rm
	// sidecar has self-removed, or "connection refused" while Squid is still
	// binding :3128 — and we don't want docker's stderr leaking those alarming (but
	// expected) lines to the user's terminal. Apply keeps Stderr on: its failures
	// are real and must be seen.
	rq := egress.ExecRunner{}
	// Teardown containers/networks, the DinD sidecar, and the injected secret.
	// Registered before any staging so an early failure still tears down what
	// doRun already started (the DinD sidecar). Runs exactly once — on normal
	// return AND on SIGINT/SIGTERM (Go defers don't run when a signal ends the
	// process). Nil-safe when the run has no DinD sidecar.
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			plan.Teardown(rq)
			dindSidecar.Cleanup(dind.ExecRunner{})
			_ = os.RemoveAll(filepath.Join(egDir, "inject")) // broker.env must not outlive the run
		})
	}
	defer cleanup()
	// Installed before plan.Apply so a Ctrl-C during bring-up still cleans up.
	stopSig := onSignalCleanup(cleanup)
	defer stopSig()

	// Squid config + logs only when a Squid sidecar is present (proxy/firewall).
	if plan.UsesSquid {
		squidCfg := filepath.Join(egDir, "squid", "config")
		if err := egress.StageSquidConfig(proveo.SquidConfig, squidCfg, providers, os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS")); err != nil {
			return err
		}
		logs := filepath.Join(egDir, "squid", "logs")
		if err := os.MkdirAll(logs, 0o755); err != nil {
			return err
		}
		// Squid starts as root and drops to its own `proxy` user (uid 13) to write
		// access.log/cache.log. On Linux, bind mounts preserve host ownership, so a
		// dir owned by the invoking host uid at 0755 is NOT writable by uid 13 —
		// Squid then exits on startup, --rm marks it "marked for removal", and the
		// network-connect in Apply fails. Docker Desktop (macOS) makes bind mounts
		// permissive, which is why this only reproduces on Linux hosts. World-write
		// is acceptable for this per-user, per-session state dir (the egress-proxy
		// dirs stay 0755 because that sidecar runs as the host uid, which owns them).
		if err := os.Chmod(logs, 0o777); err != nil {
			return err
		}
	}
	// mitmproxy confdir/flows only in firewall mode (the only mode with the MITM).
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
	// Attach the DinD daemon to the agent's user-defined network so the agent
	// resolves `docker` by alias (broker + local-model case; the default-bridge
	// case is wired via --link in doRun). No-op when no sidecar / no network.
	if dindSidecar != nil && plan.AgentNetwork != "" {
		if err := dindSidecar.ConnectNetwork(dind.ExecRunner{}, plan.AgentNetwork); err != nil {
			return fmt.Errorf("attach dind to agent network: %w", err)
		}
	}
	// Squid is the internet-facing upstream both other modes transit; wait for it
	// to accept connections so the agent's first request doesn't race a cold Squid.
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
		if err := proxy.Overlay(func(io.Reader, io.Writer) error {
			var derr error
			allowed, derr = choiceui.Consent(tcell.NewScreen, host, port)
			return derr
		}); err != nil {
			// Denying is right, but silently denying every connection makes the tier
			// look broken rather than strict — say why once.
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

// onSignalCleanup runs cleanup then exits 130 on SIGINT/SIGTERM. Go does not run
// deferred functions when a signal terminates the process, so any out-of-band
// teardown (egress topology, injected secrets, a privileged DinD sidecar) needs
// this. cleanup must be once-guarded — it may fire from this goroutine while a
// normal-return defer runs it too. Returns a stop func (deregisters the handler)
// that the caller should defer.
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
// (host.docker.internal) instead of a sidecar. On macOS a Linux container can't
// reach the Metal GPU, so a sidecar runs CPU-only and is unusably slow; the host
// Ollama is GPU-accelerated. Honored only in broker mode (egress.buildBroker);
// the locked modes keep the isolated sidecar regardless. Override with
// PROVEO_LOCAL_MODEL_SIDECAR=1 to force the in-network sidecar even on macOS.
func preferHostOllama() bool {
	if os.Getenv("PROVEO_LOCAL_MODEL_SIDECAR") == "1" {
		return false
	}
	return runtime.GOOS == "darwin"
}

// sidecarOllamaGPU reports whether the Ollama sidecar can be GPU-accelerated:
// Linux with the NVIDIA container runtime registered in Docker (so `--gpus all`
// is valid). Adding the flag without the runtime would make the sidecar fail to
// start, so we probe `docker info` and only enable it on a positive match.
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
				// A note, not data: stdout stays empty so scripted callers see zero rows.
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

// isStdinTTY gates every interactive prompt (scope picker, env wizard). A real
// ioctl check, not a char-device stat: /dev/null is a character device too and
// must not count as interactive.
func isStdinTTY() bool { return isReaderTTY(os.Stdin) }

// isReaderTTY reports whether r is an *os.File attached to a terminal. The
// interactive fuzzy picker only makes sense on a real TTY; when r is piped or a
// test's strings.Reader we fall back to the numbered prompt (keeps tests + CI
// hermetic, since the fuzzy finder reads /dev/tty directly).
func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// pickProject prints a numbered menu and returns the chosen sub-project path
// ("" for the repo root / on any invalid or empty input).
// pickProject returns the chosen monorepo scope ("" = repo root). On a real TTY
// it shows an fzf-style arrow-key + type-to-filter picker; otherwise (pipe/test)
// it falls back to a numbered prompt driven by in.
func pickProject(projs []workspace.Project, in io.Reader, out io.Writer) string {
	if isReaderTTY(in) {
		return fuzzyPickProject(projs)
	}
	return pickProjectNumbered(projs, in, out)
}

// fuzzyPickProject shows an interactive finder with "<repo root>" as entry 0.
// Esc/Ctrl-C (ErrAbort) or any finder error resolves to repo root.
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

// warnMountedSecrets warns when the mounted workspace contains a .env while a
// provider key is present — in broker/open modes the agent reads it directly and
// nothing stops the key from leaving (S4). Skipped for proxy/firewall: there the
// egress DLP + header-strip blocks exfil even if the agent can still read .env.
func warnMountedSecrets(dir, mode string, lookup func(string) string) {
	if dir == "" {
		return
	}
	switch strings.ToLower(mode) {
	case "open", "allowlist", "review":
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

// hydrateProcessEnv copies a secret from lookup into the proveo process env when
// it is present in a host .env but not exported. Docker's bare `-e NAME` only
// forwards the client process environment, so broker mode needs this.
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
