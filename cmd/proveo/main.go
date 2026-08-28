// Command proveo is the harness CLI.
// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/egress/teardown-and-signals.puml, _spec/_paradigms/egress-boundary.puml, _spec/internal/egress/egress-tiers.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/_conventions/design-decision-ids.puml, _spec/_paradigms/credential-boundary.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/provider/model-resolution.puml, _spec/internal/dind/dind-sidecar.puml, _spec/internal/runner/hardened-run-argv.puml, _spec/internal/workspace/mount-model.puml, _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/runlog/run-transcript.puml, _spec/internal/manifest/harness-manifest-schema.puml, _spec/_paradigms/git-identity.puml, _spec/internal/proveohome/proveo-home-components.puml, _spec/_plans/ci-pipeline.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
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
		var ae backend.ExitError
		if errors.As(err, &ae) {
			os.Exit(ae.Code)
		}
		ui.Failf("%v", err)
		os.Exit(1)
	}
}

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
				chosen, isLocal := posture.ResolveImageChoice(image)
				if isLocal {
					ui.Iconf("📦", "image: %s (local build — newer than the published tag)", chosen)
				}
				image = chosen
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
			return run.Do(run.Params{
				Target: target, Image: image, Mode: egressMode, Credentials: credentials,
				ModeSet: modeSet, CredsSet: credsSet, LocalModel: localModel,
				Input: input, Output: output, Scope: scope, DataDir: dataDir,
				Shell: shellMode, PrintOnly: printOnly, Extra: extra, Clone: cloneMode,
			}, runDeps())
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

func projectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List monorepo sub-projects discoverable from the current repo",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root := workspace.Resolve(run.OrWD("")).Root
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

// pickProject returns the chosen monorepo scope ("" = repo root).
func pickProject(projs []workspace.Project, in io.Reader, out io.Writer) string {
	if agentio.IsReaderTTY(in) {
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

// runDeps binds the terminal- and host-bound halves of a run. Everything here
// needs a TTY, the gh session or the docker daemon, which is why internal/run
// takes them as functions instead of importing them.
func runDeps() run.Deps {
	return run.Deps{
		ManifestFor: manifestForTarget,
		PickProject: func(projs []workspace.Project) string { return pickProject(projs, os.Stdin, os.Stderr) },
		PromptEnv: func(target string, missing []manifest.EnvVar) map[string]string {
			return promptEnv(target, missing, os.Stdin, os.Stderr, termSecret)
		},
		GitHubTokenEnv: func(interactive bool) string {
			return resolveGitHubTokenEnv(hostGhAuth(), interactive, os.Stdin, os.Stderr)
		},
		ProvisionConfirm: provisionConfirm,
		PreflightImages:  preflightImages,
		SquidConfig:      proveo.SquidConfig,
		ModelBridges:     proveo.ModelBridges,
	}
}
