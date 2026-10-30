// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml, _spec/_devops/release-gate.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runRetireLspWiring(t *testing.T, bash, home, target string) {
	t.Helper()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
proveo_retire_lsp_wiring "$2"
echo RETIRED`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), target)
	cmd.Env = append(os.Environ(), "HOME="+home, "PROVEO_HOME=", "CODEX_HOME=", "CURSOR_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RETIRED") {
		t.Fatalf("proveo_retire_lsp_wiring %s: %v\n%s", target, err, out)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", path, err, b)
	}
	return got
}

func TestRetireLspWiringStripsOnlyWhatProveoWrote(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}

	t.Run("claudecode", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		c := filepath.Join(home, ".claude")
		seedFile(t, filepath.Join(c, "skills", "proveo-lsp", ".lsp.json"), `{"bash":{}}`)
		seedFile(t, filepath.Join(c, "skills", "mine", "SKILL.md"), "# mine\n")
		seedFile(t, filepath.Join(c, "plugins", "installed_plugins.json"), `{"version":2,"plugins":{
			"typescript-lsp@claude-plugins-official":[{"scope":"user","installPath":"/opt/proveo/claude-plugins/cache/claude-plugins-official/typescript-lsp/1.0.0"}],
			"mine@elsewhere":[{"scope":"user","installPath":"/home/agent/.claude/plugins/cache/elsewhere/mine/1.0.0"}]}}`)
		seedFile(t, filepath.Join(c, "plugins", "known_marketplaces.json"), `{
			"claude-plugins-official":{"installLocation":"/opt/proveo/claude-plugins/marketplaces/claude-plugins-official"},
			"elsewhere":{"installLocation":"/home/agent/.claude/plugins/marketplaces/elsewhere"}}`)
		seedFile(t, filepath.Join(c, "settings.json"), `{"theme":"dark","enabledPlugins":{
			"typescript-lsp@claude-plugins-official":true,"mine@elsewhere":true}}`)

		runRetireLspWiring(t, bash, home, "claudecode")

		if fileExists(filepath.Join(c, "skills", "proveo-lsp")) {
			t.Error("the proveo-lsp skill dir survived")
		}
		if !fileExists(filepath.Join(c, "skills", "mine", "SKILL.md")) {
			t.Error("an operator skill was removed")
		}
		plugins, _ := readJSONFile(t, filepath.Join(c, "plugins", "installed_plugins.json"))["plugins"].(map[string]any)
		if _, ok := plugins["typescript-lsp@claude-plugins-official"]; ok {
			t.Errorf("the image-seeded plugin record survived: %v", plugins)
		}
		if _, ok := plugins["mine@elsewhere"]; !ok {
			t.Errorf("an operator plugin record was removed: %v", plugins)
		}
		known := readJSONFile(t, filepath.Join(c, "plugins", "known_marketplaces.json"))
		if _, ok := known["claude-plugins-official"]; ok {
			t.Errorf("the image-seeded marketplace survived: %v", known)
		}
		if _, ok := known["elsewhere"]; !ok {
			t.Errorf("an operator marketplace was removed: %v", known)
		}
		settings := readJSONFile(t, filepath.Join(c, "settings.json"))
		enabled, _ := settings["enabledPlugins"].(map[string]any)
		if _, ok := enabled["typescript-lsp@claude-plugins-official"]; ok || enabled["mine@elsewhere"] != true || settings["theme"] != "dark" {
			t.Errorf("settings.json = %v, want only the seeded plugin's enablement gone", settings)
		}
	})

	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cfg := filepath.Join(home, ".codex", "config.toml")
		seedFile(t, cfg, "model = \"gpt\"\n"+
			"# >>> proveo lsp (generated — edits are overwritten) >>>\n"+
			"[mcp_servers.lsp_go]\ncommand = \"mcp-language-server\"\n\n"+
			"# <<< proveo lsp <<<\n"+
			"[mcp_servers.mine]\ncommand = \"mine\"\n")
		runRetireLspWiring(t, bash, home, "codex")
		b, _ := os.ReadFile(cfg)
		if got, want := string(b), "model = \"gpt\"\n[mcp_servers.mine]\ncommand = \"mine\"\n"; got != want {
			t.Errorf("config.toml = %q, want %q", got, want)
		}
	})

	t.Run("cursor", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		mcp := filepath.Join(home, ".cursor", "mcp.json")
		seedFile(t, mcp, `{"mcpServers":{"go":{"command":"mcp-language-server","args":[]},"mine":{"command":"mine"}}}`)
		runRetireLspWiring(t, bash, home, "cursor")
		servers, _ := readJSONFile(t, mcp)["mcpServers"].(map[string]any)
		if _, ok := servers["go"]; ok || servers["mine"] == nil {
			t.Errorf("mcpServers = %v, want only the mcp-language-server entry gone", servers)
		}
	})

	t.Run("opencode", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, in string }{
			{"built-ins on", `{"model":"m","lsp":true}`},
			{"wired servers", `{"model":"m","lsp":{"typescript":{"command":["typescript-language-server","--stdio"]}}}`},
		} {
			home := t.TempDir()
			cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
			seedFile(t, cfg, tc.in)
			runRetireLspWiring(t, bash, home, "opencode")
			got := readJSONFile(t, cfg)
			if _, ok := got["lsp"]; ok || got["model"] != "m" {
				t.Errorf("%s: opencode.json = %v, want lsp gone and the rest kept", tc.name, got)
			}
		}
		home := t.TempDir()
		cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
		seedFile(t, cfg, `{"lsp":false}`)
		runRetireLspWiring(t, bash, home, "opencode")
		if b, _ := os.ReadFile(cfg); string(b) != `{"lsp":false}` {
			t.Errorf("an explicit lsp:false was rewritten: %s", b)
		}
	})
}

func TestRetireLspWiringRunsWithEveryConfigWiring(t *testing.T) {
	t.Parallel()
	body := funcBody(t, entrypointLib(t), "proveo_wire_config")
	if !strings.Contains(body, `proveo_retire_lsp_wiring "$target"`) {
		t.Error("proveo_wire_config must call proveo_retire_lsp_wiring, or persisted LSP wiring outlives the removal")
	}
}
