// SPEC: _spec/_plans/config-seeding-and-persistence.puml, _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runConfigSync sources the lib and runs one direction of the config sync.
func runConfigSync(t *testing.T, bash string, env map[string]string, mode string) string {
	t.Helper()
	script := `source "$1/packages/lib/entrypoint-lib.sh"
proveo_sync_config ` + mode + `
echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(),
		"PROVEO_HOME=", "PROVEO_STATE_HOME=", "PROVEO_CONFIG_DIRS=", "PROVEO_CONFIG_SYNC=")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo_sync_config %s failed: %v\n%s", mode, err, out)
	}
	if !strings.Contains(string(out), "rc=0") {
		t.Fatalf("proveo_sync_config %s did not succeed:\n%s", mode, out)
	}
	return string(out)
}

// The claudecode/cursor/opencode shape, as ConfigSet encodes it.
const cfgSet = ".claude|.claude|;.cursor|.cursor|auth.json"

// What step 3 exists for: a wired MCP server, an LSP config and a settings merge
// survive a teardown and are in place before the next run's agent reads them.
func TestConfigSyncRoundTripsTheHarnessConfiguration(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": cfgSet}

	// A previous run persisted settings and a proveo-lsp plugin.
	seedFile(t, filepath.Join(state, ".claude", "settings.json"), `{"enabledPlugins":{"gopls-lsp@x":true}}`)
	seedFile(t, filepath.Join(state, ".claude", "skills", "proveo-lsp", ".lsp.json"), `{"bash":{}}`)
	runConfigSync(t, bash, env, "restore")

	for _, rel := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "skills", "proveo-lsp", ".lsp.json"),
	} {
		if _, err := os.Stat(filepath.Join(agent, rel)); err != nil {
			t.Fatalf("restore did not place %s where the agent reads it: %v", rel, err)
		}
	}

	// This run wires cursor's MCP servers; teardown must carry them out.
	seedFile(t, filepath.Join(agent, ".cursor", "mcp.json"), `{"mcpServers":{"typescript":{}}}`)
	runConfigSync(t, bash, env, "save")
	if b, err := os.ReadFile(filepath.Join(state, ".cursor", "mcp.json")); err != nil ||
		!strings.Contains(string(b), "typescript") {
		t.Fatalf("save did not carry the wired MCP config to the durable root: %v", err)
	}
}

// A credential must never ride out on a config copy. The manifest denies it and
// the host scrubs it; the sandbox copy has to agree, or the sync reintroduces
// exactly the file proveohome.scrubDeny removes.
func TestConfigSyncNeverCarriesADeniedFileOut(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": cfgSet}

	seedFile(t, filepath.Join(agent, ".cursor", "auth.json"), `{"token":"SECRET"}`)
	seedFile(t, filepath.Join(agent, ".cursor", "cli-config.json"), `{"permissions":{}}`)
	runConfigSync(t, bash, env, "save")

	if _, err := os.Stat(filepath.Join(state, ".cursor", "auth.json")); err == nil {
		t.Error("auth.json reached the operator's home — the manifest denies it")
	}
	if _, err := os.Stat(filepath.Join(state, ".cursor", "cli-config.json")); err != nil {
		t.Errorf("the deny skipped the whole subtree instead of one file: %v", err)
	}
}

// State and config must not both move the same bytes. The volumes sbx owns are
// state; the config sync prunes every one of them, telemetry included, which is
// what keeps `statsig` and `shell-snapshots` out of the operator's home.
func TestConfigSyncPrunesTheVolumesStateOwns(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()

	seedFile(t, filepath.Join(agent, ".claude", "settings.json"), "{}")
	seedFile(t, filepath.Join(agent, ".claude", "projects", "big.jsonl"), "transcript")
	seedFile(t, filepath.Join(agent, ".claude", "statsig", "telemetry"), "noise")

	// _proveo_volume_mounts reads /proc/mounts; stub it to declare the two dirs
	// sbx would have mounted as per-sandbox volumes.
	script := `source "$1/packages/lib/entrypoint-lib.sh"
_proveo_volume_mounts() { printf '%s\n' "$HOME/.claude/projects" "$HOME/.claude/statsig"; }
proveo_sync_config save
echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), agent)
	cmd.Env = append(os.Environ(), "PROVEO_CONFIG_SYNC=",
		"HOME="+agent, "PROVEO_HOME=", "PROVEO_STATE_HOME="+state, "PROVEO_CONFIG_DIRS="+cfgSet)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "rc=0") {
		t.Fatalf("save failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(state, ".claude", "settings.json")); err != nil {
		t.Errorf("settings.json did not persist: %v", err)
	}
	for _, rel := range []string{
		filepath.Join(".claude", "projects", "big.jsonl"),
		filepath.Join(".claude", "statsig", "telemetry"),
	} {
		if _, err := os.Stat(filepath.Join(state, rel)); err == nil {
			t.Errorf("%s was copied by the config sync — that is a volume, and state owns it", rel)
		}
	}
}

func TestConfigSyncIsANoOpOnDockerAndUnderTheOptOut(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)

	// Docker: no PROVEO_STATE_HOME, because the home already IS the host dir.
	agent := t.TempDir()
	seedFile(t, filepath.Join(agent, ".claude", "settings.json"), "{}")
	runConfigSync(t, bash, map[string]string{"HOME": agent, "PROVEO_CONFIG_DIRS": cfgSet}, "save")

	// Opt-out, both directions.
	agent2, state2 := t.TempDir(), t.TempDir()
	off := map[string]string{"HOME": agent2, "PROVEO_STATE_HOME": state2,
		"PROVEO_CONFIG_DIRS": cfgSet, "PROVEO_CONFIG_SYNC": "off"}
	seedFile(t, filepath.Join(state2, ".claude", "settings.json"), "{}")
	runConfigSync(t, bash, off, "restore")
	if _, err := os.Stat(filepath.Join(agent2, ".claude", "settings.json")); err == nil {
		t.Error("restore ran with PROVEO_CONFIG_SYNC=off")
	}
	seedFile(t, filepath.Join(agent2, ".cursor", "mcp.json"), "{}")
	runConfigSync(t, bash, off, "save")
	if _, err := os.Stat(filepath.Join(state2, ".cursor", "mcp.json")); err == nil {
		t.Error("save ran with PROVEO_CONFIG_SYNC=off")
	}
}

// An unknown mode must be refused: teardown calls this by name, and a typo that
// fell through to one of the two directions would look like a successful copy.
func TestConfigSyncRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	script := `source "$1/packages/lib/entrypoint-lib.sh"; proveo_sync_config sideways; echo "rc=$?"`
	out, _ := exec.Command(bash, "-c", script, "bash", repoRoot(t)).CombinedOutput()
	if !strings.Contains(string(out), "rc=2") {
		t.Errorf("proveo_sync_config with an unknown mode gave %q, want rc=2", out)
	}
}

// Ordering: every configure_* step merges with setdefault semantics, so a
// config restored after them loses the operator's persisted values to this run's
// defaults — and does it silently, because a merge that overwrites nothing looks
// exactly like a merge that had nothing to overwrite.
func TestSeedRestoresConfigBeforeAnythingWritesIt(t *testing.T) {
	t.Parallel()
	seed := seedBody(t, entrypointLib(t))
	restore := strings.Index(seed, "proveo_sync_config restore")
	if restore < 0 {
		t.Fatal("proveo_seed never restores the harness configuration")
	}
	// Everything in proveo_seed that WRITES into the agent home. The per-class
	// wiring is behind proveo_wire_config now, so that entry point stands in for
	// configure_claude_lsp / configure_opencode_lsp / configure_cursor_lsp /
	// configure_cecli_mcp / configure_claude_plugins — see
	// TestConfigWiringIsReachableFromTheSeed for the rest of that path.
	for _, writer := range []string{
		"render_subagents", "proveo_wire_config",
		"proveo_compose_house_rules", "proveo_apply_ui_defaults", "proveo_install_claude_hooks",
	} {
		at := strings.Index(seed, writer)
		if at < 0 {
			t.Errorf("%s is no longer called from proveo_seed", writer)
			continue
		}
		if at < restore {
			t.Errorf("%s runs BEFORE the config restore, so the restore overwrites what it wrote", writer)
		}
	}
}
