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

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/dind"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
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
	var egressMode, localModel, input, output, scope, dataDir, imageOverride, resumeID string
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
			if target == "cursor" && !cmd.Flags().Changed("egress-mode") {
				egressMode = "broker"
			}
			if !egress.ValidMode(egressMode) {
				return fmt.Errorf("invalid --egress-mode %q (%s)", egressMode, strings.Join(egress.Modes(), "|"))
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
				target: target, image: image, mode: egressMode, localModel: localModel,
				input: input, output: output, scope: scope, dataDir: dataDir,
				shell: shellMode, printOnly: printOnly, extra: extra,
			})
		},
	}
	cmd.Flags().StringVar(&egressMode, "egress-mode", "firewall", strings.Join(egress.Modes(), "|")+" (default firewall: enforced egress + credential broker)")
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
	target, image, mode, localModel, input, output, scope, dataDir string
	shell, printOnly                                               bool
	extra                                                          []string
}

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
	if p.target == "cursor" && p.mode != "broker" {
		ui.Warnf("cursor + --egress-mode %s: the credential can't be brokered into cursor-agent's pinned TLS, so it will report \"invalid API key\" — use --egress-mode broker to forward your CURSOR_API_KEY", p.mode)
	}

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
	// monorepo, on a TTY, and not just printing.
	subScope := strings.Trim(p.scope, "/")
	if subScope == "" && !p.printOnly && isStdinTTY() && ws.IsRepo {
		if projs := workspace.DiscoverProjects(repoRoot); len(projs) > 0 {
			subScope = pickProject(projs, os.Stdin, os.Stderr)
		}
	}
	if subScope != "" {
		ui.Iconf("📂", "scope: %s", subScope)
	}

	// Build the mount plan from the manifest's workspace model (embedded whole —
	// no field-by-field copy to keep in sync).
	wsSpec := workspace.MountSpec{Workspace: man.Workspace, OutputDir: p.output, EgressMode: p.mode}
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

	// Optional add-ons, offered before the env wizard as a Tab-multiselect on a TTY
	// (the wizard may attach a bufio.Reader to stdin, which would starve an interactive
	// picker). Browser is an image variant; DinD is a sidecar attached to the same
	// base image. Non-interactively: -browser target + PROVEO_DIND (below).
	dindScope := wsSpec.InputDir
	if dindScope == "" {
		dindScope = start
	}
	wantDind := false
	browserImage := man.Images[p.target+"-browser"]         // the -browser variant, if this harness has one
	dindOfferable := man.Dind && dind.ModeSupported(p.mode) // DinD needs broker egress (see ModeSupported)
	if !p.printOnly && isStdinTTY() {
		var caps []capability
		if browserImage != "" && p.image != browserImage {
			caps = append(caps, capability{"browser", "browser variant — Playwright + Chromium image"})
		}
		if dindOfferable {
			caps = append(caps, capability{"dind", "DinD sidecar — same image + docker:dind"})
		}
		if len(caps) > 0 {
			sel, err := pickRunCapabilities(p.target, caps)
			if err != nil {
				return err
			}
			if sel["browser"] {
				p.image = browserImage
				ui.Iconf("🌐", "variant: browser → %s", browserImage)
			}
			if sel["dind"] {
				wantDind = true
				ui.Iconf("🐳", "sidecar: DinD (same image)")
			}
		}
	} else if !p.printOnly {
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

	// Credential broker: gated by brokerProvider (firewall + a resolved provider +
	// not disabled). Vendor-pinned harnesses (manifest provider:) win over the
	// "exactly one detected key" rule so a multi-provider .env does not block
	// cursor when CURSOR_API_KEY lives only in the host env. Write secrets up front.
	detected := provider.Detect(lookup)
	providerName := brokerProvider(p.mode, man, detected, lookup, brokerEnabled())
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
			switch p.mode {
			case "broker":
				env = append(env, e.Name)
				hydrateProcessEnv(e.Name, lookup)
			case "firewall":
				env = append(env, e.Name+"="+entrypoint.DefaultSentinel)
				brokerKeyNames = append(brokerKeyNames, e.Name)
			}
			continue
		}
		env = append(env, e.Name)
	}
	if p.mode == "firewall" {
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

	plan, agent, err := assemble(assembleInput{
		params: p, sid: sid, egDir: egDir, uid: uid, gid: gid,
		modelsDir: modelsDir, provider: providerName, brokerFile: brokerFile,
		hostOllama: hostOllama, ollamaGPU: ollamaGPU,
		mounts: mounts, workdir: workdir, env: env,
		providerDomains: os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"),
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
	runErr := func() error {
		if !needsLifecycle(plan) {
			if dindSidecar == nil {
				return execAgent(agent)
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
			return execAgent(agent)
		}
		squidProviders := detected
		if providerName != "" {
			squidProviders = []string{providerName}
		}
		return execWithEgress(plan, agent, egDir, squidProviders, dindSidecar)
	}()
	if len(authMissingAtStart) > 0 {
		printSubscriptionAuthHints(man, authMissingAtStart, os.Stderr)
	}
	return runErr
}

// brokerProvider returns the provider to broker for this run, or "" for none:
// firewall mode only (the sole mode whose MITM consumes it) and the broker not
// disabled. A manifest provider pin (vendor-pinned harness) is used when its
// detect key is present; otherwise exactly one detected provider is required.
func brokerProvider(mode string, man manifest.Manifest, detected []string, lookup func(string) string, brokerOn bool) string {
	if mode != "firewall" || !brokerOn {
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
	return ""
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
	pidsLimit                           int // host/tier-resolved --pids-limit
}

// assemble builds the egress plan and the agent's docker-run config from resolved
// inputs. Pure (no env/filesystem/exec), so the topology + config wiring is
// unit-testable without Docker (D2).
func assemble(in assembleInput) (egress.Plan, runner.Config, error) {
	plan, err := egress.BuildPlan(egress.Options{
		Mode: in.params.mode, SessionID: in.sid, AgentName: in.params.target, UID: in.uid, GID: in.gid,
		LocalModel: in.params.localModel, ModelsDir: in.modelsDir, Provider: in.provider, BrokerEnvFile: in.brokerFile,
		HostOllama: in.hostOllama, OllamaGPU: in.ollamaGPU,
		ProviderDomains: in.providerDomains,
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
func execWithEgress(plan egress.Plan, agent runner.Config, egDir string, providers []string, dindSidecar *dind.Sidecar) error {
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
	return execAgent(agent)
}

func execAgent(agent runner.Config) error {
	c := exec.Command("docker", runner.DockerRunArgs(agent)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return agentExitError{code: ee.ExitCode()}
	}
	return err
}

// onSignalCleanup runs cleanup then exits 130 on SIGINT/SIGTERM. Go does not run
// deferred functions when a signal terminates the process, so any out-of-band
// teardown (egress topology, injected secrets, a privileged DinD sidecar) needs
// this. cleanup must be once-guarded — it may fire from this goroutine while a
// normal-return defer runs it too. Returns a stop func (deregisters the handler)
// that the caller should defer.
func onSignalCleanup(cleanup func()) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
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

// capability is one optional harness add-on offered in the run picker.
type capability struct {
	key   string // "browser" | "dind"
	label string
}

// continueSentinel is the leading FindMulti row. It is always preselected so Enter
// confirms the current Tab set instead of selecting whatever row the cursor is on
// (FindMulti's empty-selection fallback). Index 0 is ignored when mapping keys.
const continueSentinel = "continue"

// capabilitySelection maps FindMulti indices onto capability keys. Index 0 is the
// continue sentinel and is never a selected capability.
func capabilitySelection(caps []capability, idxs []int) map[string]bool {
	sel := map[string]bool{}
	for _, i := range idxs {
		if i <= 0 || i > len(caps) {
			continue
		}
		sel[caps[i-1].key] = true
	}
	return sel
}

// pickRunCapabilities shows optional add-ons as an arrow list: Tab toggles, Enter
// always continues with the Tab selection (including none). A leading preselected
// "continue" sentinel keeps FindMulti from treating the cursor row as a selection
// when nothing was Tabbed. Esc/Ctrl-C aborts to no add-ons.
func pickRunCapabilities(target string, caps []capability) (map[string]bool, error) {
	labels := make([]string, 0, len(caps)+1)
	labels = append(labels, continueSentinel)
	for _, c := range caps {
		labels = append(labels, c.label)
	}
	idxs, err := fuzzyfinder.FindMulti(labels, func(i int) string { return labels[i] },
		fuzzyfinder.WithPromptString(target+"> "),
		// Header is green in go-fuzzyfinder — visually distinct from the item rows.
		fuzzyfinder.WithHeader("tab to add · enter continues"),
		fuzzyfinder.WithPreselected(func(i int) bool { return i == 0 }),
	)
	if errors.Is(err, fuzzyfinder.ErrAbort) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	return capabilitySelection(caps, idxs), nil
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
	case "proxy", "firewall":
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
