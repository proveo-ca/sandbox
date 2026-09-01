// SPEC: _spec/defs/claudecode/lsp-plugins-seed.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var (
	seedLoopRe  = regexp.MustCompile(`for p in ((?:[a-z0-9-]+-lsp\s*)+); do`)
	shellListRe = regexp.MustCompile(`_claude_lsp_plugins\(\)\s*\{\s*echo\s+"([^"]+)"`)
	// Plugin names carry hyphens (typescript-lsp), which quotedEchoRe's [a-z]+ does not.
	dashedEchoRe = regexp.MustCompile(`(?m)^\s*([a-z0-9-]+)\)\s+echo\s+"([^"]+)"`)
)

func dashedEchoArms(t *testing.T, src, fn string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range dashedEchoRe.FindAllStringSubmatch(caseBody(t, src, fn), -1) {
		out[m[1]] = strings.Fields(m[2])
	}
	return out
}

func sortedFields(s string) []string {
	f := strings.Fields(s)
	sort.Strings(f)
	return f
}

// The image seeds a list of official plugins; the seed step enables from a table.
// A plugin in one and not the other is either baked and never enabled (dead
// weight) or enabled without a cache to load from (a prompt to install — the very
// thing the seed exists to remove).
func TestSeededLspPluginsMatchTheEnablementTable(t *testing.T) {
	t.Parallel()
	df, err := os.ReadFile(filepath.Join(repoRoot(t), "defs", "claudecode", "mcp", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	m := seedLoopRe.FindStringSubmatch(string(df))
	if m == nil {
		t.Fatal("the claudecode Dockerfile no longer seeds official LSP plugins in a `for p in … -lsp; do` loop")
	}
	if !strings.Contains(string(df), "ENV CLAUDE_CODE_PLUGIN_SEED_DIR=") {
		t.Error("the Dockerfile must export CLAUDE_CODE_PLUGIN_SEED_DIR, or Claude Code never reads the seed")
	}
	src := entrypointLib(t)
	sm := shellListRe.FindStringSubmatch(src)
	if sm == nil {
		t.Fatal("_claude_lsp_plugins not found in entrypoint-lib.sh")
	}
	if diff := cmp.Diff(sortedFields(m[1]), sortedFields(sm[1])); diff != "" {
		t.Errorf("Dockerfile seed list vs _claude_lsp_plugins (-dockerfile +lib):\n%s", diff)
	}
	binaries := dashedEchoArms(t, src, "_claude_lsp_plugin_binary")
	langs := dashedEchoArms(t, src, "_claude_lsp_plugin_lang")
	for _, p := range strings.Fields(sm[1]) {
		if len(binaries[p]) != 1 {
			t.Errorf("%s has no binary row — the seed step cannot decide whether to enable it", p)
		}
		if len(langs[p]) != 1 {
			t.Errorf("%s has no language row — proveo-lsp would declare a second server for its extensions", p)
		}
	}
	if _, ok := binaries["kotlin-lsp"]; ok {
		t.Error("kotlin-lsp expects a `kotlin-lsp` binary this floor does not ship; proveo-lsp covers Kotlin")
	}
}

type settingsShape struct {
	Theme          string          `json:"theme"`
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

// A fake seed and a fake PATH: only plugins whose binary is present are enabled,
// an operator's explicit false survives, and a second run changes nothing.
func TestSeedEnablesOfficialLspPluginsOnlyForPresentBinaries(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	seed, home, bin := t.TempDir(), t.TempDir(), t.TempDir()
	for _, p := range []string{"typescript-lsp", "pyright-lsp", "gopls-lsp", "rust-analyzer-lsp", "clangd-lsp", "jdtls-lsp", "lua-lsp"} {
		if err := os.MkdirAll(filepath.Join(seed, "cache", "claude-plugins-official", p, "1.0.0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The seed as `claude plugin install` leaves it under CLAUDE_CODE_PLUGIN_CACHE_DIR:
	// records beside the caches (only typescript-lsp recorded; the rest exercise the
	// directory fallback).
	if err := os.MkdirAll(filepath.Join(seed, "marketplaces", "claude-plugins-official"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "known_marketplaces.json"), []byte(`{"claude-plugins-official":{"source":{"source":"github","repo":"anthropics/claude-plugins-official"},"installLocation":"/somewhere/else","lastUpdated":"2026-09-01T00:00:00Z"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "installed_plugins.json"), []byte(`{"version":2,"plugins":{"typescript-lsp@claude-plugins-official":[{"scope":"user","installPath":"`+filepath.Join(seed, "cache", "claude-plugins-official", "typescript-lsp", "1.0.0")+`","version":"1.0.0","installedAt":"2026-09-01T00:00:00Z","lastUpdated":"2026-09-01T00:00:00Z"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"typescript-language-server", "gopls"} { // pyright, rust-analyzer… absent
		if err := os.WriteFile(filepath.Join(bin, b), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// PATH holds ONLY the fakes plus the tools the lib itself needs: the host may
	// well have a real pyright-langserver or clangd, and they must not leak in.
	tools := t.TempDir()
	for _, tool := range []string{"tr", "sed", "awk", "dirname", "head", "cat", "mkdir", "node"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s unavailable", tool)
		}
		if err := os.Symlink(p, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	// The operator turned gopls-lsp off on purpose; that must hold.
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","enabledPlugins":{"gopls-lsp@claude-plugins-official":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" CLAUDE_CODE_PLUGIN_SEED_DIR="$3" PATH="$4:$5"
_proveo_agent_home() { printf '%s' "$agent_home"; }
proveo_enable_claude_lsp_plugins claudecode
echo "OFFICIAL=[$PROVEO_CLAUDE_LSP_OFFICIAL]"
proveo_enable_claude_lsp_plugins claudecode
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, seed, bin, tools).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("seed step failed: %v\n%s", err, out)
	}
	// gopls is on PATH but the operator disabled gopls-lsp, so Go stays with
	// proveo-lsp: only typescript is handed to an official plugin.
	if !strings.Contains(string(out), "OFFICIAL=[typescript]") {
		t.Errorf("PROVEO_CLAUDE_LSP_OFFICIAL must name exactly the languages the ENABLED plugins cover (typescript):\n%s", out)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var got settingsShape
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("settings.json is not JSON after the merge: %v\n%s", err, b)
	}
	want := map[string]bool{
		"typescript-lsp@claude-plugins-official": true,
		"gopls-lsp@claude-plugins-official":      false, // operator's choice, untouched
	}
	if diff := cmp.Diff(want, got.EnabledPlugins); diff != "" {
		t.Errorf("enabledPlugins (-want +got):\n%s\nonly plugins with a binary on PATH are enabled; pyright-langserver was absent", diff)
	}
	if got.Theme != "dark" {
		t.Errorf("operator's theme lost: %q", got.Theme)
	}

	// The records Claude Code installs by, pointing at the seed. Measured on 2.1.251:
	// without them the seed is "marketplace not registered" and loads nothing.
	var inst struct {
		Plugins map[string][]struct {
			Scope, InstallPath, Version string
		} `json:"plugins"`
	}
	ib, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		t.Fatalf("installed_plugins.json not written: %v", err)
	}
	if err := json.Unmarshal(ib, &inst); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"typescript-lsp", "gopls-lsp"} { // both binaries present; gopls-lsp disabled but still INSTALLED
		recs := inst.Plugins[p+"@claude-plugins-official"]
		if len(recs) != 1 || recs[0].Scope != "user" || recs[0].Version != "1.0.0" ||
			recs[0].InstallPath != filepath.Join(seed, "cache", "claude-plugins-official", p, "1.0.0") {
			t.Errorf("%s install record wrong or missing: %+v", p, recs)
		}
	}
	if _, ok := inst.Plugins["pyright-lsp@claude-plugins-official"]; ok {
		t.Error("pyright-lsp recorded as installed although its binary is absent")
	}
	var known map[string]struct {
		InstallLocation string `json:"installLocation"`
	}
	kb, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"))
	if err != nil {
		t.Fatalf("known_marketplaces.json not written: %v", err)
	}
	if err := json.Unmarshal(kb, &known); err != nil {
		t.Fatal(err)
	}
	if got := known["claude-plugins-official"].InstallLocation; got != filepath.Join(seed, "marketplaces", "claude-plugins-official") {
		t.Errorf("marketplace installLocation = %q, want the seed's clone, not the build-time path recorded inside the seed", got)
	}
}

// configure_claude_lsp must not declare a language an enabled official plugin
// already serves: two servers on one extension leaves one never starting.
func TestProveoLspYieldsLanguagesToEnabledOfficialPlugins(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	home, bin, ws := t.TempDir(), t.TempDir(), t.TempDir()
	for _, b := range []string{"typescript-language-server", "bash-language-server"} {
		if err := os.WriteFile(filepath.Join(bin, b), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"a.ts", "b.sh"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" PATH="$3:$(dirname "$(command -v jq)"):/usr/bin:/bin"
_proveo_agent_home() { printf '%s' "$agent_home"; }
export PROVEO_CLAUDE_LSP_OFFICIAL="typescript"
configure_claude_lsp "$4"
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, bin, ws).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("configure_claude_lsp failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "proveo-lsp", ".lsp.json"))
	if err != nil {
		t.Fatalf("proveo-lsp was not written (bash should still be declared): %v\n%s", err, out)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(b, &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["typescript"]; ok {
		t.Errorf("proveo-lsp still declares typescript although typescript-lsp is enabled:\n%s", b)
	}
	if _, ok := servers["bash"]; !ok {
		t.Errorf("proveo-lsp dropped bash, which no official plugin covers:\n%s", b)
	}
}
