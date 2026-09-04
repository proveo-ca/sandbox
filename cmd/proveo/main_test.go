package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestPickProject(t *testing.T) {
	t.Parallel()
	projs := []workspace.Project{
		{Name: "web", Path: "apps/web"},
		{Name: "util", Path: "packages/util"},
	}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "first choice", input: "1\n", want: "apps/web"},
		{name: "second choice", input: "2\n", want: "packages/util"},
		{name: "zero is repo root", input: "0\n", want: ""},
		{name: "empty is repo root", input: "\n", want: ""},
		{name: "out of range is repo root", input: "9\n", want: ""},
		{name: "garbage is repo root", input: "xyz\n", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pickProject(projs, strings.NewReader(tc.input), &strings.Builder{})
			if got != tc.want {
				t.Errorf("pickProject(input=%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// D2 seams — the gating/dispatch/assembly logic that was untestable inside the
// old god-function.

func TestAssembleAndDispatch(t *testing.T) {
	t.Parallel()

	t.Run("open+forward: no lifecycle, bare agent", func(t *testing.T) {
		t.Parallel()
		plan, agent, err := dockeregress.Assemble(dockeregress.Input{
			Target:      "opencode",
			Image:       "img",
			Mode:        "open",
			Credentials: "forward",
			Sid:         "s", EgDir: "/st", UID: "1000", GID: "1000",
			PidsLimit: 4096,
		})
		if err != nil {
			t.Fatal(err)
		}
		if dockeregress.NeedsLifecycle(plan) {
			t.Error("firewall (no model) must not need the lifecycle")
		}
		if agent.Image != "img" || agent.User != "1000:1000" || !agent.Interactive {
			t.Errorf("agent config wrong: %+v", agent)
		}
		if agent.PidsLimit != 4096 {
			t.Errorf("agent.PidsLimit = %d, want 4096", agent.PidsLimit)
		}
		if strings.Join(agent.ExtraArgs, " ") != strings.Join(plan.AgentArgs, " ") {
			t.Errorf("agent.ExtraArgs must be the plan's AgentArgs")
		}
	})

	t.Run("firewall + provider: full topology through the lifecycle", func(t *testing.T) {
		t.Parallel()
		plan, _, err := dockeregress.Assemble(dockeregress.Input{
			Target: "claudecode",
			Image:  "img",
			Mode:   "firewall",
			Sid:    "s", EgDir: "/st", UID: "1000", GID: "1000",
			Providers: []string{"anthropic"}, BrokerFile: "/st/inject/broker.env",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !dockeregress.NeedsLifecycle(plan) {
			t.Error("firewall must go through the lifecycle")
		}
		if !plan.UsesSquid || plan.CAWaitPath == "" {
			t.Errorf("firewall plan should use squid + set CAWaitPath: %+v", plan)
		}
	})

	t.Run("firewall + local model: lifecycle via the ollama sidecar", func(t *testing.T) {
		t.Parallel()
		plan, _, err := dockeregress.Assemble(dockeregress.Input{
			Target:     "opencode",
			Image:      "img",
			Mode:       "broker",
			LocalModel: "gemma4",
			Sid:        "s", EgDir: "/st", UID: "1000", GID: "1000",
			ModelsDir: "/models",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !dockeregress.NeedsLifecycle(plan) {
			t.Error("firewall + --local-model must need the lifecycle (ollama sidecar)")
		}
		if plan.OllamaContainer == "" {
			t.Error("local-model plan must set OllamaContainer")
		}
	})

	t.Run("shell + data-dir affect the agent config", func(t *testing.T) {
		t.Parallel()
		_, agent, err := dockeregress.Assemble(dockeregress.Input{
			Target:  "opencode",
			Image:   "img",
			Mode:    "broker",
			DataDir: "/data",
			Shell:   true,
			Sid:     "s", EgDir: "/st", UID: "1", GID: "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if agent.Entrypoint != "bash" {
			t.Errorf("--shell must set Entrypoint=bash, got %q", agent.Entrypoint)
		}
		var found bool
		for _, m := range agent.Mounts {
			if m.Host == "/data" && m.Container == "/workspace/data" && m.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Errorf("--data-dir must add a read-only /workspace/data mount: %+v", agent.Mounts)
		}
	})

	t.Run("declared env is forwarded by bare name, never as KEY=VALUE", func(t *testing.T) {
		t.Parallel()
		_, agent, err := dockeregress.Assemble(dockeregress.Input{
			Target: "cursor",
			Image:  "img",
			Mode:   "broker",
			Sid:    "s", EgDir: "/st", UID: "1", GID: "1",
			Env: []string{"CURSOR_API_KEY"},
		})
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(runner.DockerRunArgs(agent), " ")
		if !strings.Contains(argv, "-e CURSOR_API_KEY") {
			t.Errorf("argv must forward the declared env by name: %s", argv)
		}
		if strings.Contains(argv, "CURSOR_API_KEY=") {
			t.Errorf("argv must never contain the env value: %s", argv)
		}
	})

	t.Run("firewall sentinel + broker mount from host .env key", func(t *testing.T) {
		t.Parallel()
		plan, agent, err := dockeregress.Assemble(dockeregress.Input{
			Target: "cursor",
			Image:  "img",
			Mode:   "firewall",
			Sid:    "s", EgDir: "/st", UID: "1", GID: "1",
			Providers: []string{"cursor"}, BrokerFile: "/st/inject/broker.env",
			Env: []string{
				"CURSOR_API_KEY=" + entrypoint.DefaultSentinel,
				"PROVEO_CREDENTIAL_BROKER_KEYS=CURSOR_API_KEY",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Join(runner.DockerRunArgs(agent), " ")
		if !strings.Contains(argv, "CURSOR_API_KEY="+entrypoint.DefaultSentinel) {
			t.Errorf("firewall agent must get sentinel CURSOR_API_KEY: %s", argv)
		}
		if !strings.Contains(argv, "PROVEO_CREDENTIAL_BROKER_KEYS=CURSOR_API_KEY") {
			t.Errorf("firewall agent must get broker key list: %s", argv)
		}
		sidecar := strings.Join(flattenSidecars(plan), " ")
		if !strings.Contains(sidecar, "PROVEO_EGRESS_PROVIDERS=cursor") {
			t.Errorf("proxy must pin cursor: %s", sidecar)
		}
		if !strings.Contains(sidecar, "/broker:ro") {
			t.Errorf("proxy must mount broker dir: %s", sidecar)
		}
	})

	t.Run("unknown mode errors", func(t *testing.T) {
		t.Parallel()
		if _, _, err := dockeregress.Assemble(dockeregress.Input{
			Mode: "nope", Sid: "s", EgDir: "/st"}); err == nil {
			t.Error("dockeregress.Assemble with an unknown mode must error")
		}
	})
}

func flattenSidecars(p egress.Plan) []string {
	var out []string
	for _, c := range p.Sidecars {
		out = append(out, c...)
	}
	return out
}

// C6 regression: only the agent's own exit propagates as a bare exit code.
// A failed helper subprocess (docker pull, build.sh) also wraps an
// *exec.ExitError, and swallowing it would exit silently — it must NOT match
// the agent-exit type.
func TestAgentExitDiscrimination(t *testing.T) {
	t.Parallel()
	var ae backend.ExitError

	if !errors.As(error(backend.ExitError{Code: 42}), &ae) || ae.Code != 42 {
		t.Errorf("backend.ExitError must match itself and carry the code, got %+v", ae)
	}

	// A real wrapped ExitError, as a failed `docker pull` produces.
	cmdErr := exec.Command("false").Run()
	var ee *exec.ExitError
	if !errors.As(cmdErr, &ee) {
		t.Fatalf("exec false should produce an ExitError, got %v", cmdErr)
	}
	wrapped := fmt.Errorf("image unavailable: x (pull failed: %w)", cmdErr)
	if errors.As(wrapped, &ae) {
		t.Error("a wrapped helper ExitError must not be treated as the agent's exit")
	}
}

func TestProviderDetectFromInvocationDotEnv(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	root := t.TempDir()
	scope := filepath.Join(root, "scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=from-pwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostEnv := workspace.EnvFileSource(root, scope, "")
	lookup := credentials.ProviderLookup(hostEnv)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "cursor" {
		t.Fatalf("Detect(lookup from pwd .env) = %v, want [cursor]", detected)
	}
}

func TestInitAdvertisesOnlyRegisteredKeys(t *testing.T) {
	t.Parallel()
	known := map[string]bool{}
	for _, name := range provider.Names() {
		e, _ := provider.Lookup(name)
		for _, k := range e.Detect {
			known[k] = true
		}
	}
	for _, k := range initProviderKeys {
		if !known[k] {
			t.Errorf("proveo --init offers %q but no provider registers it — it can never be used", k)
		}
	}
}

// The egress sidecar gets the same recency rule as the harness image.
func TestEgressProxyImagePrefersANewerLocalBuildUnlessOverridden(t *testing.T) {
	t.Parallel()
	resolve := func(ref string) (string, bool) {
		if ref != "proveo/egress-proxy:latest" {
			t.Errorf("resolved %q, want the published sidecar reference", ref)
		}
		return "proveo/egress-proxy:local", true
	}
	env := func(map[string]string) func(string) string {
		return func(string) string { return "" }
	}
	if got, local := egressProxyImage(env(nil), resolve); got != "proveo/egress-proxy:local" || !local {
		t.Errorf("no override: got (%q, %v), want the newer local build", got, local)
	}
	override := func(k string) string {
		if k == "PROVEO_EGRESS_PROXY_IMAGE" {
			return " ghcr.io/acme/egress:pinned "
		}
		return ""
	}
	notCalled := func(string) (string, bool) {
		t.Error("an explicit override must not consult the local image store")
		return "", false
	}
	if got, local := egressProxyImage(override, notCalled); got != "ghcr.io/acme/egress:pinned" || local {
		t.Errorf("override: got (%q, %v), want the trimmed override and not-local", got, local)
	}
}

func TestReviewSupportedRequiresLinuxAndALocalDaemon(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }
	ok, why := dockeregress.ReviewSupported(none)
	if runtime.GOOS == "linux" {
		if !ok {
			t.Errorf("linux host reported unsupported: %q", why)
		}
	} else if ok || why != "linux only" {
		t.Errorf("non-linux host = (%v, %q), want (false, \"linux only\")", ok, why)
	}

	remote := func(k string) string {
		if k == "DOCKER_HOST" {
			return "tcp://10.0.0.5:2375"
		}
		return ""
	}
	if ok, why := dockeregress.ReviewSupported(remote); ok {
		t.Error("a remote daemon must be unsupported: the bind mount lands on another machine")
	} else if runtime.GOOS == "linux" && why != "needs a local docker daemon" {
		t.Errorf("remote daemon reason = %q, want it to name the daemon", why)
	}

	local := func(k string) string {
		if k == "DOCKER_HOST" {
			return "unix:///var/run/docker.sock"
		}
		return ""
	}
	if ok, _ := dockeregress.ReviewSupported(local); ok != (runtime.GOOS == "linux") {
		t.Errorf("a local unix daemon should track GOOS, got %v on %s", ok, runtime.GOOS)
	}
}

func TestSandboxSpecShellOverridesCommandAndAddsDataDir(t *testing.T) {
	// A real directory: an sbx workspace must BE one, so the spec drops binds
	// that are not (the project .env arrives as a file bind and sbx refuses it).
	dataDir := t.TempDir()
	in := sandbox.Input{
		Target:   "claudecode",
		Image:    "proveo/claudecode:latest",
		Shell:    true,
		Forwards: false,
		Man:      manifest.Manifest{Name: "claudecode"},
		Lookup:   func(string) string { return "" },
		Workdir:  "/app",
		DataDir:  dataDir,
	}
	cfg, _, secrets := sandbox.Spec(in)
	// --shell selects sbx's OWN shell agent; it does not pass a command. Launch-shaped
	// work belongs to the built-in agent, so the earlier expectation here — Command
	// == [bash] — described something sbx never honoured: it started the harness's
	// agent and handed "bash" to it as an argument, and the shell never opened.
	if cfg.Agent != sbx.ShellAgent {
		t.Errorf("shell mode agent = %q, want %q", cfg.Agent, sbx.ShellAgent)
	}
	if len(cfg.Command) != 0 {
		t.Errorf("shell mode command = %v, want none — the agent IS the shell", cfg.Command)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none without credentials", secrets)
	}
	found := false
	for _, m := range cfg.Mounts {
		if m.Host == dataDir && m.Container == "/workspace/data" && !m.ReadOnly {
			t.Errorf("data dir mount must be read-only: %+v", m)
		}
		if m.Host == dataDir && m.Container == "/workspace/data" && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Errorf("data dir mount missing from %+v", cfg.Mounts)
	}
	// There is no Workdir on an sbx run — the CLI has no -w and mounts each
	// workspace at its own HOST path, so where the harness landed is conveyed in
	// the environment instead.
	var sawWorkdir bool
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "PROVEO_WORKDIR=") {
			sawWorkdir = true
		}
	}
	if !sawWorkdir {
		t.Errorf("PROVEO_WORKDIR missing from %+v", cfg.Env)
	}
}

// sandbox.Spec never reads p.mode, so open and allowlist build an identical Kit. The
// row must say so rather than offering a risk axis on which nothing moves.
func TestKeptSandboxLinesNamesTheRunLog(t *testing.T) {
	t.Parallel()
	const name, log = "proveo-1-2", "/home/u/.proveo/logs/proveo-1-2.log"

	got := sandbox.KeptLines(name, log)
	if len(got) != 2 {
		t.Fatalf("want the kept line plus the transcript, got %d: %q", len(got), got)
	}
	// The first line stays the actionable one: it is what an operator reads first.
	for _, want := range []string{"kept for diagnosis", "sbx exec " + name, "sbx rm --force " + name} {
		if !strings.Contains(got[0], want) {
			t.Errorf("first line dropped %q: %q", want, got[0])
		}
	}
	if !strings.Contains(got[1], log) {
		t.Errorf("the transcript path must be named verbatim, got %q", got[1])
	}

	// No transcript is a real state — runlog.Open failing must not print a line
	// pointing at the empty string, which reads as a path that got lost.
	if got := sandbox.KeptLines(name, "  "); len(got) != 1 {
		t.Errorf("a run with no transcript gets one line, got %q", got)
	}
}

// The transcript is written into sandbox-LOCAL volumes, and the copy-out used to
// sit on the SUCCESS path only — so agentTranscript searched a host home the failed
// session had never written to and reported "no evidence" on every failed sbx run,
// however much the agent had said before it died. Both exits take the same copy-out
// now, which is what this pins.
func TestSaveSandboxStateCopiesOutWhenThereAreVolumes(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/home/u", sbx.StateHomeVar + "=/home/u/.proveo"}

	var calls [][]string
	run := func(args ...string) (string, error) {
		calls = append(calls, args)
		return "ok", nil
	}
	if _, err := sandbox.SaveState("proveo-1-2", env, true, run); err != nil {
		t.Fatalf("copy-out failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("want one sbx call, got %d: %v", len(calls), calls)
	}
	// The argv is sbx's, not this test's: asserting it here is what catches the
	// save being reworded into something the entrypoint lib no longer defines.
	if want := sbx.SaveStateArgs("proveo-1-2"); !slices.Equal(calls[0], want) {
		t.Errorf("argv drifted\n got %q\nwant %q", calls[0], want)
	}

	// Every reason there is nothing to copy is a no-op, NOT an error. A docker run
	// keeps its home on the host, and a run that died before sbx created anything
	// has no volumes to read — reporting either as "resume state not preserved"
	// warns about work that was never owed.
	for _, tc := range []struct {
		why    string
		name   string
		env    []string
		exists bool
	}{
		{"docker backend: no state home", "proveo-1-2", []string{"HOME=/home/u"}, true},
		{"sandbox never created", "proveo-1-2", env, false},
		{"no sandbox name", "", env, true},
	} {
		calls = nil
		out, err := sandbox.SaveState(tc.name, tc.env, tc.exists, run)
		if err != nil || out != "" {
			t.Errorf("%s: want a silent no-op, got (%q, %v)", tc.why, out, err)
		}
		if len(calls) != 0 {
			t.Errorf("%s: must not reach sbx, got %v", tc.why, calls)
		}
	}

	// A failed copy-out is reported to the caller rather than swallowed: the
	// success path warns on it, and the failure path deliberately does not.
	boom := func(...string) (string, error) { return "no such sandbox", errors.New("exit 1") }
	if out, err := sandbox.SaveState("proveo-1-2", env, true, boom); err == nil || out == "" {
		t.Errorf("want the error and its output surfaced, got (%q, %v)", out, err)
	}
}

func TestSandboxSpecReadsTheHomeRootFromItsInput(t *testing.T) {
	t.Parallel()
	lookup := func(k string) string {
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
			"ANTHROPIC_API_KEY":       "key-value",
		}[k]
	}
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	base := sandbox.Input{
		Target:   "claudecode",
		Forwards: false,
		Man:      man, Sid: "s", Lookup: lookup}

	names := func(in sandbox.Input) map[string]bool {
		_, _, secrets := sandbox.Spec(in)
		out := map[string]bool{}
		for _, kv := range secrets {
			out[kv[0]] = true
		}
		return out
	}

	// No home, so no login can be found: both credentials are stored, as before.
	noHome := names(base)
	if !noHome["ANTHROPIC_API_KEY"] {
		t.Errorf("without a host login the API key must still be stored, got %v", noHome)
	}

	// A home carrying a login makes THE FILE the credential, so nothing is stored for
	// that provider. This assertion used to read the other way — a login meant the
	// harness's own token was stored — and that is what put an env token in front of
	// the mounted login and authenticated a subscription run as the API.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude/.credentials.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withHome := base
	withHome.HomeRoot = home
	got := names(withHome)
	if got["ANTHROPIC_API_KEY"] {
		t.Errorf("a host login must suppress the competing API key, got %v", got)
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] {
		t.Errorf("an env token was stored over a mounted login; it overrides it rather than joining it, got %v", got)
	}
	if len(got) != 0 {
		t.Errorf("the mounted login needs no brokered secret at all, got %v", got)
	}
}

func TestSbxBackendSetsNeitherHome(t *testing.T) {
	mounts := []sbx.Mount{{Host: "/Users/p/.proveo", Container: proveohome.ContainerHome}}
	got := sandbox.Home([]string{"HOME=/stale", "PROVEO_HOME=/stale", "KEEP=1"}, mounts)

	for _, e := range got {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "PROVEO_HOME=") {
			t.Errorf("the sbx kit must carry no home redirect, got %q in %v", e, got)
		}
	}
	// Everything unrelated survives untouched.
	if !slices.Contains(got, "KEEP=1") {
		t.Errorf("unrelated environment must pass through, got %v", got)
	}
	// The redirect is gone, but the persistence it bought is not: the host path is
	// published as a POINTER the seed and teardown copy state through. Without it
	// resume state stays in the volumes sbx destroys with the sandbox.
	if !slices.Contains(got, sbx.StateHomeVar+"=/Users/p/.proveo") {
		t.Errorf("the host path for resume state must be published, got %v", got)
	}
}

// The pointer is only meaningful when there IS a proveo home to copy into.
func TestSbxStateHomeAbsentWithoutAProveoHome(t *testing.T) {
	t.Parallel()
	got := sandbox.Home([]string{"KEEP=1"}, []sbx.Mount{{Host: "/w", Container: "/w"}})
	if sandbox.StateHome(got) != "" {
		t.Errorf("no proveo home mount means no state pointer, got %v", got)
	}
}

// The save must run while the sandbox still exists, and must name the sandbox it
// is lifting state out of.
func TestSaveStateArgsTargetTheSandbox(t *testing.T) {
	t.Parallel()
	got := sbx.SaveStateArgs("s1")
	if got[0] != "exec" || !slices.Contains(got, "s1") {
		t.Errorf("save must exec inside the named sandbox, got %v", got)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "proveo_sync_state save") {
		t.Errorf("save must call the shared sync, not a second copy of the dir list: %v", got)
	}
	// Toolchains are installed on the VM's own disk and only reach the operator
	// here, so a teardown that forgets them throws away everything the run
	// provisioned. SPEC: _spec/_plans/config-seeding-and-persistence.puml
	if !strings.Contains(joined, "proveo_sync_tools save") {
		t.Errorf("teardown must also carry the toolchain tree out: %v", got)
	}
	// Joined with `;`, not `&&`: a failed transcript copy must not take the
	// toolchains with it.
	if strings.Contains(joined, "proveo_sync_tools save && proveo_sync_state") {
		t.Errorf("the two syncs must not be chained on success: %v", got)
	}
	// `-w /` is not decoration: a virtiofs-invalidated workspace kills the exec at
	// chdir. SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
	w := slices.Index(got, "-w")
	if w < 0 || w+1 >= len(got) || got[w+1] != "/" || w > slices.Index(got, "s1") {
		t.Errorf("save must exec from / (an sbx exec flag, before the sandbox name), not from the workspace cwd: %v", got)
	}
}

// With no proveo home among the mounts there is nothing to strip against, and the
// environment must pass through exactly as given.
func TestSbxHomeLeavesUnrelatedRunsAlone(t *testing.T) {
	got := sandbox.Home([]string{"HOME=/keep", "KEEP=1"}, []sbx.Mount{{Host: "/w", Container: "/w"}})
	if len(got) != 2 {
		t.Errorf("want the environment untouched when no proveo home is mounted, got %v", got)
	}
}

func TestSandboxSpecUsesTheHarnessAgentUnlessShellIsAsked(t *testing.T) {
	t.Parallel()
	base := sandbox.Input{
		Man:    manifest.Manifest{Name: "claudecode"},
		Lookup: func(string) string { return "" },
	}
	for _, c := range []struct {
		target, want string
		shell        bool
	}{
		{target: "claudecode", want: "claude"},
		{target: "cursor", want: "cursor"},
		{target: "claudecode", want: sbx.ShellAgent, shell: true},
		{target: "cursor", want: sbx.ShellAgent, shell: true},
	} {
		in := base
		in.Target, in.Image, in.Shell = c.target, "proveo/x:local", c.shell
		if cfg, _, _ := sandbox.Spec(in); cfg.Agent != c.want {
			t.Errorf("target %q shell=%v: agent = %q, want %q", c.target, c.shell, cfg.Agent, c.want)
		}
	}
}

// A suppressed credential is OMITTED, never stated as empty — the same as the
// docker path.
//
// This test asserted the opposite, on the theory that sbx's global secret store
// would inject a stale value in front of the mounted login. Probed against sbx
// v0.39.0 with ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN both in the global
// store and no -e flag: both arrive UNSET. sbx attaches service secrets as proxy
// headers and writes the harness credential to ~/.claude/.credentials.json; it
// never exports them as environment variables, so there was nothing to override.
//
// The empty value was the failure, not the guard. An agent reads a SET variable as
// a chosen credential whatever its value, and claudecode ranks both of these above
// the login on disk — so a blank one took the slot the login needed and left an
// unattended run at a prompt asking it to approve a key that authenticates nothing.
func TestSandboxSpecOmitsSuppressedCredentials(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	in := sandbox.Input{
		Target:   "claudecode",
		Image:    "proveo/claudecode:local",
		Forwards: false,
		Man: manifest.Manifest{
			Name: "claudecode", Subscription: true,
			Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
			Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
		},
		Lookup: func(k string) string {
			return map[string]string{
				"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
				"ANTHROPIC_API_KEY":       "key-value",
			}[k]
		},
		HomeRoot: home,
	}
	cfg, _, secrets := sandbox.Spec(in)

	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if slices.Contains(cfg.Env, k+"=") {
			t.Errorf("%s must be omitted, not stated empty — a blank value outranks the login it "+
				"was meant to defer to; env = %v", k, cfg.Env)
		}
		// Omission is the whole point: the variable must not appear at all, with or
		// without a value, so the login is the only credential the agent can see.
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, k+"=") {
				t.Errorf("%s reached the sandbox over a mounted login: %q", k, e)
			}
		}
	}
	if len(secrets) != 0 {
		t.Errorf("nothing may be written to the store for a file-backed login, got %v", secrets)
	}
}

func TestMCPGatewayIsDeclinedByDefault(t *testing.T) {
	t.Setenv("PROVEO_SBX_MCP", "")
	got := sandbox.WithMCPGatewayPolicy(map[string]string{"HOME": "/proveo-home"})
	v, ok := got[sandbox.MCPGatewayVar]
	if !ok || v != "" {
		t.Errorf("%s = %q (present=%v), want an explicit empty value — that is what makes sbx's `[ -n ... ] || exit 0` no-op", sandbox.MCPGatewayVar, v, ok)
	}
	if got["HOME"] != "/proveo-home" {
		t.Error("declining the gateway must not disturb the rest of the environment")
	}
	// A nil map is the ordinary case for a run that resolved no variables.
	if got := sandbox.WithMCPGatewayPolicy(nil); got[sandbox.MCPGatewayVar] != "" {
		t.Error("a nil environment must still carry the decline")
	}
}

func TestMCPGatewayCanBeAllowed(t *testing.T) {
	t.Setenv("PROVEO_SBX_MCP", "on")
	if got := sandbox.WithMCPGatewayPolicy(map[string]string{"HOME": "/proveo-home"}); len(got) != 1 {
		t.Errorf("PROVEO_SBX_MCP=on must leave the environment untouched, got %v", got)
	}
}

func TestRemediationHintsRunInEveryShell(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// A quoted hint that assigns an env var immediately before a command word,
	// with no `env` in front of it.
	bad := regexp.MustCompile(`[^a-zA-Z_](HOME|PATH|ANTHROPIC_[A-Z_]+|CLAUDE_[A-Z_]+)=%?[^ "]* +(claude|cursor|opencode|proveo|sbx)\b`)
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, `"`) {
			continue // only user-facing string literals matter
		}
		if strings.Contains(line, "env "+"HOME=") || strings.Contains(line, "`env ") {
			continue // already the portable form
		}
		if m := bad.FindString(line); m != "" {
			t.Errorf("hint uses the POSIX prefix form, which fish cannot run: %q\n  in: %s",
				strings.TrimSpace(m), strings.TrimSpace(line))
		}
	}
}

func TestWorktreeRepoRootIsMountedNotSynthesized(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := sandbox.WorkspaceBinds([]sbx.Mount{
		{Host: filepath.Join(repo, ".git"), Container: workspace.ContainerGitCommonDir},
	})
	if len(got) != 1 {
		t.Fatalf("want one bind, got %v", got)
	}
	if got[0].Host != repo {
		t.Errorf("sbx must mount the repo ROOT so the parent is a real bind:\n got %s\nwant %s",
			got[0].Host, repo)
	}
}

// The repo root can legitimately be a workspace already — mounting it twice is
// a duplicate positional path, not a second bind.
func TestWorkspaceBindsDoNotDuplicateTheRepoRoot(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := sandbox.WorkspaceBinds([]sbx.Mount{
		{Host: repo, Container: "/app"},
		{Host: filepath.Join(repo, ".git"), Container: workspace.ContainerGitCommonDir},
	})
	if len(got) != 1 {
		t.Errorf("the repo root was mounted twice: %v", got)
	}
}

// Read-only stays read-only: rewriting the path must not quietly widen the
// posture an operator asked for with a git-mode of "ro".
func TestRepoRootRewriteKeepsReadOnly(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := sandbox.WorkspaceBinds([]sbx.Mount{
		{Host: filepath.Join(repo, ".git"), Container: workspace.ContainerGitCommonDir, ReadOnly: true},
	})
	if len(got) != 1 || !got[0].ReadOnly {
		t.Errorf("read-only git mount lost its posture: %v", got)
	}
}

func TestStoreHoldsMatchesOnlyTheHarnessOwnCredentials(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Subscription: true,
		Env: []manifest.EnvVar{
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
			{Name: "ANTHROPIC_API_KEY", Secret: true},
			{Name: "PROVEO_AGENT_EVIDENCE"},
		},
	}
	stored := []string{
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CURSOR_API_KEY",
		"GEMINI_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY", "github", "anthropic",
	}
	got := credentials.StoreHolds(man, stored)
	want := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	if !slices.Equal(got, want) {
		t.Errorf("credentials.StoreHolds = %v, want %v", got, want)
	}
	if len(credentials.StoreHolds(man, nil)) != 0 {
		t.Error("an unreadable store must hold nothing, not everything")
	}
	if got := credentials.StoreHolds(man, []string{"CURSOR_API_KEY", "github"}); len(got) != 0 {
		t.Errorf("another harness's credentials read as this one's login: %v", got)
	}
	if got := credentials.StoreHolds(man, []string{"PROVEO_AGENT_EVIDENCE"}); len(got) != 0 {
		t.Errorf("a non-secret var counted as a credential: %v", got)
	}
}

// agentEnv is proveo's opinion about the agent, delivered on the backend whose
// agent never runs the image entrypoint. The default lands when the operator is
// silent, gives way when they are not, and reaches both the -e argv and the Kit's
// environment block — the posture proveo publishes.
func TestSandboxSpecHandsTheAgentItsManifestDefaults(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", AgentEnv: map[string]string{
		"CLAUDE_CODE_NO_FLICKER":               "0",
		"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "1",
	}}
	for _, tc := range []struct {
		why  string
		set  map[string]string
		want []string
	}{
		{"operator silent", nil,
			[]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1", "CLAUDE_CODE_NO_FLICKER=0"}},
		{"operator overrides one", map[string]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "0"},
			[]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=0", "CLAUDE_CODE_NO_FLICKER=0"}},
	} {
		in := sandbox.Input{
			Target: "claudecode", Image: "proveo/claudecode:local", Man: man, Sid: "proveo-1-2",
			Lookup: func(k string) string { return tc.set[k] },
		}
		cfg, kit, _ := sandbox.Spec(in)
		for _, w := range tc.want {
			if !slices.Contains(cfg.Env, w) {
				t.Errorf("%s: -e argv lacks %s: %v", tc.why, w, cfg.Env)
			}
			k, v, _ := strings.Cut(w, "=")
			if kit.Environment == nil || kit.Environment.Variables[k] != v {
				t.Errorf("%s: Kit environment lacks %s", tc.why, w)
			}
		}
	}
}
