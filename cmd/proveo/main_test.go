package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
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

func TestBrokerProvider(t *testing.T) {
	t.Parallel()
	cursorMan := manifest.Manifest{Provider: "cursor"}
	tests := []struct {
		name     string
		forwards bool
		man      manifest.Manifest
		detected []string
		lookup   func(string) string
		on       bool
		want     string
	}{
		{"brokered + 1 provider + on", false, manifest.Manifest{}, []string{"anthropic"}, nil, true, "anthropic"},
		{"forwarded credentials never broker", true, manifest.Manifest{}, []string{"anthropic"}, nil, true, ""},
		{"two providers → ambiguous, skip", false, manifest.Manifest{}, []string{"anthropic", "openai"}, nil, true, ""},
		{"zero providers", false, manifest.Manifest{}, nil, nil, true, ""},
		{"broker disabled", false, manifest.Manifest{}, []string{"anthropic"}, nil, false, ""},
		{"cursor pin + multi-detect + host key", false, cursorMan, []string{"anthropic", "openai", "cursor"}, func(k string) string {
			if k == "CURSOR_API_KEY" {
				return "sk-cursor"
			}
			return ""
		}, true, "cursor"},
		{"cursor pin without key", false, cursorMan, []string{"anthropic", "openai"}, func(string) string { return "" }, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := tc.lookup
			if lookup == nil {
				lookup = func(string) string { return "" }
			}
			if got := brokerProvider(tc.forwards, tc.man, tc.detected, lookup, tc.on); got != tc.want {
				t.Errorf("brokerProvider(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBrokerOffReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		forwards     bool
		providerName string
		detected     []string
		on           bool
		wantSubstr   string // "" = expect no warning at all
	}{
		{"two providers → explain", false, "", []string{"anthropic", "openai"}, true, "anthropic, openai"},
		{"broker disabled → explain", false, "", []string{"anthropic"}, false, "PROVEO_CREDENTIAL_BROKER"},
		{"broker armed → silent", false, "anthropic", []string{"anthropic"}, true, ""},
		{"forwarded credentials → silent", true, "", []string{"anthropic", "openai"}, true, ""},
		{"no keys at all → silent", false, "", nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := brokerOffReason(tc.forwards, tc.providerName, tc.detected, tc.on)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Errorf("brokerOffReason(...) = %q, want no warning", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("brokerOffReason(...) = %q, want it to mention %q", got, tc.wantSubstr)
			}
			if !strings.Contains(got, entrypoint.DefaultSentinel) {
				t.Errorf("warning must name the sentinel the agent will get; got %q", got)
			}
		})
	}
}

func TestAssembleAndDispatch(t *testing.T) {
	t.Parallel()

	t.Run("open+forward: no lifecycle, bare agent", func(t *testing.T) {
		t.Parallel()
		plan, agent, err := assemble(assembleInput{
			params: runParams{mode: "open", credentials: "forward", target: "opencode", image: "img"},
			sid:    "s", egDir: "/st", uid: "1000", gid: "1000",
			pidsLimit: 4096,
		})
		if err != nil {
			t.Fatal(err)
		}
		if needsLifecycle(plan) {
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
		plan, _, err := assemble(assembleInput{
			params: runParams{mode: "firewall", target: "claudecode", image: "img"},
			sid:    "s", egDir: "/st", uid: "1000", gid: "1000",
			provider: "anthropic", brokerFile: "/st/inject/broker.env",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !needsLifecycle(plan) {
			t.Error("firewall must go through the lifecycle")
		}
		if !plan.UsesSquid || plan.CAWaitPath == "" {
			t.Errorf("firewall plan should use squid + set CAWaitPath: %+v", plan)
		}
	})

	t.Run("firewall + local model: lifecycle via the ollama sidecar", func(t *testing.T) {
		t.Parallel()
		plan, _, err := assemble(assembleInput{
			params: runParams{mode: "broker", target: "opencode", image: "img", localModel: "gemma4"},
			sid:    "s", egDir: "/st", uid: "1000", gid: "1000",
			modelsDir: "/models",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !needsLifecycle(plan) {
			t.Error("firewall + --local-model must need the lifecycle (ollama sidecar)")
		}
		if plan.OllamaContainer == "" {
			t.Error("local-model plan must set OllamaContainer")
		}
	})

	t.Run("shell + data-dir affect the agent config", func(t *testing.T) {
		t.Parallel()
		_, agent, err := assemble(assembleInput{
			params: runParams{mode: "broker", target: "opencode", image: "img", shell: true, dataDir: "/data"},
			sid:    "s", egDir: "/st", uid: "1", gid: "1",
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
		_, agent, err := assemble(assembleInput{
			params: runParams{mode: "broker", target: "cursor", image: "img"},
			sid:    "s", egDir: "/st", uid: "1", gid: "1",
			env: []string{"CURSOR_API_KEY"},
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
		plan, agent, err := assemble(assembleInput{
			params: runParams{mode: "firewall", target: "cursor", image: "img"},
			sid:    "s", egDir: "/st", uid: "1", gid: "1",
			provider: "cursor", brokerFile: "/st/inject/broker.env",
			env: []string{
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
		if !strings.Contains(sidecar, "PROVEO_EGRESS_PROVIDER=cursor") {
			t.Errorf("proxy must pin cursor: %s", sidecar)
		}
		if !strings.Contains(sidecar, "/broker:ro") {
			t.Errorf("proxy must mount broker dir: %s", sidecar)
		}
	})

	t.Run("unknown mode errors", func(t *testing.T) {
		t.Parallel()
		if _, _, err := assemble(assembleInput{params: runParams{mode: "nope"}, sid: "s", egDir: "/st"}); err == nil {
			t.Error("assemble with an unknown mode must error")
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
	var ae agentExitError

	if !errors.As(error(agentExitError{code: 42}), &ae) || ae.code != 42 {
		t.Errorf("agentExitError must match itself and carry the code, got %+v", ae)
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

// T2: writeBrokerEnv writes the injected key to a 0600 file in a 0700 dir, and
// errors when no provider key is present (never writes an empty secret file).
func TestWriteBrokerEnv(t *testing.T) {
	// Isolate from the ambient environment: clear every provider key var.
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}

	emptyLookup := func(string) string { return "" }
	if _, err := writeBrokerEnv(filepath.Join(t.TempDir(), "inject"), emptyLookup); err == nil {
		t.Error("writeBrokerEnv with no provider key must error, not write an empty file")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-value")
	dir := filepath.Join(t.TempDir(), "inject")
	path, err := writeBrokerEnv(dir, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("broker.env perm = %o, want 600", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("inject dir perm = %o, want 700", got)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "ANTHROPIC_API_KEY=sk-ant-test-value") {
		t.Errorf("broker.env content = %q, want the key=value line", b)
	}
}

func TestWriteBrokerEnvFromHostFile(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := writeBrokerEnv(filepath.Join(t.TempDir(), "inject"), providerLookup(envPath))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "CURSOR_API_KEY=from-file") {
		t.Errorf("broker.env should include host-file key, got %q", b)
	}
}

func TestProviderDetectFromHostDotEnvOnly(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := providerLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "cursor" {
		t.Fatalf("Detect(lookup) = %v, want [cursor]", detected)
	}
	if got := brokerProvider(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); got != "cursor" {
		t.Fatalf("brokerProvider = %q, want cursor", got)
	}
}

func TestMoonshotDetectFromHostDotEnvOnly(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("MOONSHOT_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := providerLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "moonshot" {
		t.Fatalf("Detect(lookup) = %v, want [moonshot]", detected)
	}
	if got := brokerProvider(false, manifest.Manifest{}, detected, lookup, true); got != "moonshot" {
		t.Fatalf("brokerProvider = %q, want moonshot", got)
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
	lookup := providerLookup(hostEnv)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "cursor" {
		t.Fatalf("Detect(lookup from pwd .env) = %v, want [cursor]", detected)
	}
}

func TestCursorBrokerWithMultiProviderDotEnv(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	t.Setenv("CURSOR_API_KEY", "sk-cursor-host-only")
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("ANTHROPIC_API_KEY=sk-ant\nOPENAI_API_KEY=sk-oai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := providerLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) < 2 {
		t.Fatalf("Detect(lookup) = %v, want multiple providers", detected)
	}
	if got := brokerProvider(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); got != "cursor" {
		t.Fatalf("brokerProvider = %q, want cursor", got)
	}
	path, err := writeBrokerEnv(filepath.Join(t.TempDir(), "inject"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "CURSOR_API_KEY=sk-cursor-host-only") {
		t.Errorf("broker.env = %q, want host CURSOR_API_KEY", b)
	}
}

func TestHydrateProcessEnvFromLookup(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "")
	lookup := func(string) string { return "from-file" }
	hydrateProcessEnv("CURSOR_API_KEY", lookup)
	if got := os.Getenv("CURSOR_API_KEY"); got != "from-file" {
		t.Fatalf("CURSOR_API_KEY = %q, want from-file", got)
	}
}

func TestWorkspaceHeaderStatesFactsAndPredictsLSP(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "package.json", "nx.json", "main.go", "Dockerfile"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(filepath.Join(cfg, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"architect.md", "sre.md"} {
		if err := os.WriteFile(filepath.Join(cfg, "agents", a), []byte("#"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	man := manifest.Manifest{Workspace: manifest.Workspace{ConfigDir: ".opencode"}}
	got := strings.Join(workspaceHeader(man, dir, dir, t.TempDir()), "\n")

	for _, want := range []string{"tooling:", "go", "nx", "node", "docker", "subagents: 2 definition(s)", ".opencode/settings.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q\n--- got ---\n%s", want, got)
		}
	}
	if !strings.Contains(got, "lsp:      will start") {
		t.Errorf("LSP servers must be phrased as a prediction, got:\n%s", got)
	}
	if strings.Contains(got, "lsp:      detected") {
		t.Error("LSP presence depends on the image; the host must not claim detection")
	}
}

func TestWorkspaceHeaderIsEmptyWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	if got := workspaceHeader(manifest.Manifest{}, "", "", ""); got != nil {
		t.Errorf("no input dir must yield no header, got %v", got)
	}
}

func TestReadmePillsMatchToolingRegistry(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)

	pill := regexp.MustCompile(`!\[([a-z0-9.+-]+)\]\(https://img\.shields\.io/badge/`)
	inReadme := map[string]bool{}
	for _, m := range pill.FindAllStringSubmatch(readme, -1) {
		inReadme[m[1]] = true
	}
	if len(inReadme) == 0 {
		t.Fatal("no tooling pills found in README.md — the supported-tooling section is missing")
	}

	for _, label := range ToolingLabels() {
		if !inReadme[label] {
			t.Errorf("toolingMarkers has %q but README.md has no pill for it", label)
		}
		delete(inReadme, label)
	}
	for stale := range inReadme {
		t.Errorf("README.md has a pill for %q which is not in toolingMarkers", stale)
	}
}

// proveo --init advertises the keys it will copy into a new .env. Advertising a
// key with no registry entry is a lie: it is never detected, brokered or
// allowlisted, so the user sets it and the agent still gets nothing.
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

// The auth row exists only when the operator actually holds more than one
// credential for the provider this run will pin — otherwise there is no decision
// and the row would be inert.
func TestAvailableAuthVarsOnlyWhenThereIsAChoice(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}}}
	both := func(k string) string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"}[k]
	}
	if got := availableAuthVars(man, both); len(got) != 2 {
		t.Errorf("with both credentials = %v, want two options", got)
	}
	only := func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk"}[k] }
	if got := availableAuthVars(man, only); len(got) != 1 {
		t.Errorf("with one credential = %v, want one (no row is rendered for <2)", got)
	}
	none := func(string) string { return "" }
	if got := availableAuthVars(man, none); len(got) != 0 {
		t.Errorf("with no credential = %v, want none", got)
	}
}
