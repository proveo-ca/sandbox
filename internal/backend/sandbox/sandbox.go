// SPEC: _spec/internal/sbx/sandbox-backend.puml,
// _spec/internal/sbx/sandbox-backend.puml Package sandbox is the sbx backend:
// it PLANS a run (Spec) and it EXECUTES one (Run).
//
// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/internal/sbx/sandbox-backend.puml
package sandbox

import (
	"bytes"
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

// EvidenceVar and unallowlistedProbe are this backend's own: the first is the
// name it writes into the Kit environment, the second the host it asks sbx
// about to learn whether the daemon's baseline allows everything.
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

// SPEC: _spec/internal/sbx/clone-workspace.puml
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
	fetch := exec.Command("git", sbx.CloneFetchArgs(in.RepoRoot, cfg.Name)...)
	if out, err := fetch.CombinedOutput(); err != nil {
		ui.Warnf("clone: fetch from %s failed (%v): %s — if the sandbox still exists, `git fetch %s` by hand",
			sbx.CloneRemote(cfg.Name), err, strings.TrimSpace(string(out)), sbx.CloneRemote(cfg.Name))
		return
	}
	refs, _ := exec.Command("git", "-C", in.RepoRoot, "for-each-ref", "--format=%(refname:short)", sbx.CloneRefs(cfg.Name)+"/").Output()
	names := strings.Fields(string(refs))
	if len(names) == 0 {
		ui.Notef("clone: the agent left no branches to fetch")
		return
	}
	ui.Section(ui.SectionResults)
	ui.Storef("clone: the agent's work is in your repository under %s/ — %s", sbx.CloneRefs(cfg.Name), strings.Join(names, " "))
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
	Bridges                provider.BridgeTable
	Evidence               string // was params.evidenceOrDefault()
	Forwards               bool   // was params.forwards()
	Man                    manifest.Manifest
	Sid, EgDir             string
	Mounts                 []runner.Mount
	Workdir                string
	Lookup                 func(string) string
	Detected               []string
	GitEnv                 []string
	HomeEnv                []string
	BridgeEnv              []string
	ScopeRel               string
	WorktreeFallback       bool
	WorktreeEnv            []string
	DataDir                string
	Memory                 string
	CPUs                   int
	HomeRoot               string
	RunLog                 string
}

func Spec(in Input) (sbx.RunConfig, sbx.Kit, [][2]string) {
	hosts := map[string]bool{}
	for _, d := range strings.Fields(credentials.JoinDomains(os.Getenv("PROVEO_EGRESS_PROVIDER_DOMAINS"), in.Man.Capabilities.Hosts)) {
		hosts[d] = true
	}
	for _, h := range credentials.ReachableHosts(in.Detected) {
		hosts[h] = true
	}
	allow := make([]string, 0, len(hosts))
	for h := range hosts {
		allow = append(allow, h)
	}
	sort.Strings(allow)

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
		secrets = append(secrets, [2]string{name, v})
	}
	forwards := in.Forwards
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
	suppressedAuth := credentials.AuthSuppressor(in.Man, in.Target, in.AuthVar, in.HomeRoot)
	for _, e := range in.Man.Env {
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
	for _, k := range provider.KeyVars() {
		if !in.Man.Capabilities.AllowsProvider(credentials.ProviderOfKeyVar(k)) {
			continue
		}
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
	// SPEC: _spec/_plans/config-seeding-and-persistence.puml
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

	var mounts []sbx.Mount
	for _, m := range in.Mounts {
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

	agent, launch := sbx.AgentFor(in.Target)
	command := in.Extra
	if len(command) == 0 {
		command = launch
	}
	if in.Shell {
		command, agent = nil, sbx.ShellAgent
	}
	cfg := sbx.RunConfig{
		Name:    in.Sid,
		KitDir:  filepath.Join(in.EgDir, "sbx", "kit"),
		Image:   in.Image,
		Memory:  in.Memory,
		CPUs:    in.CPUs,
		Clone:   in.Clone,
		Publish: cdpPublish(in),
		Agent:   agent,
		Mounts:  WorkspaceBinds(mounts),
		Env: DeclineMCPGateway(Home(append(append(env, ResolvedModelEnv(in)...),
			"PROVEO_WORKDIR="+FirstHost(WorkspaceBinds(mounts))), mounts)),
		Command: command,
	}
	kit := sbx.Kit{
		SchemaVersion: sbx.KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          in.Target + "-posture",
		DisplayName:   "proveo posture (" + in.Target + ")",
		Description:   "Reachability, host-resolved environment and the seed step for a proveo run.",
		Permissions:   sbx.KitPermissions{Network: sbx.KitNet{Allow: allow}},
		Environment:   &sbx.KitEnv{Variables: WithMCPGatewayPolicy(KitEnvVars(cfg.Env))},
		Setup:         &sbx.KitSetup{Startup: []sbx.KitCommand{sbx.SeedCommand(in.Target)}},
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

func ResolvedModelEnv(in Input) []string {
	var out []string
	for k, v := range in.Bridges.ResolvedEnv(in.Target, in.Roles) {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // a Kit is written to disk and diffed; order must not churn
	return out
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
	cfg, kit, secrets := Spec(in)
	if _, err := sbx.WriteKit(cfg.KitDir, kit); err != nil {
		return err
	}
	ui.Section(ui.SectionStarting)
	if err := sbx.EnsureTemplate(cfg.Image, func(f string, a ...any) {
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
		ui.Hostf("sandbox secret: %s (host-side injection)", kv[0])
		if err := sbx.SecretSet(kv[0], kv[1]); err != nil {
			return fmt.Errorf("sandbox secret %s: %w", kv[0], err)
		}
	}
	if len(secrets) > 0 {
		ui.Notef("sbx's secret store is host-wide and outlives this run — `sbx secret ls`")
	}
	defer StartCDPViewport(in, cfg)()
	args := sbx.RunArgs(cfg)
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
			runErr = run()
			endedAt = time.Now()
		}
	}
	defer func() {
		CapturePolicyLog(in.EgDir, cfg.Name)
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
			restarted := !sbx.Running(cfg.Name)
			PreserveClone(in, cfg)
			_, _ = SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun)
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
			return
		}
		PreserveClone(in, cfg)
		if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
			ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
		}
		rmOut, rmErr := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput()
		if rmErr != nil && !sbx.NotFound(string(rmOut)) {
			ui.Warnf("sandbox teardown failed (%v): %s", rmErr, strings.TrimSpace(string(rmOut)))
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

func Selected(man manifest.Manifest) bool {
	if !man.IsSbx() || !Enabled() {
		return false
	}
	ok, _ := sbx.Available()
	return ok
}
