// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/internal/sbx/clone-workspace.puml, _spec/internal/sbx/kit-domain-form.puml, _spec/internal/sbx/ide-attach.puml, _spec/packages/lib/config-seeding-and-persistence.puml, _spec/_paradigms/capability-ladder.puml, _spec/_experiments/sbx-kit-capabilities.puml, _spec/minimum_requirements.puml, _spec/internal/sbx/launch-env.puml
// Package sandbox is the sbx backend:
package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/engine"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

const (
	EvidenceVar        = "PROVEO_AGENT_EVIDENCE"
	unallowlistedProbe = "proveo-egress-probe.invalid"
)

func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

func ReportUnavailable(why string) {
	ui.Section(ui.SectionExecution)
	ui.Warnf("docker sandbox unavailable (%s) — falling back to docker+egress", why)
	if eng := engine.Detect(); eng.Kind != engine.Unknown {
		ui.Notef("engine: %s (%s)", eng.Label(), eng.Isolation())
	}
	if cmd := sbx.InstallCmd(sbx.Installed()); cmd != "" {
		if sbx.Installed() {
			ui.Notef("proveo targets sbx %s or newer:", sbx.MinVersion)
		} else {
			ui.Notef("sbx is standalone and does not need Docker Desktop:")
		}
		ui.Notef("  %s", cmd)
	}
}

func Ensure(confirm func(string) bool) (bool, string) {
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
	if !confirm(fmt.Sprintf("%s the docker sandboxes CLI (%s)?", verb, install)) {
		return false, why
	}
	ui.Section(ui.SectionStarting)
	ui.Appf("%sing sbx: %s", verb, install)
	c := exec.Command("bash", "-lc", install)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return false, fmt.Sprintf("%s failed: %v", verb, err)
	}
	return sbx.Available()
}

func Ready(printOnly bool, confirm func(string) bool) (bool, string) {
	if printOnly {
		return sbx.Available()
	}
	return Ensure(confirm)
}

func StateHome(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, sbx.StateHomeVar+"="); ok {
			return v
		}
	}
	return ""
}

func SbxRun(args ...string) (string, error) {
	out, err := exec.Command(sbx.Binary, args...).CombinedOutput()
	return string(out), err
}

func SaveState(name string, env []string, exists bool, run func(...string) (string, error)) (string, error) {
	if name == "" || !exists || StateHome(env) == "" {
		return "", nil
	}
	return run(sbx.SaveStateArgs(name)...)
}

func PreserveClone(in Input, cfg sbx.RunConfig) {
	if !in.Clone || in.RepoRoot == "" || !sbx.Exists(cfg.Name) {
		return
	}
	if wd := FirstHost(cfg.Mounts); wd != "" {
		if out, err := SbxRun(sbx.CloneSnapshotArgs(cfg.Name, wd)...); err != nil {
			ui.Warnf("clone: could not snapshot uncommitted work (%v): %s", err, strings.TrimSpace(out))
		}
	}
	liftClonedOutput(in, cfg, liftViaSbx)
	if !carryClone(in, cfg, sbx.Running(cfg.Name), fetchViaRemote, bundleViaSbx) {
		return
	}
	reportCloneRefs(in, cfg)
}

// cloneCarry is one route home: the sbx exit code (for the empty sentinel),
// output worth quoting, and the error.
type cloneCarry func(in Input, cfg sbx.RunConfig) (int, string, error)

func carryClone(in Input, cfg sbx.RunConfig, running bool, viaRemote, viaBundle cloneCarry) bool {
	if running {
		if _, out, err := viaRemote(in, cfg); err == nil {
			return true
		} else {
			ui.Notef("clone: the sandbox's git remote did not answer (%v: %s) — carrying the branches out over `sbx exec` instead",
				err, strings.TrimSpace(out))
		}
	}
	code, out, err := viaBundle(in, cfg)
	switch {
	case err == nil:
		return true
	case code == sbx.CloneBundleEmpty:
		ui.Notef("clone: the agent left no commit this repository does not already have")
		return false
	default:
		ui.Warnf("clone: could not carry the agent's branches home (%v): %s", err, strings.TrimSpace(out))
		for _, l := range CloneRescueLines(cfg.Name, in.Sid, FirstHost(cfg.Mounts), in.RepoRoot) {
			ui.Notef("%s", l)
		}
		return false
	}
}

// CloneRescueLines is the by-hand recipe, using the transport that works on a
// stopped sandbox.
func CloneRescueLines(name, refsKey, workdir, repoRoot string) []string {
	if workdir == "" || repoRoot == "" {
		return nil
	}
	bundle := "/tmp/" + name + ".bundle"
	return []string{
		fmt.Sprintf("while %s exists: `sbx exec -w / %s -- git -C %s bundle create - --all > %s`",
			name, name, workdir, bundle),
		fmt.Sprintf("then: `git -C %s fetch %s '+refs/heads/*:%s/*'`", repoRoot, bundle, sbx.CloneRefs(refsKey)),
	}
}

func fetchViaRemote(in Input, cfg sbx.RunConfig) (int, string, error) {
	out, err := exec.Command("git", sbx.CloneFetchArgs(in.RepoRoot, cfg.Name, in.Sid)...).CombinedOutput()
	return exitCodeOf(err), string(out), err
}

func bundleViaSbx(in Input, cfg sbx.RunConfig) (int, string, error) {
	wd := FirstHost(cfg.Mounts)
	if wd == "" {
		return -1, "", errors.New("no workspace mount to bundle from")
	}
	f, err := os.CreateTemp("", "proveo-clone-*.bundle")
	if err != nil {
		return -1, "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	src := exec.Command(sbx.Binary, sbx.CloneBundleArgs(cfg.Name, wd, hostTips(in.RepoRoot))...)
	var errb strings.Builder
	src.Stdout, src.Stderr = f, &errb
	runErr := src.Run()
	if cerr := f.Close(); runErr == nil && cerr != nil {
		return -1, errb.String(), cerr
	}
	if runErr != nil {
		return exitCodeOf(runErr), errb.String(), runErr
	}
	out, err := exec.Command("git", sbx.CloneBundleFetchArgs(in.RepoRoot, path, in.Sid)...).CombinedOutput()
	if err != nil {
		return exitCodeOf(err), errb.String() + string(out), err
	}
	return 0, "", nil
}

func hostTips(repoRoot string) []string {
	out, err := exec.Command("git", sbx.CloneHostTipsArgs(repoRoot)...).Output()
	if err != nil {
		return nil
	}
	tips := strings.Fields(string(out))
	if len(tips) > sbx.CloneHostTipsCap {
		tips = tips[:sbx.CloneHostTipsCap]
	}
	return tips
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func reportCloneRefs(in Input, cfg sbx.RunConfig) {
	refs, _ := exec.Command("git", "-C", in.RepoRoot, "for-each-ref",
		"--format=%(refname:short)", sbx.CloneRefs(in.Sid)+"/").Output()
	names := strings.Fields(string(refs))
	if len(names) == 0 {
		ui.Notef("clone: the agent left no branches to fetch")
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("clone: the agent's work is in your repository under %s/ — %s", sbx.CloneRefs(in.Sid), strings.Join(names, " "))
	ui.Notef("review: `git log --oneline main..%s` · adopt: `git checkout -b <branch> %s`", names[0], names[0])
}

func liftClonedOutput(in Input, cfg sbx.RunConfig, lift func(args []string, into string) (int, string, error)) {
	rel, ok := nestedRel(in.RepoRoot, in.OutputDir)
	wd := FirstHost(cfg.Mounts)
	if !ok || wd == "" {
		return
	}
	code, out, err := lift(sbx.CloneLiftArgs(cfg.Name, wd, rel), in.RepoRoot)
	switch {
	case err == nil:
		ui.Storef("clone: deliverables lifted from the clone's %s/ into %s", rel, in.OutputDir)
	case code == sbx.CloneLiftNothing:
		ui.Notef("clone: the agent wrote nothing under %s/", rel)
	default:
		ui.Warnf("clone: could not lift %s/ out of the sandbox (%v): %s — while %s exists: `sbx exec -w / %s -- tar -C %s -cf - %s | tar -xf - -C %s`",
			rel, err, strings.TrimSpace(out), cfg.Name, cfg.Name, wd, rel, in.RepoRoot)
	}
}

func liftViaSbx(args []string, into string) (int, string, error) {
	src := exec.Command(sbx.Binary, args...)
	var srcErr strings.Builder
	src.Stderr = &srcErr
	stdout, err := src.StdoutPipe()
	if err != nil {
		return -1, "", err
	}
	untar := exec.Command("tar", "-xf", "-", "-C", into)
	untar.Stdin = stdout
	var untarOut strings.Builder
	untar.Stdout, untar.Stderr = &untarOut, &untarOut
	if err := src.Start(); err != nil {
		return -1, srcErr.String(), err
	}
	untarErr := untar.Run()
	srcRun := src.Wait()
	if srcRun != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(srcRun, &ee) {
			code = ee.ExitCode()
		}
		return code, srcErr.String() + untarOut.String(), srcRun
	}
	if untarErr != nil {
		return 0, untarOut.String(), untarErr
	}
	return 0, "", nil
}

func FreeLoopbackPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func StartCDPViewport(in Input, cfg sbx.RunConfig) func() {
	if len(cfg.Publish) == 0 {
		return func() {}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", in.CDPHostPort)
	ui.Section(ui.SectionInterface)
	ui.Hostf("browser viewport: %s — attach Chrome DevTools or Playwright (connectOverCDP) to the agent's Chromium", url)
	ui.Notef("%s/json/list once the agent opens a page; nothing is exposed beyond this machine's loopback", url)

	stop := make(chan struct{})
	go func() {
		for {
			if !sbx.Running(cfg.Name) {
				select {
				case <-stop:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			c := exec.Command(sbx.Binary, sbx.CDPRelayArgs(cfg.Name)...)
			c.Stdout, c.Stderr = io.Discard, io.Discard
			if err := c.Start(); err != nil {
				select {
				case <-stop:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			done := make(chan struct{})
			go func() { _ = c.Wait(); close(done) }()
			select {
			case <-stop:
				_ = c.Process.Kill()
				return
			case <-done:
			}
		}
	}()
	return func() { close(stop) }
}

func cdpPublish(in Input) []string {
	if !in.Browser || in.CDPHostPort <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d:%d", in.CDPHostPort, sbx.CDPRelayPort)}
}

func SplitNested(root string, mounts []sbx.Mount) (kept, nested []sbx.Mount) {
	for _, m := range mounts {
		if _, ok := nestedRel(root, m.Host); ok {
			nested = append(nested, m)
			continue
		}
		kept = append(kept, m)
	}
	return kept, nested
}

func nestedRel(root, path string) (string, bool) {
	root, path = strings.TrimSpace(root), strings.TrimSpace(path)
	if root == "" || path == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func KeptLines(name, runLog string) []string {
	lines := []string{fmt.Sprintf(
		"sandbox %s kept for diagnosis (the run failed) — `sbx exec %s -- sh`, then `sbx rm --force %s`",
		name, name, name)}
	if strings.TrimSpace(runLog) != "" {
		lines = append(lines,
			fmt.Sprintf("every line this run printed, posture and warnings included: %s", runLog))
	}
	return lines
}

func IDEAttachLines(in Input, cfg sbx.RunConfig, live bool) []string {
	name, workdir := strings.TrimSpace(cfg.Name), FirstHost(cfg.Mounts)
	if name == "" || workdir == "" {
		return nil
	}
	when := ""
	if live {
		when = " (live)"
	}
	lines := []string{fmt.Sprintf(
		"IDE attach%s: run `sbx %s` once, then connect to `%s` and open %s",
		when, strings.Join(sbx.SetupSSHArgs(), " "), sbx.SSHHost(name), workdir)}
	if live {
		lines = append(lines, "the running agent and the editor both write this tree")
	}
	if !in.Clone {
		return append(lines,
			"workspace: mounted checkout — IDE saves write the host tree directly")
	}
	refsKey := strings.TrimSpace(in.Sid)
	if refsKey == "" {
		refsKey = name
	}
	fetch := strings.Join(sbx.CloneFetchArgs(in.RepoRoot, name, refsKey), " ")
	return append(lines,
		"workspace: DISPOSABLE CLONE — commit IDE edits before removing the sandbox; they do not appear in the host checkout",
		fmt.Sprintf("carry IDE commits home before `sbx rm`: `git %s`", fetch))
}

func PrintIDEAttach(in Input, cfg sbx.RunConfig, live bool) {
	lines := IDEAttachLines(in, cfg, live)
	if len(lines) == 0 {
		return
	}
	if live {
		ui.Section(ui.SectionInterface)
	}
	for i, l := range lines {
		if i == 0 {
			ui.Hostf("%s", l)
			continue
		}
		ui.Notef("%s", l)
	}
}

func WorkspaceBinds(mounts []sbx.Mount) []sbx.Mount {
	var out []sbx.Mount
	seen := map[string]bool{}
	for _, m := range mounts {
		if strings.HasPrefix(m.Container, proveohome.ContainerHome+"/") {
			continue // nested under home; its nesting cannot be reproduced
		}
		if fi, err := os.Stat(m.Host); err != nil || !fi.IsDir() {
			continue
		}
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

func Home(env []string, mounts []sbx.Mount) []string {
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
	return append(out, sbx.StateHomeVar+"="+host)
}

func FirstHost(mounts []sbx.Mount) string {
	if len(mounts) == 0 {
		return ""
	}
	return mounts[0].Host
}

// Input is the resolved input to the sbx backend.
type Input struct {
	Target, Image, AuthVar string
	Shell, Clone           bool
	RepoRoot               string // host repository root: where a clone's commits are fetched back to
	OutputDir              string
	Browser                bool
	CDPHostPort            int
	Extra                  []string
	Roles                  provider.Roles
	Evidence               string // was params.evidenceOrDefault()
	Forwards               bool   // was params.forwards()
	Man                    manifest.Manifest
	// ImageEntrypoint reads the image's declared ENTRYPOINT; a sandbox Kit needs
	// it verbatim. Injected so Spec stays testable without docker.
	ImageEntrypoint func(image string) []string
	// AgentEnv is what the egress plan decided for the agent, as KEY=VALUE.
	AgentEnv         []string
	Sid, EgDir       string
	Mounts           []runner.Mount
	Workdir          string
	Lookup           func(string) string
	Detected         []string
	GitEnv           []string
	HomeEnv          []string
	BridgeEnv        []string
	ScopeRel         string
	WorktreeFallback bool
	WorktreeEnv      []string
	DataDir          string
	Memory           string
	CPUs             int
	HomeRoot         string
	RunLog           string
	HomeAccess       HomeAccess
}

// imageEntrypoint reads the image's declared ENTRYPOINT, or nothing when the
// image cannot be inspected.
func imageEntrypoint(in Input) []string {
	if in.ImageEntrypoint == nil || in.Image == "" {
		return nil
	}
	return in.ImageEntrypoint(in.Image)
}

// Harness is the def a target belongs to. One manifest owns several targets —
// its variant images — and they all share the def's sbx agent and launch.
func Harness(in Input) string {
	if in.Man.Name != "" {
		return in.Man.Name
	}
	return in.Target
}

func Spec(in Input) (sbx.RunConfig, sbx.Kit, [][2]string) {
	// The registry speaks Squid's `dstdomain`; the Kit speaks sbx's patterns.
	// This is the one place the two grammars meet, and it has to happen before
	// anything is deduplicated — `.x.ai` and `x.ai` are one entry afterwards.
	hosts := map[string]bool{}
	addHost := func(h string) {
		for _, p := range sbx.DomainPatterns(h) {
			hosts[p] = true
		}
	}
	for _, d := range strings.Fields(credentials.JoinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), in.Man.Capabilities.Hosts)) {
		addHost(d)
	}
	// Gated here as well as at run.go:433. agentCredentials and the env-var loop
	// both re-check AllowsProvider; the allowlist used to trust its caller, so a
	// forbidden provider kept its reach while losing its credential — reach the
	// harness never needed and proveo never meant to grant.
	for _, h := range credentials.ReachableHosts(credentials.FilterProviders(in.Detected, in.Man.Capabilities)) {
		addHost(h)
	}
	allow := make([]string, 0, len(hosts))
	for h := range hosts {
		allow = append(allow, h)
	}
	sort.Strings(allow)

	var forwarded []string
	addForward := func(name string) {
		if in.Lookup(name) == "" {
			return
		}
		for _, n := range forwarded {
			if n == name {
				return
			}
		}
		forwarded = append(forwarded, name)
	}

	var secrets [][2]string
	addSecret := func(name string) {
		v := in.Lookup(name)
		if v == "" {
			return
		}
		for _, kv := range secrets {
			if kv[0] == name {
				return
			}
		}
		if _, kind := credentials.StoreName(name, in.Target); kind == credentials.StoreUninjectable {
			addForward(name) // nothing to attach: a signing key or a credentials file, not a header
			return
		}
		if in.Forwards {
			addForward(name) // the operator chose the complete route over the safer one
			return
		}
		if service, kind := credentials.StoreName(name, in.Target); kind == credentials.StoreAPIKey && sbx.HasOAuthSlot(service) {
			ui.Notef("sandbox secret: %s not stored — sbx's global %q entry also holds the subscription, and an API key "+
				"there outranks it in every sandbox; to bill per token, `sbx secret set %s` yourself", name, service, service)
			return
		}
		secrets = append(secrets, [2]string{name, v})
	}
	suppressedAuth := credentials.AuthSuppressor(in.Man, in.Target, in.AuthVar, in.HomeRoot, in.Lookup)
	for _, e := range in.Man.Env {
		if !e.Secret {
			continue
		}
		if suppressedAuth(e.Name) {
			continue
		}
		addSecret(e.Name)
	}
	for _, k := range provider.KeyVars() {
		if !in.Man.Capabilities.AllowsProvider(credentials.ProviderOfKeyVar(k)) {
			continue
		}
		if suppressedAuth(k) {
			continue
		}
		addSecret(k)
	}

	var env []string
	env = append(env, forwarded...)
	for _, e := range in.Man.Env {
		if e.Secret {
			continue
		}
		if v := strings.TrimSpace(in.Lookup(e.Name)); v != "" {
			env = append(env, e.Name+"="+v)
		}
	}
	for _, k := range credentials.ConfigVarsFor(in.Man) {
		if v := strings.TrimSpace(in.Lookup(k)); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, EvidenceVar+"="+in.Evidence)
	if set := proveohome.ConfigSet(in.Man.Home); set != "" {
		env = append(env, proveohome.ConfigSetVar+"="+set)
	}
	if files := proveohome.ConfigFiles(in.Man.Home); files != "" {
		env = append(env, proveohome.ConfigFilesVar+"="+files)
	}
	env = append(env, in.Man.AgentEnvPairs(in.Lookup)...)
	env = append(env, in.GitEnv...)
	env = append(env, in.HomeEnv...)
	env = append(env, in.BridgeEnv...)
	if in.Browser && in.CDPHostPort > 0 {
		existing := ""
		if in.Lookup != nil {
			existing = in.Lookup("AGENT_BROWSER_ARGS")
		}
		env = append(env, "AGENT_BROWSER_ARGS="+sbx.BrowserCDPArgs(existing))
	}
	if in.ScopeRel != "" {
		env = append(env, "PROVEO_SCOPE_REL="+in.ScopeRel)
	}
	if in.WorktreeFallback {
		env = append(env, in.WorktreeEnv...)
	}

	var mounts, homeSourceMounts []sbx.Mount
	for _, m := range in.Mounts {
		sbxMount := sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly}
		homeSourceMounts = append(homeSourceMounts, sbxMount)
		if m.Container == proveohome.ContainerHome && filepath.Clean(m.Host) == filepath.Clean(in.HomeRoot) &&
			len(in.HomeAccess.Mounts) > 0 {
			continue
		}
		mounts = append(mounts, sbxMount)
	}
	for _, m := range in.HomeAccess.Mounts {
		mounts = append(mounts, sbx.Mount{Host: m.Host, Container: m.Container, ReadOnly: m.ReadOnly})
	}
	if in.DataDir != "" {
		mounts = append(mounts, sbx.Mount{Host: in.DataDir, Container: "/workspace/data", ReadOnly: true})
	}
	if in.Clone && in.RepoRoot != "" {
		var nested []sbx.Mount
		mounts, nested = SplitNested(in.RepoRoot, mounts)
		for _, m := range nested {
			if _, isOutput := nestedRel(in.RepoRoot, in.OutputDir); isOutput && filepath.Clean(m.Host) == filepath.Clean(in.OutputDir) {
				ui.Storef("clone: %s is inside the repository, so it is not mounted live — sbx clones only into an "+
					"empty workspace; the agent writes it inside the clone and proveo lifts it back here at teardown", m.Host)
				continue
			}
			ui.Warnf("clone: %s is inside the repository and cannot be mounted into a clone — read it from the clone instead", m.Host)
		}
	}

	harness := Harness(in)
	agent, launch := sbx.AgentFor(harness)
	command := launch
	if len(in.Extra) > 0 {
		if agent == sbx.ShellAgent {
			command = sbx.ShellLaunch(harness, in.Extra)
		} else {
			command = in.Extra
		}
	}
	entrypoint := imageEntrypoint(in)
	ownAgent := !in.Shell && sbx.DeclaresOwnAgent(harness)
	if ownAgent && len(entrypoint) == 0 {
		ownAgent = false
		ui.Warnf("sandbox: no readable ENTRYPOINT on %s — %s runs under sbx's shell agent rather than one of its own",
			in.Image, harness)
	}
	if ownAgent {
		agent, command = sbx.AgentName(harness), in.Extra
	}
	if in.Shell {
		command, agent = nil, sbx.ShellAgent
	}
	command = withChromeFlag(agent, in.BridgeEnv, command)
	cfg := sbx.RunConfig{
		Name:    sbx.SandboxName(in.Target, FirstHost(WorkspaceBinds(mounts))),
		KitDir:  filepath.Join(in.EgDir, "sbx", "kit"),
		Image:   in.Image,
		Memory:  in.Memory,
		CPUs:    in.CPUs,
		Clone:   in.Clone,
		Publish: cdpPublish(in),
		Agent:   agent,
		Mounts:  WorkspaceBinds(mounts),
		Env:     DeclineMCPGateway(Home(launchEnv(in, agent, env, mounts), homeSourceMounts)),
		Command: command,
	}
	if in.HomeAccess.FilesRoot != "" {
		cfg.Env = append(cfg.Env, proveohome.ConfigFilesRootVar+"="+in.HomeAccess.FilesRoot)
	}
	var creds []sbx.KitCredential
	if ownAgent {
		var domains []string
		creds, domains, secrets = agentCredentials(in, secrets)
		for _, d := range domains {
			if !hosts[d] {
				hosts[d] = true
				allow = append(allow, d)
			}
		}
		sort.Strings(allow)
	}

	kit := sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          in.Target + "-posture",
		DisplayName:   "proveo posture (" + in.Target + ")",
		Description:   "Reachability, host-resolved environment and the seed step for a proveo run.",
		Permissions:   sbx.KitPermissions{Network: sbx.KitNet{Allow: allow}},
		Environment:   &sbx.KitEnv{Variables: WithMCPGatewayPolicy(KitEnvVars(cfg.Env))},
		Setup:         &sbx.KitSetup{Startup: startupCommands(in.Target, ownAgent)},
	}
	if ownAgent {
		kit.Kind = "sandbox"
		kit.Name = cfg.Agent
		kit.DisplayName = in.Target + " (proveo)"
		kit.Description = "proveo harness " + in.Target + ": image, launch, reachability and the seed step."
		kit.Sandbox = &sbx.KitSandbox{Image: in.Image, Entrypoint: entrypoint}
		kit.Credentials = creds
	}
	return cfg, kit, secrets
}

// MCPGatewayVar is the variable sbx's built-in agent kits gate their MCP
// registration on.
const MCPGatewayVar = "MCP_GATEWAY_URL"

func WithMCPGatewayPolicy(vars map[string]string) map[string]string {
	if MCPGatewayAllowed() {
		return vars
	}
	if vars == nil {
		vars = map[string]string{}
	}
	vars[MCPGatewayVar] = ""
	return vars
}

func DeclineMCPGateway(env []string) []string {
	if MCPGatewayAllowed() {
		return env
	}
	for _, e := range env {
		if strings.HasPrefix(e, MCPGatewayVar+"=") {
			return env
		}
	}
	return append(env, MCPGatewayVar+"=")
}

func MCPGatewayAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_SBX_MCP"))) {
	case "on", "1", "yes", "true", "enable", "enabled":
		return true
	}
	return false
}

// SPEC: _spec/defs/claudecode/chrome-bridge.puml
func withChromeFlag(agent string, bridge, command []string) []string {
	if len(bridge) == 0 || agent != sbx.BuiltinAgent("claudecode") {
		return command
	}
	for _, a := range command {
		if a == "--chrome" {
			return command
		}
	}
	return append([]string{"--chrome"}, command...)
}

func KitEnvVars(env []string) map[string]string {
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

func Run(in Input) error {
	homeAccess := in.HomeAccess
	if homeAccess.Root == "" && in.HomeRoot != "" {
		var err error
		homeAccess, err = PrepareHomeAccess(in.HomeRoot, in.EgDir, in.Man.Home)
		if err != nil {
			return err
		}
	}
	in.HomeAccess = homeAccess
	keepHomeAccess := false
	defer func() {
		if !keepHomeAccess {
			homeAccess.Cleanup()
		}
	}()
	cfg, kit, secrets := Spec(in)
	if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
		return err
	}
	ui.Section(ui.SectionStarting)
	launchCfg := reuseOrCreate(cfg, sbx.Exists)
	if err := sbx.EnsureTemplate(launchCfg.Image, func(f string, a ...any) {
		ui.Appf(f, a...)
	}); err != nil {
		return err
	}
	var child credentials.ChildEnv
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") {
			child.Add(e, in.Lookup)
		}
	}
	for _, kv := range secrets {
		ui.Section(ui.SectionSecrets)
		if hosts, service, ok := customSecretTarget(kv[0], in.Lookup); ok {
			ui.Hostf("sandbox secret: %s (custom — sbx does not know %q, so the hosts and the "+
				"variable are declared with it)", kv[0], service)
			if err := sbx.SecretSetCustom(hosts, kv[0], kv[1]); err != nil {
				return fmt.Errorf("sandbox secret %s: %w", kv[0], err)
			}
			continue
		}
		name, _ := credentials.StoreName(kv[0], in.Target)
		if name == "" {
			name = kv[0]
		}
		ui.Hostf("sandbox secret: %s stored as %s — sbx's proxy attaches it, the agent never holds it", kv[0], name)
		if err := sbx.SecretSet(name, kv[1]); err != nil {
			return fmt.Errorf("sandbox secret %s: %w", name, err)
		}
	}
	if len(secrets) > 0 {
		ui.Notef("sbx's secret store is host-wide and outlives this run — `sbx secret ls`")
	}
	PrintIDEAttach(in, cfg, true)
	defer StartCDPViewport(in, launchCfg)()
	args := sbx.RunArgs(launchCfg)
	stdout, stderr, tail := agentio.Stdio(os.Stdout, os.Stderr, agentio.IsWriterTTY(os.Stdout))
	traceIn, stopTrace := agentio.Tracer(os.Getenv("PROVEO_TRACE_STDIN"))
	defer stopTrace()
	filtered := ptyproxy.Usable(os.Stdin, os.Stdout) && agentio.FilterEnabled()
	run := func() error {
		c := exec.Command(sbx.Binary, args...)
		c.Env = child.Apply(os.Environ())
		if filtered || (traceIn != nil && ptyproxy.Usable(os.Stdin, os.Stdout)) {
			px := ptyproxy.New(os.Stdin, os.Stdout)
			px.DisableFilter = !filtered
			px.DropReports = true
			px.Tap = traceIn
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
	endedAt := time.Now()
	if runErr != nil && !sbx.Exists(cfg.Name) {
		ui.Section(ui.SectionStarting)
		if err := sbx.ReloadTemplate(cfg.Image, func(f string, a ...any) {
			ui.Appf(f, a...)
		}); err == nil {
			ui.Asyncf("the sandbox did not start — retrying once on a freshly loaded template")
			args = sbx.RunArgs(cfg)
			runErr = run()
			endedAt = time.Now()
		}
	}
	defer func() {
		CapturePolicyLog(in.EgDir, cfg.Name)
		CaptureMemoryEvidence(in.EgDir, cfg.Name)
		if runErr != nil {
			said := false
			if lines := tail.Lines(); len(lines) > 0 {
				said = true
				ui.Section(ui.SectionResults)
				ui.Hostf("last output from the agent:")
				for _, l := range lines {
					ui.Notef("%s", l)
				}
			}
			restarted := !sbx.Running(cfg.Name)
			PreserveClone(in, cfg)
			_, _ = SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun)
			if err := homeAccess.Commit(); err != nil {
				ui.Warnf("home-root config not preserved: %v", err)
			}
			if t := credentials.AgentTranscript(in.Target, in.HomeRoot, startedAt, endedAt); t != "" {
				said = true
				ui.Storef("what the agent actually said is in %s", t)
			} else if restarted && sbx.Exists(cfg.Name) {
				ui.Storef("no transcript from this run — the sandbox had already stopped, " +
					"so state was copied out after it ended and anything newer than the run is the harvest's own")
			}
			if !said {
				if hint := credentials.NoCredentialHint(in.Man, in.Target, in.HomeRoot, cfg.Env, secrets,
					sbx.StoredSecretNames(), in.Lookup); len(hint) > 0 {
					ui.Hostf("%s", hint[0])
					for _, l := range hint[1:] {
						ui.Notef("%s", l)
					}
				}
			}
			ui.Section(ui.SectionResults)
			kept := KeptLines(cfg.Name, in.RunLog)
			ui.Warnf("%s", kept[0])
			for _, l := range kept[1:] {
				ui.Notef("%s", l)
			}
			if sbx.Exists(cfg.Name) {
				keepHomeAccess = true
				PrintIDEAttach(in, cfg, false)
			}
			return
		}
		PreserveClone(in, cfg)
		if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
			ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
		}
		if err := homeAccess.Commit(); err != nil {
			ui.Warnf("home-root config not preserved: %v", err)
		}
		rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput()
		if rmErr != nil && !sbx.NotFound(string(rmOut)) {
			keepHomeAccess = sbx.Exists(cfg.Name)
			ui.Warnf("sandbox teardown failed (%v): %s", rmErr, strings.TrimSpace(string(rmOut)))
			if keepHomeAccess {
				PrintIDEAttach(in, cfg, false)
			}
		}
	}()
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return backend.ExitError{Code: ee.ExitCode()}
	}
	return runErr
}

func WarnBaseline() {
	ui.Section(ui.SectionEgress)
	allowed, known := sbx.NetworkAllowed(unallowlistedProbe)
	if !known {
		ui.Notef("sbx network baseline: unreadable (`sbx policy check network %s`)", unallowlistedProbe)
		return
	}
	if !allowed {
		return
	}
	ui.Warnf("sbx's global network policy allows every host, so this run's Kit allowlist adds reach rather than limiting it")
	ui.Notef("the tier below describes proveo's intent, not what sandboxd will enforce")
	ui.Notef("make it bind once, host-wide: `sbx policy init deny-all` (or `balanced`), then `sbx policy ls`")
}

func CapturePolicyLog(egDir, name string) {
	if egDir == "" || name == "" {
		return
	}
	out, err := sbx.PolicyLog(name)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return
	}
	dir := filepath.Join(egDir, "sbx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, runlog.PolicyLogFile)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("egress record: %s", path)
}

// CaptureMemoryEvidence writes the guest's own account of its memory beside the
// policy log, and says so out loud when it names a kill.
//
// A sandbox is a VM with no swap: pressure does not degrade, it kills, and the
// kill carries no OOMKilled flag and no exit message. _spec/minimum_requirements
// .puml calls that "invisible in exactly the place an operator would look".
// This is that place.
//
// Best effort throughout. The sandbox may already be gone, and a run that died
// of memory pressure is the run least able to answer — so every failure here is
// silent, because a teardown warning about teardown teaches nothing.
// memoryEvidence is a var so a teardown test can state what the guest said
// without a live sandbox — the same seam storedSecretNames uses, and for the
// same reason: the branch that matters here fires only on an OOM, which is not
// a thing a test can arrange for real.
var memoryEvidence = sbx.MemoryEvidence

func CaptureMemoryEvidence(egDir, name string) {
	restarted := !sbx.Running(name)
	if egDir == "" || name == "" {
		return
	}
	out, err := memoryEvidence(name)
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return
	}
	dir := filepath.Join(egDir, "sbx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, runlog.MemoryEvidenceFile)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return
	}
	ui.Section(ui.SectionResults)
	if sbx.OOMEvidence(out) {
		// The whole reason this function exists: an OOM is otherwise a freeze
		// with no cause, and the operator goes looking at the agent instead.
		ui.Warnf("the guest kernel reported an out-of-memory kill — a sandbox has NO SWAP, "+
			"so memory pressure kills rather than slows. Raise the ceiling with "+
			"`PROVEO_SBX_MEMORY=16g` (capped at 32g) and see %s", path)
		return
	}
	if restarted {
		// `sbx exec` starts a stopped sandbox, so this reading is a FRESH boot:
		// its dmesg is empty and its meminfo is idle. Silence here is not
		// evidence of no kill — it is evidence the kill's boot is gone.
		ui.Notef("memory logs: %s — the sandbox had already stopped, so this is a fresh boot; "+
			"a kill in the boot that died left no trace to read", path)
		return
	}
	ui.Storef("memory logs: %s", path)
}

func Selected(man manifest.Manifest) bool {
	if !man.IsSbx() || !Enabled() {
		return false
	}
	ok, _ := sbx.Available()
	return ok
}

var storedSecretNames = sbx.StoredSecretNames

func agentCredentials(in Input, secrets [][2]string) ([]sbx.KitCredential, []string, [][2]string) {
	var (
		creds   []sbx.KitCredential
		domains []string
	)
	stored := map[string]bool{}
	for _, n := range storedSecretNames() {
		stored[n] = true
	}
	seen := map[string]bool{}
	for _, name := range provider.Detect(in.Lookup) {
		if seen[name] || !in.Man.Capabilities.AllowsProvider(name) {
			continue
		}
		r, ok := provider.ResolveWith(name, in.AuthVar, in.Lookup)
		// !ok is a signed-request provider (AWS SigV4 and friends): nothing to
		// attach as a header, so nothing to proxy-manage.
		if !ok || r.EnvVar == "" || len(r.Hosts) == 0 {
			continue
		}
		if r.Header == "" || r.Query != "" {
			continue
		}
		// Nothing stored under the service name: sbx would ask a human, and an
		// unattended run stops there.
		if !stored[name] {
			continue
		}
		format := "%s"
		if r.Bearer {
			format = "Bearer %s"
		}
		// SPEC-v2 requires every inject domain to also appear in
		// permissions.network.allow, so it has to be the SAME translated form —
		// a domain sbx cannot match is a credential sbx cannot attach.
		inject := make([]sbx.KitCredInject, 0, len(r.Hosts)*2)
		for _, h := range r.Hosts {
			for _, d := range sbx.DomainPatterns(h) {
				inject = append(inject, sbx.KitCredInject{Domain: d, Header: r.Header, Format: format})
				domains = append(domains, d)
			}
		}
		creds = append(creds, sbx.KitCredential{
			Service: name,
			APIKey: &sbx.KitCredAPIKey{
				Name:         r.EnvVar,
				ProxyManaged: true,
				Inject:       inject,
			},
		})
		seen[name] = true
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].Service < creds[j].Service })
	return creds, domains, secrets
}

// customSecretTarget answers whether a credential needs `sbx secret set-custom`
// rather than a plain service secret, and with which hosts.
//
// sbx injects a plain service secret only for the thirteen services it has
// built-in knowledge of. For any other provider it holds the value and knows
// neither the host to match nor the header to write, which is why a stored
// `opencode` secret still produced HTTP 401 while the same key forwarded fine.
// A custom secret carries the hosts and the variable with it, so the proxy can
// substitute at egress the way it does for a service it knows.
func customSecretTarget(envVar string, lookup func(string) string) (hosts []string, service string, ok bool) {
	for _, name := range provider.Names() {
		r, found := provider.ResolveWith(name, envVar, lookup)
		if !found || !strings.EqualFold(r.EnvVar, envVar) || len(r.Hosts) == 0 {
			continue
		}
		if sbx.IsBuiltinService(name) {
			return nil, name, false
		}
		for _, h := range r.Hosts {
			hosts = append(hosts, sbx.DomainPatterns(h)...)
		}
		return hosts, name, len(hosts) > 0
	}
	return nil, "", false
}

// sandboxAgentEnv keeps the pairs a sandbox can honour and drops the ones that
// name a proxy it does not have.
func sandboxAgentEnv(pairs []string) []string {
	var out []string
	for _, kv := range pairs {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || proxyOnlyVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func proxyOnlyVar(name string) bool {
	switch name {
	case "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
		"NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
		"SSL_CERT_FILE", "GIT_SSL_CAINFO", "INSPECT_PROXY", "ENFORCEMENT_PROXY",
		"PROVEO_EGRESS_CA_CERT":
		return true
	}
	return false
}

// startupCommands is the seed, plus the def's own entrypoint for a mixin kit.
func startupCommands(target string, ownAgent bool) []sbx.KitCommand {
	cmds := []sbx.KitCommand{sbx.SeedCommand(target)}
	if !ownAgent {
		cmds = append(cmds, sbx.SeedEntrypointCommand())
	}
	return cmds
}

// gitSafeDirectoryEnv declares the repository root safe for git, in the env the agent inherits.
func gitSafeDirectoryEnv(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	root := repoRoot
	if r, err := filepath.EvalSymlinks(repoRoot); err == nil {
		root = r
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=" + root,
	}
}

// launchConfigEnv renders the def's model wiring in the form the agent reads
// at launch, for a def whose entrypoint sbx does not run before the agent.
func launchConfigEnv(agent string, agentEnv []string) []string {
	if agent != "opencode" {
		return nil
	}
	model, base := envValue(agentEnv, "PROVEO_LOCAL_MODEL"), envValue(agentEnv, "OLLAMA_API_BASE")
	if model == "" {
		return nil
	}
	if base == "" {
		base = "http://ollama:11434"
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"ollama": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "Ollama (local)",
				"options": map[string]any{"baseURL": strings.TrimRight(base, "/") + "/v1", "apiKey": "ollama"},
				"models":  map[string]any{model: map[string]any{"name": model + " (local)"}},
			},
		},
		"model":       "ollama/" + model,
		"small_model": "ollama/" + model,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	return []string{
		"OPENCODE_CONFIG_CONTENT=" + string(b),
		"OPENCODE_MODEL=ollama/" + model,
		"OPENCODE_SMALL_MODEL=ollama/" + model,
	}
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// scopedGitIndexEnv hides the repository paths a subproject scope does not
// mount, so `git status` in the sandbox does not report them as deleted.
// scopedGitIndexEnv names a host-built index that hides the repository paths a scope does not mount.
func scopedGitIndexEnv(in Input, binds []sbx.Mount) []string {
	if in.ScopeRel == "" || in.RepoRoot == "" || in.Sid == "" {
		return nil
	}
	home := stateHomeHost(binds)
	src := filepath.Join(in.RepoRoot, ".git", "index")
	if home == "" || !exists(src) {
		return nil
	}
	dst := filepath.Join(home, "git-index", in.Sid)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return nil
	}
	var hidden []string
	for _, rel := range trackedFiles(in.RepoRoot) {
		if !mountedHost(filepath.Join(in.RepoRoot, rel), binds) {
			hidden = append(hidden, rel)
		}
	}
	if len(hidden) > 0 {
		cmd := exec.Command("git", append([]string{"-C", in.RepoRoot, "update-index", "--skip-worktree", "--"}, hidden...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+dst)
		if err := cmd.Run(); err != nil {
			return nil
		}
	}
	return []string{"GIT_INDEX_FILE=" + dst}
}

func stateHomeHost(binds []sbx.Mount) string {
	for _, m := range binds {
		if m.Container == proveohome.ContainerHome {
			return m.Host
		}
	}
	return ""
}

func trackedFiles(repoRoot string) []string {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func mountedHost(path string, binds []sbx.Mount) bool {
	p := filepath.Clean(path)
	for _, m := range binds {
		h := filepath.Clean(m.Host)
		if p == h || strings.HasPrefix(p, h+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// launchEnv is everything the agent must already hold when sbx starts it: the
// plan's decision, the workdir, git's reach across the mount boundary and the
// repository's safety, the def's model wiring, and the scoped index.
func launchEnv(in Input, agent string, env []string, mounts []sbx.Mount) []string {
	binds := WorkspaceBinds(mounts)
	out := append([]string{}, env...)
	out = append(out, sandboxAgentEnv(in.AgentEnv)...)
	out = append(out, "PROVEO_WORKDIR="+FirstHost(binds), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1")
	out = append(out, gitSafeDirectoryEnv(in.RepoRoot)...)
	out = append(out, launchConfigEnv(agent, in.AgentEnv)...)
	out = append(out, scopedGitIndexEnv(in, binds)...)
	return out
}

// reuseOrCreate answers the collision proveo creates for itself: the sandbox
// name is derived from def and workspace so runs reuse one VM, and a failed run
// is KEPT for diagnosis — so the next run met a sandbox sbx will not give new
// workspaces to and stopped before the agent started.
//
// It needs none. That name can only exist for THIS workspace, so the existing
// sandbox reads its Kit, environment, agent and workspace from its stored spec.
// Re-attaching passes only the name plus trailing agent arguments; the full cfg
// remains available to preserve clone and agent state after the child exits.
// SPEC: _spec/internal/sbx/kit-lifecycle.puml
func reuseOrCreate(cfg sbx.RunConfig, exists func(string) bool) sbx.RunConfig {
	if !exists(cfg.Name) {
		return cfg
	}
	ui.Notef("re-attaching to %s, which an earlier run left here — its workspace is this one", cfg.Name)
	ui.Notef("for a sandbox built fresh instead: `sbx rm --force %s`", cfg.Name)
	return sbx.Reattach(cfg)
}
