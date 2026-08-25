package main

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
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

func TestWorkspaceHeaderStatesFactsAndListsLSP(t *testing.T) {
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
	got := strings.Join(workspaceHeader(man, dir, dir, t.TempDir(), glyphsOff), "\n")

	for _, want := range []string{"tooling:", "go", "nx", "node", "docker", "subagents: 2 definition(s)", ".opencode/settings.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q\n--- got ---\n%s", want, got)
		}
	}
	if !strings.Contains(got, "lsp:      gopls") {
		t.Errorf("LSP row must list the servers plainly, got:\n%s", got)
	}
	// The "will start" prefix was dropped deliberately (_spec/internal/choiceui/wireframe.puml).
	// The harder rule survives it: LSP presence depends on the image, so the host may
	// neither claim detection nor re-add a prediction phrase it cannot honour.
	for _, banned := range []string{"lsp:      detected", "will start"} {
		if strings.Contains(got, banned) {
			t.Errorf("LSP row must state servers plainly; found %q in:\n%s", banned, got)
		}
	}
}

func TestWorkspaceHeaderIsEmptyWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	if got := workspaceHeader(manifest.Manifest{}, "", "", "", glyphsOff); got != nil {
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

	if len(kit.Permissions.Network.Allow) == 0 {
		t.Error("allowlist must include at least the manifest hosts")
	}
	sawManifestHost := false
	for _, d := range kit.Permissions.Network.Allow {
		if d == "api.anthropic.com" || d == "statsig.anthropic.com" {
			sawManifestHost = true
		}
	}
	if !sawManifestHost {
		t.Errorf("allowlist missing manifest hosts: %v", kit.Permissions.Network.Allow)
	}
	// Credentials are NOT declared here any more. The built-in agent's own kit
	// declares service "anthropic", and a mixin repeating it is rejected outright
	// ("defined in both") — sbx's proxy does the injection either way.
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin: a sandbox kit declares an agent sbx will not register", kit.Kind)
	}
	if kit.Setup == nil || len(kit.Setup.Startup) == 0 {
		t.Error("the Kit must carry the seed step, or nothing composes subagents under sbx")
	}

	if cfg.Name != "proveo-1-2" || cfg.Image != "proveo/claudecode:latest" {
		t.Errorf("run config name/image = %q/%q", cfg.Name, cfg.Image)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "--verbose" {
		t.Errorf("command = %v, want agent args passed through", cfg.Command)
	}
}

func TestSandboxSpecShellOverridesCommandAndAddsDataDir(t *testing.T) {
	// A real directory: an sbx workspace must BE one, so the spec drops binds
	// that are not (the project .env arrives as a file bind and sbx refuses it).
	dataDir := t.TempDir()
	in := runSandboxInput{
		params:  runParams{target: "claudecode", image: "proveo/claudecode:latest", shell: true},
		man:     manifest.Manifest{Name: "claudecode"},
		lookup:  func(string) string { return "" },
		workdir: "/app",
		dataDir: dataDir,
	}
	cfg, _, secrets := sandboxSpec(in)
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
	// The Kit never declares credentials at all now — brokered or forwarded, the
	// built-in agent owns that service and a mixin repeating it is rejected.
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin", kit.Kind)
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
	// The secret still goes to sbx's store host-side, but the Kit no longer NAMES
	// it: the built-in agent declares service "anthropic" itself, and a mixin
	// repeating it is rejected ("defined in both").
	var named bool
	for _, kv := range secrets {
		if kv[0] == "CLAUDE_CODE_OAUTH_TOKEN" {
			named = true
		}
	}
	if !named {
		t.Errorf("secrets = %v, want the declared credential injected host-side", secrets)
	}
	if kit.Kind != "mixin" {
		t.Errorf("kit.Kind = %q, want mixin", kit.Kind)
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

// The DLP's on-provider exemption cannot be derived from detected keys alone. A
// subscription harness authenticates INSIDE the sandbox, so nothing is
// detectable host-side, yet the token it mints there still has to reach the
// vendor — the manifest's declared providers are the only statement of where
// that is. Deriving the set from detection alone made the exemption empty for
// exactly the harness that needs it most.
func TestPolicyProviderHostsCoversDeclaredAndDetected(t *testing.T) {
	t.Parallel()
	subscription := manifest.Capabilities{Providers: []string{"anthropic"}}

	if got := policyProviderHosts(nil, subscription); len(got) == 0 {
		t.Error("a subscription harness with no host-side key got no provider hosts")
	}
	if got := policyProviderHosts([]string{"anthropic"}, manifest.Capabilities{}); len(got) == 0 {
		t.Error("a detected provider with no declared capability got no provider hosts")
	}
	// Declared and detected overlap on the common path; the union must not
	// double-list a host (the policy would still match, but the plan reads twice).
	got := policyProviderHosts([]string{"anthropic"}, subscription)
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Errorf("duplicate host %q in %v", h, got)
		}
		seen[h] = true
	}
	if len(got) == 0 {
		t.Fatal("union of declared and detected must not be empty")
	}
}

// The cache seeds a prompt and is never an authority of its own, so a run with
// no prompt to seed takes the manifest default
// (_spec/internal/agentsettings/choice-cache.puml). Applying it headlessly let
// the last interactive session decide a later run's security posture: an e2e run
// that asked for the default `--credentials broker` silently got `forward`, and
// with it a `browser` image variant it never selected.
func TestCacheOnlyAppliesWhereThereIsAPromptToSeed(t *testing.T) {
	for _, tc := range []struct {
		name           string
		printOnly, tty bool
		wizard         string
		want           bool
	}{
		{name: "interactive", tty: true, want: true},
		{name: "no tty", tty: false, want: false},
		{name: "dry run", printOnly: true, tty: true, want: false},
		{name: "wizard off", tty: true, wizard: "off", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROVEO_WIZARD", tc.wizard)
			if got := cacheApplies(tc.printOnly, tc.tty); got != tc.want {
				t.Errorf("cacheApplies(printOnly=%v, tty=%v) = %v, want %v",
					tc.printOnly, tc.tty, got, tc.want)
			}
		})
	}
}

// The seeding itself must not overwrite an axis the operator stated on the
// command line — the cache fills gaps, it does not out-rank a flag.
func TestSeedFromCacheYieldsToExplicitFlags(t *testing.T) {
	t.Parallel()
	cached := agentsettings.Choice{Egress: "open", Credentials: "forward", Addons: []string{"browser"}}
	lookup := func(string) string { return "" }

	stated := runParams{mode: "allowlist", credentials: "broker", modeSet: true, credsSet: true}
	stated.seedFromCache(cached, lookup, false)
	if stated.mode != "allowlist" || stated.credentials != "broker" {
		t.Errorf("explicit flags were overwritten: mode=%q credentials=%q", stated.mode, stated.credentials)
	}

	unstated := runParams{mode: "allowlist", credentials: "broker"}
	unstated.seedFromCache(cached, lookup, false)
	if unstated.mode != "open" || unstated.credentials != "forward" {
		t.Errorf("unstated axes were not seeded: mode=%q credentials=%q", unstated.mode, unstated.credentials)
	}
	if !hasAddon(unstated.addons, "browser") {
		t.Errorf("remembered add-ons were not seeded: %v", unstated.addons)
	}
}

// anthropic takes either an API key or a subscription token, and an operator may
// hold both. A service is one identity in a Kit, so declaring it twice would
// leave the effective credential up to map order inside sbx.
func TestKitCredentialsDeclareOneEntryPerService(t *testing.T) {
	t.Parallel()
	secrets := [][2]string{
		{"CLAUDE_CODE_OAUTH_TOKEN", "oauth"}, // manifest-declared: comes first
		{"ANTHROPIC_API_KEY", "sk-x"},        // same service, also present
		{"OPENAI_API_KEY", "sk-y"},
	}
	got := kitCredentials(secrets, false)

	byService := map[string]int{}
	for _, c := range got {
		byService[c.Service]++
	}
	for svc, n := range byService {
		if n != 1 {
			t.Errorf("service %q declared %d times, want exactly 1", svc, n)
		}
	}
	// First wins, so the manifest-declared credential is the one that stands.
	for _, c := range got {
		if c.Service == "anthropic" && c.APIKey.Name != "CLAUDE_CODE_OAUTH_TOKEN" {
			t.Errorf("anthropic resolved to %q, want the first-listed CLAUDE_CODE_OAUTH_TOKEN", c.APIKey.Name)
		}
	}
	if len(byService) != 2 {
		t.Errorf("services = %v, want anthropic and openai", byService)
	}
}

// Nerd is the default and ASCII is the fallback an operator selects when their font
// stops at the Powerline range. Off must leave the row byte-identical, and a server
// with no devicon must degrade to its category marker rather than to a ragged column.
func TestLSPGlyphModes(t *testing.T) {
	t.Parallel()
	labels := []string{"typescript-language-server", "bash-language-server", "gopls"}

	if got := withGlyphs(labels, glyphsOff); !reflect.DeepEqual(got, labels) {
		t.Errorf("glyphs off must not touch the labels, got %v", got)
	}

	for _, mode := range []glyphMode{glyphsNerd, glyphsASCII} {
		got := withGlyphs(labels, mode)
		for i, l := range labels {
			if !strings.HasSuffix(got[i], l) {
				t.Errorf("mode %d: %q must keep its server name, got %q", mode, l, got[i])
			}
			if got[i] == l {
				t.Errorf("mode %d: %q should have gained a glyph", mode, l)
			}
		}
	}

	// Nerd falls back to the ASCII category marker rather than leaving a hole.
	delete(lspNerd, "gopls")
	defer func() { lspNerd["gopls"] = "\ue627" }()
	if got := withGlyphs([]string{"gopls"}, glyphsNerd); got[0] != lspASCII["gopls"]+" gopls" {
		t.Errorf("a server with no devicon must fall back to ASCII, got %q", got[0])
	}

	// A server in neither table stays bare.
	if got := withGlyphs([]string{"unknown-langserver"}, glyphsNerd); got[0] != "unknown-langserver" {
		t.Errorf("unmapped server must stay bare, got %q", got[0])
	}
}

// Every language the scanner can detect needs a category marker, or enabling ASCII
// silently produces a column where some rows are indented and others are not.
func TestEveryLSPMarkerHasAnASCIIGlyph(t *testing.T) {
	t.Parallel()
	for _, m := range lspMarkers {
		if _, ok := lspASCII[m.Label]; !ok {
			t.Errorf("%s has no ASCII category marker", m.Label)
		}
	}
	// ASCII markers pad to two columns so names stay aligned across categories.
	for label, g := range lspASCII {
		if len([]rune(g)) != 2 {
			t.Errorf("%s marker %q is %d cols; must be 2", label, g, len([]rune(g)))
		}
	}
}

func TestGlyphModeFromLookup(t *testing.T) {
	t.Parallel()
	cases := map[string]glyphMode{
		"": glyphsNerd, "nerd": glyphsNerd, "1": glyphsNerd, "typo": glyphsNerd,
		"ascii": glyphsASCII, "ASCII": glyphsASCII,
		"off": glyphsOff, "0": glyphsOff, "false": glyphsOff, "none": glyphsOff,
	}
	for in, want := range cases {
		if got := glyphModeFrom(func(string) string { return in }); got != want {
			t.Errorf("PROVEO_GLYPHS=%q: got mode %d, want %d", in, got, want)
		}
	}
}

// Print mode now writes the Kit so the command it prints is runnable, which puts a
// file on disk that a dry run never used to create. That is only acceptable because
// the Kit declares credential NAMES and never values — this is the property the
// write depends on, so it is asserted rather than assumed.
func TestKitCredentialsNeverEmbedSecretValues(t *testing.T) {
	t.Parallel()
	secrets := [][2]string{
		{"CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-SUPERSECRET"},
		{"ANTHROPIC_API_KEY", "sk-ant-api-ALSOSECRET"},
	}
	creds := kitCredentials(secrets, false)
	if len(creds) == 0 {
		t.Fatal("brokered credentials must be declared")
	}
	blob, err := yaml.Marshal(creds)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(blob)
	// One entry per service, so two anthropic secrets collapse to one declaration:
	// assert a name is carried, not that every name is.
	var named bool
	for _, kv := range secrets {
		if strings.Contains(rendered, kv[0]) {
			named = true
		}
		if strings.Contains(rendered, kv[1]) {
			t.Errorf("Kit leaked the VALUE of %s; print mode writes this file to disk", kv[0])
		}
	}
	if !named {
		t.Errorf("no credential name reached the Kit:\n%s", rendered)
	}

	// --credentials forward declares nothing: the agent holds its own key and there
	// is no brokering to describe, so print mode writes an empty credentials block.
	if got := kitCredentials(secrets, true); got != nil {
		t.Errorf("forward mode must declare no credentials, got %v", got)
	}
}

// sandboxSpec never reads p.mode, so open and allowlist build an identical Kit. The
// row must say so rather than offering a risk axis on which nothing moves.
func TestSbxEgressRealityGreysOpen(t *testing.T) {
	t.Parallel()
	row := axisRow("egress", []string{"open", "allowlist", "review"}, nil, "open")

	if got := sbxEgressReality(row, false); got.Off != nil {
		t.Errorf("docker backend must leave every tier selectable, got Off=%v", got.Off)
	}

	got := sbxEgressReality(row, true)
	var greyed bool
	for i, o := range got.Options {
		if o == "open" && len(got.Off) > i && got.Off[i] {
			greyed = true
		}
		if o == "allowlist" && len(got.Off) > i && got.Off[i] {
			t.Error("allowlist is what sbx actually enforces; it must stay selectable")
		}
	}
	if !greyed {
		t.Errorf("open must be greyed on the sbx backend, got Off=%v", got.Off)
	}
	// Greyed, never hidden: removing it would misrepresent an unenforced tier as an
	// unavailable one.
	if len(got.Options) != len(row.Options) {
		t.Errorf("no tier may be removed, got %v", got.Options)
	}
}

func TestEnforcedByNamesTheBoundaryHolder(t *testing.T) {
	t.Parallel()
	if got := enforcedBy(true); !strings.Contains(got, "sbx") || !strings.Contains(got, "no Squid") {
		t.Errorf("sbx runs must name sbx and disclaim proveo's sidecars, got %q", got)
	}
	if got := enforcedBy(false); !strings.Contains(got, "squid") {
		t.Errorf("docker runs must name proveo's sidecars, got %q", got)
	}
}

// "agent exited with code 137" and "sandbox was stopped" are both sbx's auto-stop,
// arriving 30s after the agent exited; neither says why. The tail is what explains a
// REDIRECTED run — "Credit balance is too low", which is what it turned out to be.
//
// An interactive run takes no tail (see TestAgentStdioHandsTheTerminalOverUnwrapped:
// wrapping stdout costs the agent its tty) and is explained by the session transcript
// instead, which agentTranscript names on failure.
func TestTailWriterKeepsTheExplanation(t *testing.T) {
	t.Parallel()
	w := newTailWriter(3)
	// A TUI writes escapes, redraws the same status line, and may not end with \n.
	fmt.Fprint(w, "\x1b[2J\x1b[H🚀 Launching Claude Code…\n")
	fmt.Fprint(w, "\x1b[Kthinking\r\x1b[Kthinking\r")
	fmt.Fprint(w, "Ignoring 10 permissions.allow entries\n")
	fmt.Fprint(w, "\x1b[31mCredit balance is too low\x1b[0m")

	got := w.Lines()
	if len(got) != 3 {
		t.Fatalf("want the last 3 lines, got %d: %q", len(got), got)
	}
	if last := got[len(got)-1]; last != "Credit balance is too low" {
		t.Errorf("the unterminated final line must survive and be de-escaped, got %q", last)
	}
	for _, l := range got {
		if strings.Contains(l, "\x1b") {
			t.Errorf("escape sequences must be stripped, got %q", l)
		}
	}
	// A redrawn status line is one line, not many.
	var thinking int
	for _, l := range got {
		if l == "thinking" {
			thinking++
		}
	}
	if thinking > 1 {
		t.Errorf("consecutive duplicates must collapse, got %q", got)
	}
}

// MissingEnv reads env vars only, so a completed login sitting in the proveo home
// read as "no auth" — and the refusal built on it would have blocked working runs.
func TestHasPersistedLoginSeesTheCredentialFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if hasPersistedLogin("claudecode", home) {
		t.Error("an empty home has no login")
	}
	if hasPersistedLogin("claudecode", "") {
		t.Error("no home root means no login")
	}

	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(cred, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if hasPersistedLogin("claudecode", home) {
		t.Error("an empty credential file is not a login")
	}
	if err := os.WriteFile(cred, []byte(`{"x":{"accessToken":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasPersistedLogin("claudecode", home) {
		t.Error("a populated credential file is a login")
	}
	if hasPersistedLogin("opencode", home) {
		t.Error("a target with no known login file must not borrow another's")
	}
}

// os/exec copies stdout and stderr on separate goroutines and both are teed into one
// tailWriter, so Write is genuinely concurrent. Without a lock the two interleave on
// the shared buffer and reslicing panics with bounds out of range — which is exactly
// how this crashed a real run. Run under -race to catch the regression properly.
func TestTailWriterIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	w := newTailWriter(8)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				// Mixed shapes: with newlines, without, and with escapes — the
				// unterminated fragments are what made the indices disagree.
				fmt.Fprintf(w, "g%d line %d\n\x1b[Kpartial %d", g, i, i)
			}
		}(g)
	}
	wg.Wait()

	got := w.Lines()
	if len(got) == 0 || len(got) > 8 {
		t.Fatalf("want 1..8 retained lines, got %d", len(got))
	}
	for _, l := range got {
		if strings.Contains(l, "\x1b") {
			t.Errorf("escapes must be stripped even under concurrency, got %q", l)
		}
	}
}

// The picker and the backend selection must agree about PROVEO_SBX. They did not:
// sbx.Available() only reports whether the host CAN run sbx, so with the backend
// switched off the add-on stayed selectable and default-ticked while the run took
// docker — the prompt described a posture the run did not have.
func TestSandboxAddonIsGreyedAndUntickedWhenUnavailable(t *testing.T) {
	t.Parallel()
	row := choiceui.Row{
		Label: "add-ons", Multi: true,
		Options: []string{"browser", addonSandbox},
		On:      []bool{true, true}, // as addonDefaults leaves it: sandbox pre-ticked
	}
	f := &choiceui.Form{Rows: []choiceui.Row{row}}

	gateAddons(f, "open", "forward", "PROVEO_SBX is off")

	got := f.Rows[0]
	var i int
	for j, o := range got.Options {
		if o == addonSandbox {
			i = j
		}
	}
	if !got.Off[i] {
		t.Error("an unavailable sandbox add-on must be greyed")
	}
	if got.On[i] {
		t.Error("a greyed add-on must also be unticked: a ticked box reads as the run's posture")
	}
	if !strings.Contains(got.Reason, "PROVEO_SBX is off") {
		t.Errorf("the row must name why, got %q", got.Reason)
	}
	// An available backend leaves the operator's choice alone.
	f2 := &choiceui.Form{Rows: []choiceui.Row{{
		Label: "add-ons", Multi: true,
		Options: []string{addonSandbox}, On: []bool{true},
	}}}
	gateAddons(f2, "open", "forward", "")
	if f2.Rows[0].Off[0] || !f2.Rows[0].On[0] {
		t.Error("an available sandbox add-on must stay selectable and ticked")
	}
}

// Anthropic can authenticate two ways. Handing sbx both put an API key and a
// subscription token in the same store, its proxy injected the key, and a
// subscription run billed per token — the auth row the operator answered was
// overridden somewhere they could not see.
func TestOnlyTheChosenAuthVarIsStored(t *testing.T) {
	t.Parallel()
	const oauth, apikey = "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"

	if !losesToChosenAuth(apikey, oauth) {
		t.Error("the API key must lose when the operator chose the subscription token")
	}
	if !losesToChosenAuth(oauth, apikey) {
		t.Error("and the reverse: the token must lose when the operator chose the key")
	}
	if losesToChosenAuth(oauth, oauth) {
		t.Error("the chosen var must never be dropped")
	}
	// Only same-provider vars compete: an anthropic choice says nothing about
	// openai, and dropping an unrelated key removes reach the harness has.
	if losesToChosenAuth("OPENAI_API_KEY", oauth) {
		t.Error("a different provider's key must survive an anthropic choice")
	}
	// No choice made: change nothing.
	if losesToChosenAuth(apikey, "") {
		t.Error("without a chosen auth var nothing may be dropped")
	}
}

// An operator may log in on the HOST before launching; that credential reaches the
// container because HOME points at the mounted proveo home. When it is there it IS
// the answer, and proveo must not also hand sbx an API key whose proxy injection
// would override it — which is how a subscription run silently billed per token.
func TestHostLoginCountsAsTheChosenAuth(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Subscription: true, Env: []manifest.EnvVar{
		{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
	}}
	home := t.TempDir()

	// No explicit choice and no host login: nothing is implied, nothing is dropped.
	if got := effectiveAuthVar(man, "claudecode", "", home); got != "" {
		t.Errorf("without a login or a choice the auth var is unknown, got %q", got)
	}

	// A host login stands in for the answer the operator never had to give.
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := effectiveAuthVar(man, "claudecode", "", home); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("a persisted host login must select the harness credential, got %q", got)
	}
	if !losesToChosenAuth("ANTHROPIC_API_KEY", effectiveAuthVar(man, "claudecode", "", home)) {
		t.Error("with a host login present the competing API key must not be stored")
	}

	// An explicit answer always wins over the inferred one.
	if got := effectiveAuthVar(man, "claudecode", "ANTHROPIC_API_KEY", home); got != "ANTHROPIC_API_KEY" {
		t.Errorf("the operator's own choice must win, got %q", got)
	}
}

// sandboxSpec must stay pure: a host login decides which credential the run uses,
// but that fact arrives through the input, never by reaching for the real
// filesystem. It read proveohome.Root() briefly and every result then depended on
// whether the developer happened to be logged in.
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
	base := runSandboxInput{params: runParams{target: "claudecode"}, man: man, sid: "s", lookup: lookup}

	names := func(in runSandboxInput) map[string]bool {
		_, _, secrets := sandboxSpec(in)
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
	withHome.homeRoot = home
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

// A wrapped writer is not an *os.File, so os/exec substitutes a pipe and the agent
// loses its tty: no window size, and a TUI that draws one character per line. The
// terminal must be handed over by identity, which is what this pins down.
func TestAgentStdioHandsTheTerminalOverUnwrapped(t *testing.T) {
	out, errw := os.Stdout, os.Stderr
	gotOut, gotErr, tail := agentStdio(out, errw, true)
	if gotOut != io.Writer(out) || gotErr != io.Writer(errw) {
		t.Fatalf("interactive run wrapped the terminal: stdout=%T stderr=%T", gotOut, gotErr)
	}
	if tail != nil {
		t.Fatal("interactive run took a tail; that is what forces the pipe")
	}
	if lines := tail.Lines(); lines != nil {
		t.Fatalf("a nil tail must replay as no lines, got %v", lines)
	}
}

// Off a terminal the stream is already redirected, so the tail is free.
func TestAgentStdioTeesWhenStdoutIsRedirected(t *testing.T) {
	var out, errw bytes.Buffer
	gotOut, gotErr, tail := agentStdio(&out, &errw, false)
	if tail == nil {
		t.Fatal("a redirected run must keep the agent's last output")
	}
	fmt.Fprintln(gotOut, "credit balance is too low")
	fmt.Fprintln(gotErr, "agent exited with code 137")
	if !strings.Contains(out.String(), "credit balance") {
		t.Fatalf("the tee stopped reaching stdout: %q", out.String())
	}
	if got := tail.Lines(); len(got) != 2 || got[0] != "credit balance is too low" {
		t.Fatalf("tail did not retain both streams: %v", got)
	}
}

// A credential file under the mounted proveo home IS the login. Injecting the
// harness's own auth var alongside it does not add a second credential — it
// overrides the first, which is how a subscription run authenticated as the API.
func TestFileBackedLoginSuppressesEveryAuthVarForItsProvider(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	suppressed := authSuppressor(man, "claudecode", "", home)

	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppressed(k) {
			t.Errorf("%s injected over a mounted login; it would override the subscription", k)
		}
	}
	if suppressed("OPENAI_API_KEY") {
		t.Error("an anthropic login must say nothing about another provider's reach")
	}
}

// An answered auth row is the operator's decision and still wins.
func TestChosenAuthVarSurvivesAPersistedLogin(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	os.MkdirAll(filepath.Dir(cred), 0o755)
	os.WriteFile(cred, []byte(`{"oauth":"x"}`), 0o600)
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	suppressed := authSuppressor(man, "claudecode", "ANTHROPIC_API_KEY", home)

	if suppressed("ANTHROPIC_API_KEY") {
		t.Error("the operator's answer was dropped")
	}
	if !suppressed("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("the alternative to the answer must not be injected too")
	}
}

// With no login on disk nothing is suppressed: the env vars are the only auth.
func TestNoPersistedLoginInjectsTheManifestSecret(t *testing.T) {
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	if authSuppressor(man, "claudecode", "", t.TempDir())("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("dropped the only credential the run had")
	}
}

// sbx runs setup.startup under `user: "1000"`, which resets HOME from /etc/passwd.
// A seed reading $HOME therefore targets the image's home, not the run's — it
// composed subagents into /home/claude while the agent ran elsewhere. PROVEO_HOME
// is the name no launcher rewrites, so it must travel with every rewritten HOME.
func TestRewrittenHomeAlsoCarriesProveoHome(t *testing.T) {
	mounts := []sbx.Mount{{Host: "/Users/p/.proveo", Container: proveohome.ContainerHome}}
	got := sbxHome([]string{"HOME=/stale", "PROVEO_HOME=/stale", "KEEP=1"}, mounts)

	var home, seed int
	for _, e := range got {
		switch {
		case e == "HOME=/Users/p/.proveo":
			home++
		case e == "PROVEO_HOME=/Users/p/.proveo":
			seed++
		}
	}
	if home != 1 || seed != 1 {
		t.Fatalf("want exactly one rewritten HOME and PROVEO_HOME, got %v", got)
	}
	for _, e := range got {
		if e == "HOME=/stale" || e == "PROVEO_HOME=/stale" {
			t.Fatalf("a stale home survived the rewrite: %v", got)
		}
	}
}

// Interactive runs take no tail, so the transcript is the only record proveo can
// name. It must name THIS run's — an older one sends the reader to stale evidence.
func TestAgentTranscriptNamesOnlyThisRunsFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-w-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old.jsonl")
	fresh := filepath.Join(dir, "new.jsonl")
	for _, f := range []string{stale, fresh} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	old := started.Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	later := started.Add(time.Second)
	if err := os.Chtimes(fresh, later, later); err != nil {
		t.Fatal(err)
	}

	if got := agentTranscript("claudecode", home, started); got != fresh {
		t.Fatalf("want this run's transcript %q, got %q", fresh, got)
	}
	// Nothing written this run, and nothing to point at.
	if got := agentTranscript("claudecode", home, later.Add(time.Minute)); got != "" {
		t.Fatalf("named a transcript no run wrote: %q", got)
	}
	// A harness whose transcript location we have not established stays silent
	// rather than guessing a path that will read as "no evidence" forever.
	if got := agentTranscript("cursor", home, old); got != "" {
		t.Fatalf("guessed a location for an unmapped harness: %q", got)
	}
}

// Without --shell the harness's own sbx agent runs; the two must not be confused,
// because naming the wrong one is what skips the binding gate and drops the session.
func TestSandboxSpecUsesTheHarnessAgentUnlessShellIsAsked(t *testing.T) {
	t.Parallel()
	base := runSandboxInput{
		man:    manifest.Manifest{Name: "claudecode"},
		lookup: func(string) string { return "" },
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
		in.params = runParams{target: c.target, image: "proveo/x:local", shell: c.shell}
		if cfg, _, _ := sandboxSpec(in); cfg.Agent != c.want {
			t.Errorf("target %q shell=%v: agent = %q, want %q", c.target, c.shell, cfg.Agent, c.want)
		}
	}
}

// Omitting a suppressed credential is not enough on the sandbox backend: sbx's
// secret store is global and injects on its own authority, so an absent variable
// leaves whatever an earlier run stored sitting in front of the mounted login —
// which is how a subscription run kept authenticating as an API account. proveo
// states the decision instead, as the empty value `sbx run -e VAR=` accepts.
func TestSandboxSpecNeutralizesSuppressedCredentials(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	in := runSandboxInput{
		params: runParams{target: "claudecode", image: "proveo/claudecode:local"},
		man: manifest.Manifest{
			Name: "claudecode", Subscription: true,
			Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
			Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
		},
		lookup: func(k string) string {
			return map[string]string{
				"CLAUDE_CODE_OAUTH_TOKEN": "oauth-value",
				"ANTHROPIC_API_KEY":       "key-value",
			}[k]
		},
		homeRoot: home,
	}
	cfg, _, secrets := sandboxSpec(in)

	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !slices.Contains(cfg.Env, k+"=") {
			t.Errorf("%s must be stated empty so the global store cannot inject it; env = %v", k, cfg.Env)
		}
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, k+"=") && e != k+"=" {
				t.Errorf("%s carries a value over a mounted login: %q", k, e)
			}
		}
	}
	if len(secrets) != 0 {
		t.Errorf("nothing may be written to the store for a file-backed login, got %v", secrets)
	}
}

// The persisted login must be NAMEABLE in the auth row. Until it was, the row listed
// only environment variables, so a remembered answer naming one of them outranked a
// login the operator established later — proveo forwarded a token the API refused
// while a working subscription sat mounted and unread.
func TestAuthRowOffersThePersistedLoginFirst(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{
		Name: "claudecode", Provider: "anthropic",
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	lookup := func(k string) string {
		return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok", "ANTHROPIC_API_KEY": "key"}[k]
	}

	got := availableAuthVarsIn(man, lookup, "claudecode", home)
	if len(got) == 0 || got[0] != authVarLogin {
		t.Fatalf("the login must be offered first, got %v", got)
	}
	// Without one on disk the row is unchanged: nothing to name.
	if bare := availableAuthVarsIn(man, lookup, "claudecode", t.TempDir()); slices.Contains(bare, authVarLogin) {
		t.Errorf("offered a login that does not exist: %v", bare)
	}

	// Naming it suppresses that provider's variables, and only that provider's.
	suppressed := authSuppressor(man, "claudecode", authVarLogin, home)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppressed(k) {
			t.Errorf("%s injected over the login the operator named", k)
		}
	}
	if suppressed("OPENAI_API_KEY") {
		t.Error("an anthropic login must not remove reach to another provider")
	}
	// It is a sentinel, never an env var name.
	if v := effectiveAuthVar(man, "claudecode", authVarLogin, home); v == authVarLogin {
		t.Errorf("the login sentinel leaked into an env var name: %q", v)
	}
}
