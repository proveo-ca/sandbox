// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml
package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
)

func TestEveryHarnessHomeMountReachesTheConfigSet(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no manifests embedded")
	}
	saw := false
	for _, m := range ms {
		if !m.Home.Active() {
			continue
		}
		saw = true
		set := proveohome.ConfigSet(m.Home)
		synced := 0
		for _, mt := range m.Home.Mounts {
			if strings.TrimSpace(mt.Container) != "" {
				synced++
			} else if strings.Contains(set, strings.Trim(mt.Host, "/")+"|") {
				t.Errorf("%s: %s has no container path, so it is a direct bind, yet it reached the config set %q", m.Name, mt.Host, set)
			}
		}
		if got := len(strings.FieldsFunc(set, func(r rune) bool { return r == ';' })); got != synced {
			t.Errorf("%s: encoded %d of %d synced home mounts (%q) — a dropped mount is config "+
				"that stops persisting on sbx", m.Name, got, synced, set)
		}
		// Whatever the manifest denies must reach the shell's skip set, or a file
		// the host scrubs is a file the sandbox copies back out.
		for _, mt := range m.Home.Mounts {
			for _, d := range mt.Deny {
				if d = strings.TrimSpace(d); d != "" && !strings.Contains(set, d) {
					t.Errorf("%s: deny %q is not in the config set %q", m.Name, d, set)
				}
			}
		}
	}
	if !saw {
		t.Fatal("no harness declares a durable home — the fleet assertion is vacuous")
	}
}

// A transient path reaches the shell's prune list; claudecode's job scratch is one.
func TestEveryTransientPathReachesTheConfigSet(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	sawJobs := false
	for _, m := range ms {
		set := proveohome.ConfigSet(m.Home)
		for _, mt := range m.Home.Mounts {
			for _, p := range mt.Transient {
				if !strings.Contains(set, p) {
					t.Errorf("%s: transient %q is not in the config set %q", m.Name, p, set)
				}
				if m.Name == "claudecode" && p == "jobs/*/tmp" {
					sawJobs = true
				}
			}
		}
	}
	if !sawJobs {
		t.Error("claudecode no longer marks jobs/*/tmp transient — one job's scratch cost 32s of every boot (measured 2026-10-13)")
	}
}

// codex's app-server daemon state stays in the sandbox that installed it; its config and login still sync.
// SPEC: _spec/defs/codex/codex-topology.puml
func TestCodexDaemonStateNeverCrossesTheSandbox(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	var home manifest.Home
	for _, m := range ms {
		if m.Name == "codex" {
			home = m.Home
		}
	}
	set := proveohome.ConfigSet(home)
	if set == "" {
		t.Fatal("codex declares no synced home mount")
	}
	daemon := []string{"packages", "app-server-daemon", "app-server-control"}

	// The host copy an earlier proveo left: the release tree without its `current` link.
	state, agent := t.TempDir(), t.TempDir()
	seedFile(t, filepath.Join(state, ".codex", "config.toml"), "model = \"x\"\n")
	seedFile(t, filepath.Join(state, ".codex", "auth.json"), "{}")
	seedFile(t, filepath.Join(state, ".codex", "packages", "app-server-daemon", "releases", "0.167.0-x", "bin", "codex"), "elf")
	seedFile(t, filepath.Join(state, ".codex", "packages", "app-server-daemon", "selection-history"), "0.167.0-x")
	seedFile(t, filepath.Join(state, ".codex", "app-server-daemon", "daemon.pid"), `{"pid":1}`)
	runConfigSync(t, bash, map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_CONFIG_DIRS": set}, "restore")
	for _, rel := range []string{"config.toml", "auth.json"} {
		if _, err := os.Stat(filepath.Join(agent, ".codex", rel)); err != nil {
			t.Errorf("restore dropped .codex/%s: %v", rel, err)
		}
	}
	for _, d := range daemon {
		if _, err := os.Lstat(filepath.Join(agent, ".codex", d)); err == nil {
			t.Errorf("restore copied .codex/%s into the sandbox; codex then reports \"daemon executable not found\"", d)
		}
	}

	// A sandbox's own install: an absolute `current` link and a socket link into /tmp.
	state2, agent2 := t.TempDir(), t.TempDir()
	rel := filepath.Join(agent2, ".codex", "packages", "app-server-daemon", "releases", "0.167.0-x")
	seedFile(t, filepath.Join(rel, "bin", "codex"), "elf")
	if err := os.Symlink(rel, filepath.Join(agent2, ".codex", "packages", "app-server-daemon", "current")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, filepath.Join(agent2, ".codex", "app-server-daemon", "daemon.pid"), `{"pid":1}`)
	if err := os.MkdirAll(filepath.Join(agent2, ".codex", "app-server-control"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/codex-daemon-1000/sock", filepath.Join(agent2, ".codex", "app-server-control", "app-server-control.sock")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, filepath.Join(agent2, ".codex", "config.toml"), "model = \"y\"\n")
	runConfigSync(t, bash, map[string]string{"HOME": agent2, "PROVEO_STATE_HOME": state2, "PROVEO_CONFIG_DIRS": set}, "save")
	if _, err := os.Stat(filepath.Join(state2, ".codex", "config.toml")); err != nil {
		t.Errorf("save dropped .codex/config.toml: %v", err)
	}
	for _, d := range daemon {
		if _, err := os.Lstat(filepath.Join(state2, ".codex", d)); err == nil {
			t.Errorf("save carried .codex/%s to the operator's home", d)
		}
	}
}

func TestHermesDataIsBoundNotCopiedIntoTheAgentHome(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	var home manifest.Home
	for _, m := range ms {
		if m.Name == "hermes" {
			home = m.Home
		}
	}
	if len(home.Mounts) != 1 || home.Mounts[0].Host != "hermes/data" {
		t.Fatalf("hermes home mounts = %+v, want the one hermes/data mount", home.Mounts)
	}
	state, agent := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "hermes", "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "hermes", "data", "state.db"), []byte("sessions"), 0o600); err != nil {
		t.Fatal(err)
	}
	runConfigSync(t, bash, map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state,
		"PROVEO_CONFIG_DIRS": proveohome.ConfigSet(home)}, "restore")
	if _, err := os.Stat(filepath.Join(agent, "data", "state.db")); err == nil {
		t.Error("the seed copied hermes's durable home into the agent home; HERMES_HOME already points at the bound host path, so the copy is dead weight on every launch")
	}
}
