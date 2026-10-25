// SPEC: _spec/overview.puml, _spec/cmd/proveo/usage.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/_conventions/design-decision-ids.puml, _spec/_paradigms/credential-boundary.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/provider/model-resolution.puml, _spec/_paradigms/retire-dind.puml, _spec/internal/workspace/mount-model.puml, _spec/internal/runlog/run-transcript.puml, _spec/internal/manifest/harness-manifest-schema.puml, _spec/_paradigms/git-identity.puml, _spec/internal/proveohome/proveo-home-components.puml, _spec/_plans/ci-pipeline.puml, _spec/internal/sbx/virtiofs-cwd-invalidation.puml, _spec/internal/sbx/policy-baseline.puml, _spec/internal/ptyproxy/pty-ownership.puml
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

func loadTargets() (map[string]string, error) {
	ms, err := loadManifests()
	if err != nil {
		return nil, err
	}
	return manifest.Targets(ms)
}

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
	if err := rootCmd().Execute(); err != nil {
		if code, ok := exitCode(err); ok {
			os.Exit(code)
		}
		ui.Failf("%v", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
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
				return doInit(initOptions{})
			}
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{printf \"%s version %s\\n\" .Name .Version}}")
	root.Flags().BoolVar(&flagLS, "ls", false, "List available harness targets")
	root.Flags().BoolVar(&flagInit, "init", false, "Install and sign in to the sbx backend proveo runs on")
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) { renderHelp(cmd.OutOrStdout(), cmd) })
	root.AddCommand(versionCmd(), lsCmd(), runCmd(), scheduleCmd(), attachCmd(), projectsCmd(), setupCmd(), initCmd(),
		updateCmd(), uninstallCmd(), cleanCmd(), buildCmd(), deployCmd(), testCmd(), envHelpCmd())
	addHarnessAliases(root)
	return root
}

// exitCode is the status err carries: the agent's own, or 128+signal for a stopped run.
func exitCode(err error) (int, bool) {
	var ae backend.ExitError
	if errors.As(err, &ae) {
		return ae.Code, true
	}
	var st backend.Stopped
	if errors.As(err, &st) {
		return st.Code(), true
	}
	return 0, false
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the proveo version (alias for --version)",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { fmt.Printf("proveo version %s\n", version) },
	}
}

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
	writeTargets(os.Stdout, targets)
	return nil
}

// runFlags are the flags shared by `proveo run` and each harness alias.
type runFlags struct {
	credentials, localModel, input, output, scope, dataDir, imageOverride, resumeID string
	printOnly, contSession, listSessions, cloneMode, yes                            bool
	addons                                                                          []string
}

func (f *runFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.credentials, "credentials", "broker",
		strings.Join(run.CredentialModes(), "|")+" (default broker: sbx's proxy holds the key and attaches it; forward puts it in the sandbox env)")
	cmd.Flags().StringVar(&f.localModel, "local-model", "", "Ollama model to serve locally")
	cmd.Flags().StringVar(&f.input, "input", "", "input dir to mount read-only (default: cwd)")
	cmd.Flags().StringVar(&f.output, "output", "", "output dir to mount read-write (default: <input>/reports)")
	cmd.Flags().StringVar(&f.scope, "scope", "", "monorepo sub-project to open (repo-relative; omit for an interactive picker)")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "extra directory to mount read-only at /workspace/data")
	cmd.Flags().StringVar(&f.imageOverride, "image", "", "override the image for the target")
	cmd.Flags().BoolVar(&f.cloneMode, "clone", run.CloneDefault(os.Getenv),
		"run the agent on a private in-container CLONE of the repo (sbx backend; default on, PROVEO_CLONE=off flips it): "+
			"the checkout is never written, host-built dependency trees never cross, and the agent's commits are fetched back "+
			"at teardown under refs/proveo/<sid>/ — `--clone=false` edits the mounted checkout directly")
	cmd.Flags().StringVar(&f.resumeID, "resume", "", "resume a prior agent session by id (harness-specific)")
	cmd.Flags().BoolVar(&f.contSession, "continue", false, "continue the most recent session for this workspace")
	cmd.Flags().BoolVar(&f.listSessions, "ls", false, "list resumable sessions (cursor/claude/codex) and exit into the tool picker")
	cmd.Flags().BoolVar(&f.printOnly, "print", false, "write the sbx kit and print the sbx command instead of executing")
	cmd.Flags().StringArrayVar(&f.addons, "addon", nil, "interface add-on to enable without the choice form (repeatable): host-chrome, browser, claude-in-chrome")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "accept the remembered and flagged choices without drawing the choice form (unattended runs)")
}

// prompts is a terminal with no unattended flag, so a picker may open.
func (f *runFlags) prompts(cmd *cobra.Command) bool {
	if f.yes || f.printOnly || f.listSessions || f.resumeID != "" || f.contSession {
		return false
	}
	return stdioInteractive(cmd)
}

func (f *runFlags) execute(cmd *cobra.Command, target string, extra []string) error {
	targets, err := loadTargets()
	if err != nil {
		return err
	}
	image, ok := targets[target]
	if !ok {
		return fmt.Errorf("unknown target %q (see `proveo ls`)", target)
	}
	if f.imageOverride != "" {
		image = f.imageOverride // an explicit --image is a decision, not a default
	} else {
		chosen, isLocal := posture.ResolveImageChoice(image)
		if isLocal {
			ui.Section(ui.SectionRun)
			ui.Appf("image: %s (local build — newer than the published tag)", chosen)
		}
		image = chosen
	}
	credsSet := cmd.Flags().Changed("credentials")
	if !run.ValidCredentials(f.credentials) {
		return fmt.Errorf("invalid --credentials %q (%s)", f.credentials, strings.Join(run.CredentialModes(), "|"))
	}
	resumeArgs, err := proveohome.ResumeArgs(target, f.resumeID, f.contSession, f.listSessions)
	if err != nil {
		return err
	}
	if len(resumeArgs) > 0 {
		extra = append(append([]string{}, resumeArgs...), extra...)
	}
	return run.Do(run.Params{
		Target: target, Image: image, Credentials: f.credentials,
		CredsSet: credsSet, LocalModel: f.localModel,
		Input: f.input, Output: f.output, Scope: f.scope, DataDir: f.dataDir,
		PrintOnly: f.printOnly, Extra: extra,
		Clone: f.cloneMode, CloneSet: cmd.Flags().Changed("clone"),
		Addons: f.addons, AddonsSet: cmd.Flags().Changed("addon"), Yes: f.yes,
	}, runDeps())
}

func runCmd() *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run [target] [-- args...]",
		Short: "Run a harness against the current repo",
		Long: `With no target, a terminal lists running tasks and the harnesses.
Picking a running task opens its terminal. Picking a harness starts it.
Without a terminal the same lists print and proveo stops.

proveo <harness> is proveo run <harness>. When a task is already running
and no arguments are given, that command lists the running tasks and
offers to start the harness.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				name, err := f.chooseTarget(cmd)
				if err != nil || name == "" {
					return err
				}
				return f.execute(cmd, name, nil)
			}
			return f.execute(cmd, args[0], args[1:])
		},
	}
	f.bind(cmd)
	return cmd
}

// chooseTarget lists running tasks and harnesses. An empty name means the lists were printed.
func (f *runFlags) chooseTarget(cmd *cobra.Command) (string, error) {
	targets, err := loadTargets()
	if err != nil {
		return "", err
	}
	tasks := runningOrNone()
	if !f.prompts(cmd) {
		if stdioInteractive(cmd) {
			return "", fmt.Errorf("no target given (see `proveo ls`)")
		}
		writeRunLists(cmd.OutOrStdout(), tasks, targets)
		return "", nil
	}
	got, err := offer(runChoices(tasks, targets), "run> ", "no target selected")
	if err != nil {
		return "", err
	}
	if got.Attach {
		return "", attachSession(got.Name, false)
	}
	return got.Name, nil
}

func addHarnessAliases(root *cobra.Command) {
	targets, err := loadTargets()
	if err != nil {
		return
	}
	taken := map[string]bool{}
	for _, c := range root.Commands() {
		taken[c.Name()] = true
		for _, a := range c.Aliases {
			taken[a] = true
		}
	}
	for _, name := range sortedKeys(targets) {
		if taken[name] {
			continue
		}
		root.AddCommand(harnessCmd(name))
	}
}

func harnessCmd(name string) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   name + " [-- args...]",
		Short: "Alias for `proveo run " + name + "`",
		Annotations: map[string]string{
			"proveo": annHarness,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && f.prompts(cmd) {
				if tasks := runningOrNone(); len(tasks) > 0 {
					got, err := offer(aliasChoices(name, tasks), name+"> ", "no task selected")
					if err != nil {
						return err
					}
					if got.Attach {
						return attachSession(got.Name, false)
					}
				}
			}
			return f.execute(cmd, name, args)
		},
	}
	f.bind(cmd)
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
			out := ui.New(os.Stdout)
			out.Section(ui.SectionProjects)
			for _, p := range projs {
				out.Storef("%-34s %s", p.Path, p.Tool)
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
		ui.Section(ui.SectionSetup)
		ui.Storef("would append to %s:", rc)
		for _, l := range strings.Split(strings.TrimRight(sh.Block(binDir), "\n"), "\n") {
			ui.Notef("  %s", l)
		}
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
	dir = filepath.Clean(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

func pickProject(projs []workspace.Project, in io.Reader, out io.Writer) string {
	if agentio.IsReaderTTY(in) {
		return fuzzyPickProject(projs)
	}
	return pickProjectNumbered(projs, in, out)
}

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
	pr := ui.New(out)
	pr.Section(ui.SectionScope)
	pr.Hostf("monorepo detected — choose a scope:")
	pr.Notef("   0) <repo root>")
	for i, p := range projs {
		pr.Notef("  %2d) %s", i+1, p.Path)
	}
	pr.Askf("scope [0]:")
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

func runDeps() run.Deps {
	return run.Deps{
		ManifestFor: manifestForTarget,
		PickProject: func(projs []workspace.Project) string { return pickProject(projs, os.Stdin, os.Stderr) },
		PromptEnv: func(target string, missing []manifest.EnvVar) map[string]string {
			return promptEnv(target, missing, os.Stdin, os.Stderr, termSecret)
		},
		GitHubTokenEnv: func() string {
			return resolveGitHubTokenEnv(hostGhAuth())
		},
		ProvisionConfirm: provisionConfirm,
		PreflightImages:  preflightImages,
		EnsureDesktop:    ensureDesktop,
	}
}
