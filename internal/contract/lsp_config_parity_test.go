// SPEC: _spec/packages/lib/language-server-provisioning.puml, _spec/internal/sbx/sbx-kit-contract.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// lspWiring is the table: every harness that has a config format for language
// servers, and the shared-lib function that writes it.
//
// The WIRE step is the last one in the provisioning chain (detect → rank →
// eligibility → recipe → install → wire) and the only one that knows a harness's
// config format. It is also the one that was easiest to leave in a def, because
// on the docker backend a def entrypoint runs and nothing looks wrong.
var lspWiring = []struct {
	target string
	fn     string
}{
	{"claudecode", "configure_claude_lsp"},
	{"opencode", "configure_opencode_lsp"},
	{"cursor", "configure_cursor_lsp"},
}

// On sbx the Kit's only startup command is `proveo-seed <target>` and the image
// ENTRYPOINT never runs (see sbx.SeedCommand / internal/backend/sandbox). So a
// wiring step reachable only from defs/<harness>/entrypoint.sh reaches the docker
// backend alone: opencode and cursor came up on the sandbox backend with every
// language server installed by proveo_provision_toolchain and none of them
// configured, which looks exactly like "no code intelligence in this harness".
func TestLspWiringIsReachableFromTheSeed(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)
	seed := seedBody(t, src)
	for _, w := range lspWiring {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(w.fn) + `\(\)\s*\{`).MatchString(src) {
			t.Errorf("%s is not defined in packages/lib/entrypoint-lib.sh — a wiring step "+
				"outside the shared lib cannot run on the sbx backend", w.fn)
			continue
		}
		if !strings.Contains(seed, w.fn) {
			t.Errorf("proveo_seed never calls %s, so %s gets language servers installed and "+
				"never configured on the sbx backend", w.fn, w.target)
		}
	}
}

// The same rule from the other side: a def entrypoint may not own a wiring step.
// Defining one there is how the drift happened the first time — it works on
// docker, so nothing fails until someone opens the harness under sbx.
func TestDefEntrypointsDoNotOwnLspWiring(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	eps, err := filepath.Glob(filepath.Join(root, "defs", "*", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	nested, err := filepath.Glob(filepath.Join(root, "defs", "*", "*", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	eps = append(eps, nested...)
	if len(eps) == 0 {
		t.Fatal("no def entrypoints found — the glob has drifted from the tree layout")
	}
	// A definition, not a mention: the comments that explain the move are fine.
	def := regexp.MustCompile(`(?m)^\s*(configure_\w*lsp\w*)\(\)\s*\{`)
	for _, ep := range eps {
		b, err := os.ReadFile(ep)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range def.FindAllStringSubmatch(string(b), -1) {
			rel, _ := filepath.Rel(root, ep)
			t.Errorf("%s defines %s() — LSP wiring belongs in packages/lib/entrypoint-lib.sh "+
				"and must be called from proveo_seed, or the sbx backend skips it", rel, m[1])
		}
	}
}

// The moved functions must still WRITE the config they used to write from the
// def entrypoint — same format, same setdefault semantics — now sourced from the
// shared lib and pointed at the agent home rather than a bare $HOME.
func TestConfigureOpencodeLspWritesTheUserConfig(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
	writeFakeServer(t, bin, "typescript-language-server")
	writeFile(t, filepath.Join(ws, "a.ts"), "x")

	// A server the operator already declared, so setdefault can be observed.
	cfgDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cfgDir, "opencode.json"),
		`{"model":"kept","lsp":{"typescript":{"command":["mine"]}}}`)

	out := runLibFn(t, bash, home, bin, ws, "configure_opencode_lsp")

	var got struct {
		Model string `json:"model"`
		LSP   map[string]struct {
			Command []string `json:"command"`
		} `json:"lsp"`
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "opencode.json"))
	if err != nil {
		t.Fatalf("opencode.json not written: %v\n%s", err, out)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("opencode.json is not valid JSON: %v\n%s", err, b)
	}
	if got.Model != "kept" {
		t.Errorf("configure_opencode_lsp clobbered unrelated config: model = %q, want %q", got.Model, "kept")
	}
	ts, ok := got.LSP["typescript"]
	if !ok {
		t.Fatalf("typescript is missing from .lsp:\n%s", b)
	}
	if len(ts.Command) != 1 || ts.Command[0] != "mine" {
		t.Errorf("the operator's own typescript entry was overwritten: %v — .lsp merges with setdefault semantics", ts.Command)
	}
}

// cursor reaches language servers through MCP, and the workspace it indexes must
// be the SCAN ROOT: /app is the docker container path and does not exist on sbx,
// where the tree is mounted at its own host path.
func TestConfigureCursorLspTargetsTheScanRoot(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
	writeFakeServer(t, bin, "typescript-language-server")
	writeFakeServer(t, bin, "mcp-language-server")
	writeFile(t, filepath.Join(ws, "a.ts"), "x")

	out := runLibFn(t, bash, home, bin, ws, "configure_cursor_lsp")

	b, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatalf("mcp.json not written: %v\n%s", err, out)
	}
	var got struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("mcp.json is not valid JSON: %v\n%s", err, b)
	}
	ts, ok := got.MCPServers["typescript"]
	if !ok {
		t.Fatalf("typescript is missing from mcpServers:\n%s", b)
	}
	if ts.Command != "mcp-language-server" {
		t.Errorf("command = %q, want mcp-language-server", ts.Command)
	}
	if len(ts.Args) < 2 || ts.Args[0] != "--workspace" || ts.Args[1] != ws {
		t.Errorf("args = %v, want --workspace %s — a hardcoded /app does not exist on the sbx backend", ts.Args, ws)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeServer(t *testing.T, bin, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runLibFn sources the shared lib with the agent home pinned to a temp dir and
// runs one wiring function against ws, exactly as proveo_seed does.
func runLibFn(t *testing.T, bash, home, bin, ws, fn string) string {
	t.Helper()
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" PATH="$3:$(dirname "$(command -v jq)"):/usr/bin:/bin"
_proveo_agent_home() { printf '%s' "$agent_home"; }
` + fn + ` "$4"
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, bin, ws).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("%s failed: %v\n%s", fn, err, out)
	}
	return string(out)
}
