// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml, _spec/internal/sbx/virtiofs-cwd-invalidation.puml, _spec/internal/sbx/ide-attach.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
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
	hostEnv := workspace.ProjectEnvFile(root, scope, "")
	lookup := credentials.ProviderLookup(hostEnv)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "cursor" {
		t.Fatalf("Detect(lookup from pwd .env) = %v, want [cursor]", detected)
	}
}

func TestSandboxSpecAddsTheDataDirReadOnly(t *testing.T) {
	// A real directory: an sbx workspace must BE one, so the spec drops binds
	// that are not (the project .env arrives as a file bind and sbx refuses it).
	dataDir := t.TempDir()
	in := sandbox.Input{
		Target:   "claudecode",
		Image:    "proveo/claudecode:latest",
		Forwards: false,
		Man:      manifest.Manifest{Name: "claudecode"},
		Lookup:   func(string) string { return "" },
		DataDir:  dataDir,
	}
	cfg, _, secrets := sandbox.Spec(in)
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
	for _, want := range []string{"kept for diagnosis", "sbx exec -it " + name + " bash", "sbx rm --force " + name} {
		if !strings.Contains(got[0], want) {
			t.Errorf("first line dropped %q: %q", want, got[0])
		}
	}
	if !strings.Contains(got[1], log) {
		t.Errorf("the transcript path must be named verbatim, got %q", got[1])
	}

	attach := sandbox.IDEAttachLines(sandbox.Input{Clone: true, RepoRoot: "/host/repo"},
		sbx.RunConfig{Name: name, Mounts: []sbx.Mount{{Host: "/host/repo"}}}, false)
	joined := strings.Join(attach, "\n")
	for _, want := range []string{"sbx setup ssh", name + ".sbx", "DISPOSABLE CLONE", "refs/proveo/" + name} {
		if !strings.Contains(joined, want) {
			t.Errorf("kept-sandbox attach guidance dropped %q: %q", want, joined)
		}
	}

	// No transcript is a real state — runlog.Open failing must not print a line
	// pointing at the empty string, which reads as a path that got lost.
	if got := sandbox.KeptLines(name, "  "); len(got) != 1 {
		t.Errorf("a run with no transcript gets one line, got %q", got)
	}
}

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

	// No home, so no login can be found. Neither is stored: a plan token has no
	// store slot (R4), and an API key in sbx's anthropic entry would bill over the
	// subscription (R5).
	noHome := names(base)
	if len(noHome) != 0 {
		t.Errorf("without a host login want nothing stored, got %v", noHome)
	}

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
	if !strings.Contains(joined, "proveo_sync_tools save") {
		t.Errorf("teardown must also carry the toolchain tree out: %v", got)
	}
	// The harness's own configuration — wired MCP servers, LSP config, plugin
	// records, settings merges — reaches the operator only here.
	if !strings.Contains(joined, "proveo_sync_config save") {
		t.Errorf("teardown must also carry the harness configuration out: %v", got)
	}
	// Joined with `;`, not `&&`: a failed transcript copy must not take the
	// toolchains with it.
	if strings.Contains(joined, "proveo_sync_tools save && proveo_sync_state") {
		t.Errorf("the two syncs must not be chained on success: %v", got)
	}
	// `-w /` is not decoration: a virtiofs-invalidated workspace kills the exec at
	// chdir.
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

// The DEF picks the agent. A variant target is another image of the same def,
// so claudecode-browser is served by `claude` and not by an agent of its own.
func TestSandboxSpecUsesTheHarnessAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, def, target, want string
	}{
		{name: "claudecode", def: "claudecode", target: "claudecode", want: "claude"},
		{name: "claudecode variant", def: "claudecode", target: "claudecode-browser", want: "claude"},
		{name: "cursor", def: "cursor", target: "cursor", want: "cursor-agent"},
		{name: "cursor variant", def: "cursor", target: "cursor-browser", want: "cursor-agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := sandbox.Input{
				Man:    manifest.Manifest{Name: tc.def},
				Lookup: func(string) string { return "" },
				Target: tc.target, Image: "proveo/x:local",
			}
			cfg, w, _ := sandbox.Spec(in)
			if want := sbx.AgentName(tc.def); cfg.Agent != want {
				t.Errorf("Spec(def %q, target %q).Agent = %q, want the def's workload %q", tc.def, tc.target, cfg.Agent, want)
			}
			if len(w.Recipe.Entrypoint) == 0 || w.Recipe.Entrypoint[0] != tc.want {
				t.Errorf("Spec(def %q, target %q) launches %q, want the def's published %s", tc.def, tc.target, w.Recipe.Entrypoint, tc.want)
			}
		})
	}
}

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

func TestMCPGatewayIsDeclinedUnlessAllowed(t *testing.T) {
	t.Setenv("PROVEO_SBX_MCP", "on")
	got := sandbox.DeclineMCPGateway(false, []string{"HOME=/proveo-home"})
	if !slices.Contains(got, sandbox.MCPGatewayVar+"=") || !slices.Contains(got, "HOME=/proveo-home") {
		t.Errorf("declined: env = %v, want an explicit empty %s beside the rest — that is what makes sbx's `[ -n ... ] || exit 0` no-op; the env var must not decide", got, sandbox.MCPGatewayVar)
	}
	if got := sandbox.DeclineMCPGateway(true, []string{"HOME=/proveo-home"}); len(got) != 1 {
		t.Errorf("allowed: env = %v, want it untouched", got)
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
	stored := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "github", "anthropic", "cursor"}
	got := credentials.StoreHolds(man, stored)
	want := []string{"anthropic"}
	if !slices.Equal(got, want) {
		t.Errorf("credentials.StoreHolds = %v, want %v — the store is read by service id, never by env-var name (P0 M2)", got, want)
	}
	if len(credentials.StoreHolds(man, nil)) != 0 {
		t.Error("an unreadable store must hold nothing, not everything")
	}
	if got := credentials.StoreHolds(man, []string{"CURSOR_API_KEY", "cursor", "github"}); len(got) != 0 {
		t.Errorf("another harness's credentials read as this one's login: %v", got)
	}
	if got := credentials.StoreHolds(man, []string{"PROVEO_AGENT_EVIDENCE"}); len(got) != 0 {
		t.Errorf("a non-secret var counted as a credential: %v", got)
	}
}

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
		cfg, _, _ := sandbox.Spec(in)
		for _, w := range tc.want {
			if !slices.Contains(cfg.Env, w) {
				t.Errorf("%s: -e argv lacks %s: %v", tc.why, w, cfg.Env)
			}
		}
	}
}

func TestSessionAliveReadsThePidFromTheSessionID(t *testing.T) {
	if !sessionAlive(fmt.Sprintf("proveo-1-%d", os.Getpid())) {
		t.Error("this test's own pid must read as alive")
	}
	for _, sid := range []string{"proveo-1-0", "proveo-1-x", "other-1-1", "proveo"} {
		if sessionAlive(sid) {
			t.Errorf("%q must not read as alive", sid)
		}
	}
}

func TestExitCodeIsTheAgentsOrTheStoppingSignals(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		want   int
		wantOK bool
	}{
		{"the agent's own code", fmt.Errorf("run: %w", backend.ExitError{Code: 137}), 137, true},
		{"a SIGTERM stop", fmt.Errorf("run: %w", backend.Stopped{Signal: syscall.SIGTERM}), 143, true},
		{"a SIGHUP stop", backend.Stopped{Signal: syscall.SIGHUP}, 129, true},
		{"proveo's own failure", errors.New("no sbx"), 0, false},
	} {
		if got, ok := exitCode(c.err); got != c.want || ok != c.wantOK {
			t.Errorf("exitCode(%s) = %d, %v; want %d, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}
