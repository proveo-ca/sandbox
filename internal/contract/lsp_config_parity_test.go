// SPEC: _spec/packages/lib/language-server-provisioning.puml, _spec/internal/sbx/sbx-kit-contract.puml
package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var configWiring = []struct {
	class, target, fn string
}{
	{"lsp", "claudecode", "configure_claude_lsp"},
	{"lsp", "codex", "configure_codex_lsp"},
	{"lsp", "opencode", "configure_opencode_lsp"},
	{"lsp", "cursor", "configure_cursor_lsp"},
	{"formatter", "opencode", "configure_opencode_formatter"},
	{"mcp", "cecli", "configure_cecli_mcp"},
	{"plugin", "claudecode", "configure_claude_plugins"},
}

func TestConfigWiringIsReachableFromTheSeed(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)
	seed := seedBody(t, src)

	if !strings.Contains(seed, "proveo_wire_config") {
		t.Fatal("proveo_seed no longer calls proveo_wire_config — nothing wires any class")
	}
	table := caseArms(t, src, "_proveo_class_wire")

	for _, w := range configWiring {
		key := w.class + ":" + w.target
		if got := table[key]; got != w.fn {
			t.Errorf("_proveo_class_wire %s = %q, want %q", key, got, w.fn)
		}
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(w.fn) + `\(\)\s*\{`).MatchString(src) {
			t.Errorf("%s is not defined in packages/lib/entrypoint-lib.sh — a wiring step "+
				"outside the shared lib cannot run on the sbx backend", w.fn)
		}
	}
	// And nothing in the table is unaccounted for: a row added here without a row
	// above is a class nobody reviewed.
	for key := range table {
		found := false
		for _, w := range configWiring {
			if w.class+":"+w.target == key {
				found = true
			}
		}
		if !found {
			t.Errorf("_proveo_class_wire declares %q, which this table does not cover", key)
		}
	}
}

func TestPluginClassIsWiredBeforeLsp(t *testing.T) {
	t.Parallel()
	classes := classOrder(t, entrypointLib(t))
	plugin, lsp := indexOf(classes, "plugin"), indexOf(classes, "lsp")
	if plugin < 0 || lsp < 0 {
		t.Fatalf("_proveo_config_classes = %v, want it to name both plugin and lsp", classes)
	}
	if plugin > lsp {
		t.Errorf("_proveo_config_classes = %v — plugin must precede lsp, or proveo-lsp claims "+
			"a language an enabled official plugin already serves", classes)
	}
}

// caseArms reads a `case "$1:$2" in <key>) echo "<value>" ;;` table.
func caseArms(t *testing.T, src, fn string) map[string]string {
	t.Helper()
	start := strings.Index(src, fn+"() {")
	if start < 0 {
		t.Fatalf("%s not found in entrypoint-lib.sh", fn)
	}
	rest := src[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("%s has no closing brace", fn)
	}
	out := map[string]string{}
	arm := regexp.MustCompile(`(?m)^\s*([a-z]+:[a-z]+]?)\)\s+echo\s+"([^"]+)"`)
	for _, m := range arm.FindAllStringSubmatch(rest[:end], -1) {
		out[strings.TrimSuffix(m[1], "]")] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("parsed zero arms from %s — the parser has drifted", fn)
	}
	return out
}

func classOrder(t *testing.T, src string) []string {
	t.Helper()
	m := regexp.MustCompile(`_proveo_config_classes\(\)\s*\{\s*echo\s+"([^"]+)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("_proveo_config_classes not found in entrypoint-lib.sh")
	}
	return strings.Fields(m[1])
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

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

// opencode reads an ABSENT lsp key as disabled — "Omit or set to false to
// disable, true to enable built-ins" (opencode.ai/config.json). The function
// used to return early when nothing matched, writing no key at all, which is
// what printed "LSPs are disabled" on a workspace whose languages proveo could
// not match. SPEC: _spec/packages/lib/language-server-provisioning.puml
func TestConfigureOpencodeLspEnablesBuiltinsWhenNothingMatched(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	cases := []struct {
		name     string
		existing string   // "" writes no file at all
		wantLSP  any      // true, false, or a map for the object form
		keepKeys []string // unrelated config that must survive
	}{
		{name: "no config at all", existing: "", wantLSP: true},
		{name: "config without an lsp key", existing: `{"model":"kept"}`, wantLSP: true, keepKeys: []string{"kept"}},
		{name: "an operator who turned it off keeps it off", existing: `{"lsp":false}`, wantLSP: false},
		{name: "an existing object is left alone", existing: `{"lsp":{"gopls":{"command":["gopls"]}}}`, wantLSP: "object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
			// A workspace with no source file matches no installed server.
			cfgDir := filepath.Join(home, ".config", "opencode")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(cfgDir, "opencode.json")
			if tc.existing != "" {
				writeFile(t, cfg, tc.existing)
			}

			out := runLibFn(t, bash, home, bin, ws, "configure_opencode_lsp")

			b, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("configure_opencode_lsp wrote no config, so opencode reads LSP as disabled: %v\n%s", err, out)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("opencode.json is not valid JSON: %v\n%s", err, b)
			}
			lsp, present := got["lsp"]
			if !present {
				t.Fatalf("no lsp key, which opencode reads as disabled:\n%s", b)
			}
			switch want := tc.wantLSP.(type) {
			case bool:
				if lsp != want {
					t.Errorf("lsp = %v, want %v\n%s", lsp, want, b)
				}
			case string: // "object"
				if _, ok := lsp.(map[string]any); !ok {
					t.Errorf("lsp = %v, want the operator's object left intact\n%s", lsp, b)
				}
			}
			for _, k := range tc.keepKeys {
				if !bytes.Contains(b, []byte(k)) {
					t.Errorf("unrelated config %q was lost:\n%s", k, b)
				}
			}
		})
	}
}
