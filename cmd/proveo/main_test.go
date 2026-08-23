package main

import (
	"bytes"
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

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/ui"
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

func TestBrokerProviders(t *testing.T) {
	t.Parallel()
	cursorMan := manifest.Manifest{Provider: "cursor"}
	tests := []struct {
		name     string
		forwards bool
		man      manifest.Manifest
		detected []string
		lookup   func(string) string
		on       bool
		want     []string
	}{
		{"brokered + 1 provider + on", false, manifest.Manifest{}, []string{"anthropic"}, nil, true, []string{"anthropic"}},
		{"forwarded credentials never broker", true, manifest.Manifest{}, []string{"anthropic"}, nil, true, nil},
		// The row this feature exists for: several keys used to mean "ambiguous,
		// broker nothing", which handed every provider the sentinel. All of them
		// are now routed.
		{"two providers → both routed", false, manifest.Manifest{}, []string{"anthropic", "openai"}, nil, true, []string{"anthropic", "openai"}},
		{"roles spanning vendors → both routed", false, manifest.Manifest{}, []string{"moonshot", "xai"}, nil, true, []string{"moonshot", "xai"}},
		{"zero providers", false, manifest.Manifest{}, nil, nil, true, nil},
		{"broker disabled", false, manifest.Manifest{}, []string{"anthropic"}, nil, false, nil},
		// A vendor-locked harness stays narrow: the other keys are not inference
		// providers for it.
		{"cursor pin + multi-detect + host key", false, cursorMan, []string{"anthropic", "openai", "cursor"}, func(k string) string {
			if k == "CURSOR_API_KEY" {
				return "sk-cursor"
			}
			return ""
		}, true, []string{"cursor"}},
		{"cursor pin without key", false, cursorMan, []string{"anthropic", "openai"}, func(string) string { return "" }, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := tc.lookup
			if lookup == nil {
				lookup = func(string) string { return "" }
			}
			got := brokerProviders(tc.forwards, tc.man, tc.detected, lookup, tc.on)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("brokerProviders(...) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBrokerOffReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		forwards   bool
		routed     []string
		detected   []string
		on         bool
		wantSubstr string // "" = expect no warning at all
	}{
		// Several providers is the supported shape now, so it must be SILENT — the
		// old "broker pins exactly one" warning was the symptom, not the diagnosis.
		{"two providers routed → silent", false, []string{"anthropic", "openai"}, []string{"anthropic", "openai"}, true, ""},
		{"keys present but none routable → explain", false, nil, []string{"anthropic", "openai"}, true, "anthropic, openai"},
		{"broker disabled → explain", false, nil, []string{"anthropic"}, false, "PROVEO_CREDENTIAL_BROKER"},
		{"broker armed → silent", false, []string{"anthropic"}, []string{"anthropic"}, true, ""},
		{"forwarded credentials → silent", true, nil, []string{"anthropic", "openai"}, true, ""},
		{"no keys at all → silent", false, nil, nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := brokerOffReason(tc.forwards, tc.routed, tc.detected, tc.on)
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
			providers: []string{"anthropic"}, brokerFile: "/st/inject/broker.env",
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
			providers: []string{"cursor"}, brokerFile: "/st/inject/broker.env",
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
		if !strings.Contains(sidecar, "PROVEO_EGRESS_PROVIDERS=cursor") {
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
	if got := brokerProviders(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("brokerProviders = %v, want [cursor]", got)
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
	if got := brokerProviders(false, manifest.Manifest{}, detected, lookup, true); len(got) != 1 || got[0] != "moonshot" {
		t.Fatalf("brokerProviders = %v, want [moonshot]", got)
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
	if got := brokerProviders(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("brokerProviders = %v, want [cursor]", got)
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

func TestLSPMarkerLabelsAreRealServerBinaries(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "..", "..", "packages", "lib", "entrypoint-lib.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	arm := regexp.MustCompile(`(?m)^\s*[a-z]+\)\s+echo\s+"([^"\s]+)`)
	binaries := map[string]bool{}
	for _, m := range arm.FindAllStringSubmatch(string(data), -1) {
		binaries[m[1]] = true
	}
	if len(binaries) == 0 {
		t.Fatalf("no _lsp_server arms parsed from %s", path)
	}

	for _, m := range lspMarkers {
		if !binaries[m.Label] {
			t.Errorf("lspMarkers predicts %q, which is not a command in _lsp_server(); "+
				"the host would promise a binary the image never installs", m.Label)
		}
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

// The review tier's transport is a bind-mounted unix socket, which only works on a
// Linux host talking to a local daemon. Anywhere else the gate is unreachable and
// every connection is denied without a prompt, so the option must say which.
func TestReviewSupportedRequiresLinuxAndALocalDaemon(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }
	ok, why := reviewSupported(none)
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
	if ok, why := reviewSupported(remote); ok {
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
	if ok, _ := reviewSupported(local); ok != (runtime.GOOS == "linux") {
		t.Errorf("a local unix daemon should track GOOS, got %v on %s", ok, runtime.GOOS)
	}
}

// TestAddonOptionsNeverOffersBothDaemons is the overlap this enum retired: a
// harness declares ONE docker mode, so the picker can never show both
// "docker (sandbox)" and "docker (dind)" — there is no locked-but-visible state
// left to explain, because the second option does not exist.
func TestAddonOptionsNeverOffersBothDaemons(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.DockerMode{manifest.DockerNone, manifest.DockerSbx, manifest.DockerDind} {
		man := manifest.Manifest{Name: "h", Docker: mode, Images: map[string]string{"h": "proveo/h:latest"}}
		opts := addonOptions(man)
		if slices.Contains(opts, addonSandbox) && slices.Contains(opts, addonDind) {
			t.Errorf("docker %q offered both daemons: %v", mode, opts)
		}
		switch mode {
		case manifest.DockerSbx:
			if !slices.Contains(opts, addonSandbox) {
				t.Errorf("docker: sbx must offer %q, got %v", addonSandbox, opts)
			}
		case manifest.DockerDind:
			if !slices.Contains(opts, addonDind) {
				t.Errorf("docker: dind must offer %q, got %v", addonDind, opts)
			}
		default:
			if len(opts) != 0 {
				t.Errorf("a harness with no docker mode must be offered none, got %v", opts)
			}
		}
	}
}

func TestGateAddonsEgressStillGatesWithoutSandbox(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Options: []string{addonDind}, Multi: true, On: []bool{false},
	}}}
	gateAddons(f, "firewall", "inject", "")
	if !f.Rows[0].Off[0] {
		t.Fatal("firewall+inject must still disable dind")
	}
	if f.Rows[0].Reason != addonDind+" needs egress open + credentials forward" {
		t.Errorf("reason = %q", f.Rows[0].Reason)
	}
	gateAddons(f, "open", "forward", "")
	if f.Rows[0].Off[0] {
		t.Error("open+forward on a docker: dind harness must leave the add-on enabled")
	}
}

func TestEvidenceRowDefaultsToVerbose(t *testing.T) {
	t.Parallel()
	r := evidenceRow((runParams{}).evidenceOrDefault())
	if r.Label != evidenceLabel || !r.Multi {
		t.Fatalf("row = %+v, want a checkbox row labelled %q", r, evidenceLabel)
	}
	if len(r.Options) != 2 || r.Options[0] != evidenceDefault || r.Options[1] != evidenceVerbose {
		t.Fatalf("options = %v, want [%s %s]", r.Options, evidenceDefault, evidenceVerbose)
	}
	if r.On[0] || !r.On[1] {
		t.Errorf("On = %v, want verbose ticked and default clear", r.On)
	}
	if got := evidenceRow(evidenceDefault); !got.On[0] || got.On[1] {
		t.Errorf("a remembered 'default' must tick default only, got %v", got.On)
	}
}

// The two boxes are one answer wearing checkbox glyphs: ticking one clears the
// other, and clearing both reads as default rather than as a third state.
func TestGateEvidenceKeepsTheLevelsExclusive(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{
		{Label: "add-ons", Options: []string{"browser"}, Multi: true, On: []bool{true}},
		evidenceRow(evidenceVerbose),
	}}
	// Ticking "default" (index 0) must clear the verbose box.
	f.Rows[1].Selected, f.Rows[1].On[0] = 0, true
	gateEvidence(f)
	if f.Rows[1].On[1] {
		t.Errorf("verbose survived a tick on default: %v", f.Rows[1].On)
	}
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != evidenceDefault {
		t.Errorf("evidence = %q, want %q", got, evidenceDefault)
	}
	// Un-ticking the only box leaves nothing selected, which is still default.
	f.Rows[1].On[0] = false
	gateEvidence(f)
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != evidenceDefault {
		t.Errorf("empty row = %q, want %q", got, evidenceDefault)
	}
	// Back to verbose, and the other row must be untouched throughout.
	f.Rows[1].Selected, f.Rows[1].On[1] = 1, true
	gateEvidence(f)
	if got := evidenceFrom(f.Selections(evidenceLabel)); got != evidenceVerbose {
		t.Errorf("evidence = %q, want %q", got, evidenceVerbose)
	}
	if !f.Rows[0].On[0] {
		t.Error("gateEvidence must not reach into the add-ons row")
	}
}

// Anything that is not an explicit opt-out resolves to verbose: a typo in
// PROVEO_AGENT_EVIDENCE must not quietly buy a black-box run.
func TestEvidenceOrDefaultOnlyOptsOutOnDefault(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":              evidenceVerbose,
		evidenceVerbose: evidenceVerbose,
		"bogus":         evidenceVerbose,
		evidenceDefault: evidenceDefault,
	} {
		if got := (runParams{evidence: in}).evidenceOrDefault(); got != want {
			t.Errorf("evidence %q resolved to %q, want %q", in, got, want)
		}
	}
}

// The mounted-.env warning was dead for a release cycle: the guard returned on
// all three canonical tiers after broker/firewall/proxy were renamed. Nothing
// asserted it, which is why the rename went unnoticed.
func TestWarnMountedSecretsFiresOnlyOnTheOpenTier(t *testing.T) {
	dirWithEnv := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirWithEnv, ".env"), []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withKey := func(name string) string {
		if name == "ANTHROPIC_API_KEY" {
			return "sk-test"
		}
		return ""
	}
	noKey := func(string) string { return "" }

	cases := []struct {
		name, dir, mode string
		lookup          func(string) string
		wantWarning     bool
	}{
		{"open tier warns — the plain bridge has no DLP", dirWithEnv, "open", withKey, true},
		{"allowlist stays silent — DLP blocks the exfil", dirWithEnv, "allowlist", withKey, false},
		{"review stays silent — same topology as allowlist", dirWithEnv, "review", withKey, false},
		{"mode is matched case-insensitively", dirWithEnv, "OPEN", withKey, true},
		{"no .env in the mounted tree", t.TempDir(), "open", withKey, false},
		{"no provider key on the host", dirWithEnv, "open", noKey, false},
		{"no mounted dir at all", "", "open", withKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := ui.Default
			ui.Default = ui.New(&buf)
			t.Cleanup(func() { ui.Default = restore })

			warnMountedSecrets(tc.dir, tc.mode, tc.lookup)

			got := strings.Contains(buf.String(), ".env is mounted")
			if got != tc.wantWarning {
				t.Errorf("warnMountedSecrets(%q, %q, lookup) warned = %v, want %v (output %q)",
					tc.dir, tc.mode, got, tc.wantWarning, buf.String())
			}
		})
	}
}

func TestSandboxSpecSeparatesSecretsFromEnv(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
			"ANTHROPIC_API_KEY":       "sk-value",
			"ANTHROPIC_BASE_URL":      "https://api.anthropic.com",
		}[k]
	}
	in := runSandboxInput{
		params: runParams{
			target: "claudecode", image: "proveo/claudecode:latest",
			mode: "broker", credentials: "",
			evidence: evidenceDefault,
			extra:    []string{"--verbose"},
		},
		man: manifest.Manifest{
			Name: "claudecode",
			Capabilities: manifest.Capabilities{
				Hosts: []string{"api.anthropic.com", "statsig.anthropic.com"},
			},
			Env: []manifest.EnvVar{
				{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
				{Name: "ANTHROPIC_BASE_URL"},
			},
		},
		sid:    "proveo-1-2",
		lookup: lookup,
		detected: func() []string {
			if _, ok := provider.Lookup("anthropic"); ok {
				return []string{"anthropic"}
			}
			return nil
		}(),
		gitEnv:  []string{"GIT_AUTHOR_NAME=Executor"},
		homeEnv: []string{"PROVEO_HOME=/proveo-home"},
	}

	cfg, kit, secrets := sandboxSpec(in)

	wantSecrets := map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": false, "ANTHROPIC_API_KEY": false}
	if len(secrets) != len(wantSecrets) {
		t.Fatalf("secrets = %v, want exactly the declared+provider keys %v", secrets, wantSecrets)
	}
	for _, kv := range secrets {
		if _, tracked := wantSecrets[kv[0]]; !tracked {
			t.Errorf("unexpected secret %q", kv[0])
		}
		if kv[1] == "" {
			t.Errorf("secret %q lost its value", kv[0])
		}
	}
	for _, e := range cfg.Env {
		name := strings.SplitN(e, "=", 2)[0]
		if name == "CLAUDE_CODE_OAUTH_TOKEN" || name == "ANTHROPIC_API_KEY" {
			t.Errorf("secret %q must travel via sbx secret, not env (%q)", name, e)
		}
	}

	var sawBaseURL bool
	for _, e := range cfg.Env {
		if e == "ANTHROPIC_BASE_URL=https://api.anthropic.com" {
			sawBaseURL = true
		}
	}
	if !sawBaseURL {
		t.Errorf("non-secret env missing resolved ANTHROPIC_BASE_URL in %v", cfg.Env)
	}
	for _, want := range []string{"PROVEO_AGENT_EVIDENCE=default", "GIT_AUTHOR_NAME=Executor", "PROVEO_HOME=/proveo-home"} {
		found := false
		for _, e := range cfg.Env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("env missing %q in %v", want, cfg.Env)
		}
	}

	if len(kit.Network.AllowedDomains) == 0 {
		t.Error("allowlist must include at least the manifest hosts")
	}
	sawManifestHost := false
	for _, d := range kit.Network.AllowedDomains {
		if d == "api.anthropic.com" || d == "statsig.anthropic.com" {
			sawManifestHost = true
		}
	}
	if !sawManifestHost {
		t.Errorf("allowlist missing manifest hosts: %v", kit.Network.AllowedDomains)
	}
	if got := kit.CredentialsEnv; len(got) != len(wantSecrets) {
		t.Errorf("kit credentialsEnv = %v, want %d names", got, len(wantSecrets))
	}

	if cfg.Name != "proveo-1-2" || cfg.Image != "proveo/claudecode:latest" {
		t.Errorf("run config name/image = %q/%q", cfg.Name, cfg.Image)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "--verbose" {
		t.Errorf("command = %v, want agent args passed through", cfg.Command)
	}
}

func TestSandboxSpecShellOverridesCommandAndAddsDataDir(t *testing.T) {
	in := runSandboxInput{
		params:  runParams{target: "claudecode", image: "proveo/claudecode:latest", shell: true},
		man:     manifest.Manifest{Name: "claudecode"},
		lookup:  func(string) string { return "" },
		workdir: "/workspace/input",
		dataDir: "/tmp/data",
	}
	cfg, _, secrets := sandboxSpec(in)
	if len(cfg.Command) != 1 || cfg.Command[0] != "bash" {
		t.Errorf("shell mode command = %v, want [bash]", cfg.Command)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none without credentials", secrets)
	}
	found := false
	for _, m := range cfg.Mounts {
		if m.Host == "/tmp/data" && m.Container == "/workspace/data" && !m.ReadOnly {
			t.Errorf("data dir mount must be read-only: %+v", m)
		}
		if m.Host == "/tmp/data" && m.Container == "/workspace/data" && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Errorf("data dir mount missing from %+v", cfg.Mounts)
	}
	if cfg.Workdir != "/workspace/input" {
		t.Errorf("workdir = %q", cfg.Workdir)
	}
}

func TestReviewAvailabilityGreysReviewOnSandboxBackend(t *testing.T) {
	row := choiceui.Row{Label: "egress", Options: []string{"open", "review"}, Selected: 1}
	greyed := reviewAvailability(row, true)
	if !greyed.Off[1] {
		t.Error("sbx backend must grey out review")
	}
	if greyed.Selected == 1 {
		t.Errorf("selection must move off review, got %d", greyed.Selected)
	}
	if !strings.Contains(greyed.Reason, "sandbox") {
		t.Errorf("reason = %q, want it to name the sandbox backend", greyed.Reason)
	}
	keep := reviewAvailability(choiceui.Row{Label: "egress", Options: []string{"open", "review"}}, false)
	if runtime.GOOS == "linux" && len(keep.Off) != 0 {
		t.Errorf("linux host without sbx must leave review enabled, got Off=%v", keep.Off)
	}
}

func TestSandboxSpecForwardsCredentialsWhenTheHarnessRequiresIt(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{"CURSOR_API_KEY": "key-value"}[k]
	}
	in := runSandboxInput{
		params: runParams{
			target: "cursor", image: "proveo/cursor:latest",
			mode: "open", credentials: "forward", evidence: evidenceDefault,
		},
		man: manifest.Manifest{
			Name: "cursor",
			Env:  []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
			Capabilities: manifest.Capabilities{
				Hosts:       []string{"api2.cursor.sh"},
				Egress:      []string{"open"},
				Credentials: []string{"forward"},
			},
		},
		sid:    "proveo-cursor-1",
		lookup: lookup,
	}

	cfg, kit, secrets := sandboxSpec(in)

	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none: forward mode must not route through sbx secret set", secrets)
	}
	if len(kit.CredentialsEnv) != 0 {
		t.Errorf("kit.CredentialsEnv = %v, want empty in forward mode", kit.CredentialsEnv)
	}
	var bare bool
	for _, e := range cfg.Env {
		if e == "CURSOR_API_KEY" {
			bare = true
		}
		if strings.HasPrefix(e, "CURSOR_API_KEY=") {
			t.Errorf("forwarded key must stay a bare -e name, got %q (value would ride argv)", e)
		}
	}
	if !bare {
		t.Errorf("cfg.Env = %v, want a bare CURSOR_API_KEY forwarded from the host", cfg.Env)
	}
}

func TestSandboxSpecBrokeredCredentialsStayHostSide(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
	lookup := func(k string) string {
		return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value"}[k]
	}
	in := runSandboxInput{
		params: runParams{
			target: "claudecode", image: "proveo/claudecode:latest",
			mode: "broker", credentials: "", evidence: evidenceDefault,
		},
		man: manifest.Manifest{
			Name: "claudecode",
			Env:  []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		},
		sid:    "proveo-cc-1",
		lookup: lookup,
	}

	_, kit, secrets := sandboxSpec(in)

	if len(secrets) == 0 {
		t.Fatal("secrets = none, want the declared secret injected host-side outside forward mode")
	}
	found := false
	for _, n := range kit.CredentialsEnv {
		if n == "CLAUDE_CODE_OAUTH_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Errorf("kit.CredentialsEnv = %v, want CLAUDE_CODE_OAUTH_TOKEN", kit.CredentialsEnv)
	}
}

func TestAddonOptionsOffersTheDockerSandbox(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:   "claudecode",
		Docker: manifest.DockerSbx,
		Images: map[string]string{"claudecode": "proveo/claudecode:latest", "claudecode-browser": "proveo/claudecode-browser:latest"},
	}
	got := addonOptions(man)
	want := []string{"browser", addonSandbox}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("addonOptions() mismatch (-want +got):\n%s", diff)
	}
	if opts := addonOptions(manifest.Manifest{Name: "opencode", Docker: manifest.DockerDind}); !slices.Contains(opts, addonDind) || slices.Contains(opts, addonSandbox) {
		t.Errorf("a docker: dind harness must not be offered the sandbox: %v", opts)
	}
}

func TestSandboxAddonIsOnUntilAnAnswerSaysOtherwise(t *testing.T) {
	t.Parallel()
	if !(&runParams{}).sandboxAddonOn() {
		t.Error("a first run must take the sandbox backend without being asked")
	}
	if !(&runParams{addons: []string{addonSandbox}, addonsAnswered: true}).sandboxAddonOn() {
		t.Error("a remembered yes must keep the sandbox on")
	}
	if (&runParams{addons: []string{"browser"}, addonsAnswered: true}).sandboxAddonOn() {
		t.Error("a remembered answer WITHOUT the add-on means the operator turned it off")
	}
	if (&runParams{addonsAnswered: true}).sandboxAddonOn() {
		t.Error("an empty remembered answer is still an answer — the sandbox stays off")
	}
}

func TestGateAddonsGreysTheSandboxWhenTheHostCannotRunIt(t *testing.T) {
	t.Parallel()
	f := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Options: []string{"browser", addonSandbox}, Multi: true, On: []bool{false, true},
	}}}
	gateAddons(f, "open", "forward", "sbx CLI not found on PATH")
	r := f.Rows[0]
	if !r.Off[1] {
		t.Fatal("the sandbox add-on must be greyed out when sbx is unavailable")
	}
	if !strings.Contains(r.Reason, "sbx CLI not found on PATH") {
		t.Errorf("reason = %q, want the availability reason", r.Reason)
	}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Errorf("a greyed add-on must not count as selected, got %v", got)
	}
	f.Rows[0].Off, f.Rows[0].Reason = nil, ""
	gateAddons(f, "open", "forward", "")
	if f.Rows[0].Off[1] {
		t.Error("an available sbx must leave the add-on checkable")
	}
}

func TestGateReviewFollowsTheSandboxAddon(t *testing.T) {
	t.Parallel()
	row := choiceui.Row{Label: "egress", Options: []string{"open", "review"}, Selected: 1}
	f := &choiceui.Form{Rows: []choiceui.Row{row}}
	gateReview(f, true)
	if !f.Rows[0].Off[1] {
		t.Fatal("review must be greyed out while the sandbox add-on is on")
	}
	if f.Rows[0].Selected == 1 {
		t.Error("selection must move off a greyed option")
	}
	if !strings.Contains(f.Rows[0].Reason, "docker sandbox backend") {
		t.Errorf("reason = %q", f.Rows[0].Reason)
	}
	gateReview(f, false)
	if ok, _ := reviewSupported(func(string) string { return "" }); ok && f.Rows[0].Off[1] {
		t.Error("turning the add-on off must hand the review tier back")
	}
}

func TestBothDockerAddonsStartChecked(t *testing.T) {
	t.Parallel()
	opts := []string{"browser", addonSandbox, addonDind}
	got := (&runParams{}).addonDefaults(opts)
	if diff := cmp.Diff([]bool{false, true, true}, got); diff != "" {
		t.Errorf("first-run defaults mismatch (-want +got):\n%s", diff)
	}
	// A remembered answer is authoritative in both directions.
	remembered := &runParams{addons: []string{"browser"}, addonsAnswered: true}
	if diff := cmp.Diff([]bool{true, false, false}, remembered.addonDefaults(opts)); diff != "" {
		t.Errorf("remembered choice mismatch (-want +got):\n%s", diff)
	}
}

func TestNormalizeAddonsUpgradesTheRememberedDindName(t *testing.T) {
	t.Parallel()
	got := normalizeAddons([]string{"browser", "dind"})
	if diff := cmp.Diff([]string{"browser", addonDind}, got); diff != "" {
		t.Errorf("normalizeAddons() mismatch (-want +got):\n%s", diff)
	}
}
