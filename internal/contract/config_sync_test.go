// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml, _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// What step 3 exists for: a wired MCP server, a skill and a settings merge
// survive a teardown and are in place before the next run's agent reads them.
func TestConfigSyncRoundTripsTheHarnessConfiguration(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": cfgSet}

	// A previous run persisted settings and a skill.
	seedFile(t, filepath.Join(state, ".claude", "settings.json"), `{"enabledPlugins":{"example@x":true}}`)
	seedFile(t, filepath.Join(state, ".claude", "skills", "example", "SKILL.md"), "# example\n")
	runConfigSync(t, bash, env, "restore")

	for _, rel := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "skills", "example", "SKILL.md"),
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

func TestSeedRestoresConfigBeforeAnythingWritesIt(t *testing.T) {
	t.Parallel()
	seed := seedBody(t, entrypointLib(t))
	restore := strings.Index(seed, "proveo_sync_config restore")
	if restore < 0 {
		t.Fatal("proveo_seed never restores the harness configuration")
	}
	for _, writer := range []string{
		"render_subagents", "proveo_wire_config",
		"proveo_compose_house_rules", "proveo_apply_ui_defaults", "proveo_install_claude_hooks",
		"proveo_install_git_sync_hooks",
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

// A path the manifest marks transient never crosses the sandbox boundary, in
// either direction, and the host copy stays where it is.
func TestConfigSyncSkipsTransientPathsBothWays(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": ".claude|.claude|jobs/*/tmp"}

	seedFile(t, filepath.Join(state, ".claude", "jobs", "j1", "state.json"), "{}")
	seedFile(t, filepath.Join(state, ".claude", "jobs", "j1", "tmp", "probe", "main.go"), "package main")
	runConfigSync(t, bash, env, "restore")
	if _, err := os.Stat(filepath.Join(agent, ".claude", "jobs", "j1", "state.json")); err != nil {
		t.Errorf("restore dropped the job's own state beside its scratch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, ".claude", "jobs", "j1", "tmp")); err == nil {
		t.Error("restore copied jobs/j1/tmp into the sandbox — the manifest marks it transient")
	}

	seedFile(t, filepath.Join(agent, ".claude", "jobs", "j2", "tmp", "render.svg"), "<svg/>")
	runConfigSync(t, bash, env, "save")
	if _, err := os.Stat(filepath.Join(state, ".claude", "jobs", "j2", "tmp")); err == nil {
		t.Error("save carried jobs/j2/tmp to the operator's home — the manifest marks it transient")
	}
	if _, err := os.Stat(filepath.Join(state, ".claude", "jobs", "j1", "tmp", "probe", "main.go")); err != nil {
		t.Errorf("the sync deleted the host's own transient copy: %v", err)
	}
}

// The restore races the agent's 30s gate, so it must not fork a copy per file;
// it must still replace only what is newer and create what is missing.
func TestConfigSyncCopiesInBulk(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	realCp, err := exec.LookPath("cp")
	if err != nil {
		t.Skipf("cp unavailable: %v", err)
	}
	agent, state, bin := t.TempDir(), t.TempDir(), t.TempDir()
	calls := filepath.Join(t.TempDir(), "cp.calls")
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\necho x >> '"+calls+"'\nexec '"+realCp+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	const many = 400
	for i := range many {
		seedFile(t, filepath.Join(state, ".claude", "file-history", "s", strings.Repeat("f", 1+i%7)+"-"+strconv.Itoa(i)), "v")
	}
	seedFile(t, filepath.Join(state, ".claude", "with space", "a b.json"), "{}")
	if err := os.MkdirAll(filepath.Join(state, ".claude", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedFile(t, filepath.Join(state, ".claude", "settings.json"), "host-newer")
	seedFile(t, filepath.Join(agent, ".claude", "settings.json"), "agent-older")
	seedFile(t, filepath.Join(state, ".claude", "keep.json"), "host-older")
	seedFile(t, filepath.Join(agent, ".claude", "keep.json"), "agent-newer")
	old, mid, now := time.Unix(1_700_000_000, 0), time.Unix(1_750_000_000, 0), time.Unix(1_780_000_000, 0)
	for _, f := range []struct {
		path string
		at   time.Time
	}{
		{filepath.Join(agent, ".claude", "settings.json"), old},
		{filepath.Join(state, ".claude", "settings.json"), now},
		{filepath.Join(state, ".claude", "keep.json"), mid},
		{filepath.Join(agent, ".claude", "keep.json"), now},
	} {
		if err := os.Chtimes(f.path, f.at, f.at); err != nil {
			t.Fatal(err)
		}
	}

	runConfigSync(t, bash, map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state,
		"PROVEO_CONFIG_DIRS": cfgSet, "PATH": bin + ":" + os.Getenv("PATH")}, "restore")

	b, _ := os.ReadFile(calls)
	if n := strings.Count(string(b), "x"); n > 10 {
		t.Errorf("restore forked cp %d times for %d files — the copy must be batched", n, many+3)
	}
	entries, _ := os.ReadDir(filepath.Join(agent, ".claude", "file-history", "s"))
	if len(entries) != many {
		t.Errorf("restore placed %d of %d new files", len(entries), many)
	}
	for _, c := range []struct{ rel, want string }{
		{filepath.Join(".claude", "with space", "a b.json"), "{}"},
		{filepath.Join(".claude", "settings.json"), "host-newer"},
		{filepath.Join(".claude", "keep.json"), "agent-newer"},
	} {
		if got, err := os.ReadFile(filepath.Join(agent, c.rel)); err != nil || string(got) != c.want {
			t.Errorf("after restore %s = %q (%v), want %q", c.rel, got, err, c.want)
		}
	}
	if fi, err := os.Stat(filepath.Join(agent, ".claude", "empty")); err != nil || !fi.IsDir() {
		t.Errorf("restore did not create the empty directory: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(agent, ".claude", "settings.json")); err != nil || !fi.ModTime().Equal(now) {
		t.Errorf("a replaced file must keep the source mtime, or the next save sees it as changed: %v", err)
	}
}

// The newer-than test compares to the nanosecond: a float would round two
// sub-second stamps in the same second together.
func TestConfigSyncComparesSubSecondMtimes(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	sec := int64(1_780_000_000)
	for _, c := range []struct {
		name          string
		host, sandbox int64
		want          string
	}{
		{"newer.json", 600_000_123, 600_000_122, "host"},
		{"older.json", 300_000_000, 600_000_000, "sandbox"},
		{"equal.json", 500_000_000, 500_000_000, "sandbox"},
	} {
		h, s := filepath.Join(state, ".claude", c.name), filepath.Join(agent, ".claude", c.name)
		seedFile(t, h, "host")
		seedFile(t, s, "sandbox")
		if err := os.Chtimes(h, time.Unix(sec, c.host), time.Unix(sec, c.host)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(s, time.Unix(sec, c.sandbox), time.Unix(sec, c.sandbox)); err != nil {
			t.Fatal(err)
		}
		defer func(name, want string) {
			if b, _ := os.ReadFile(filepath.Join(agent, ".claude", name)); string(b) != want {
				t.Errorf("after restore %s = %q, want %q", name, b, want)
			}
		}(c.name, c.want)
	}
	runConfigSync(t, bash, map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": cfgSet}, "restore")
}

// A transient glob is a find pattern, never a shell glob: a cwd that happens to
// hold a matching path must not turn it into that literal path.
func TestConfigSyncNeverGlobsTheSkipListAgainstTheCwd(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "jobs", "build", "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedFile(t, filepath.Join(state, ".claude", "jobs", "j1", "tmp", "scratch"), "x")
	script := `source "$1/packages/lib/entrypoint-lib.sh"
proveo_sync_config restore
echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PROVEO_CONFIG_SYNC=", "PROVEO_HOME=", "HOME="+agent,
		"PROVEO_STATE_HOME="+state, "PROVEO_CONFIG_DIRS=.claude|.claude|jobs/*/tmp")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "rc=0") {
		t.Fatalf("restore failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(agent, ".claude", "jobs", "j1", "tmp")); err == nil {
		t.Error("restore copied jobs/j1/tmp: the skip list was globbed against the cwd, so jobs/*/tmp became ./jobs/build/tmp")
	}
}
