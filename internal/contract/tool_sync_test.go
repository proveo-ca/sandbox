// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml, _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runToolSync sources the lib with a stubbed `uname -m` and runs one sync
// direction, returning its combined output.
func runToolSync(t *testing.T, bash string, env map[string]string, mode string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(
		"#!/bin/sh\ncase \"$1\" in -m) echo aarch64 ;; *) echo Linux ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
proveo_sync_tools ` + mode
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_HOME=", "PROVEO_STATE_HOME=", "_PROVEO_TOOL_HOME=", "PROVEO_TOOL_SYNC=")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo_sync_tools %s failed: %v\n%s", mode, err, out)
	}
	return string(out)
}

func seedFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const plat = "linux-arm64"

// The whole point of the flip: a run installs onto the VM's own disk, and the
// tree still reaches the operator's host at teardown.
func TestToolSyncRoundTripsThroughTheDurableRoot(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state}

	// A previous run left a server in the store; this run must start with it.
	seedFile(t, filepath.Join(state, "toolchains", plat, ".local", "bin", "gopls"), "old")
	runToolSync(t, bash, env, "restore")
	restored := filepath.Join(agent, "toolchains", plat, ".local", "bin", "gopls")
	if b, err := os.ReadFile(restored); err != nil || string(b) != "old" {
		t.Fatalf("restore did not bring the stored toolchain onto the agent disk: %v", err)
	}

	// This run installs another one, and teardown must carry it out.
	seedFile(t, filepath.Join(agent, "toolchains", plat, ".local", "bin", "pyright"), "new")
	runToolSync(t, bash, env, "save")
	saved := filepath.Join(state, "toolchains", plat, ".local", "bin", "pyright")
	if b, err := os.ReadFile(saved); err != nil || string(b) != "new" {
		t.Fatalf("save did not carry the new toolchain to the durable root: %v", err)
	}
}

// Docker installs straight into the mounted host dir, so a sync there would copy
// a directory onto itself. It must do nothing at all.
func TestToolSyncIsANoOpOnDocker(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent := t.TempDir()

	for _, mode := range []string{"restore", "save"} {
		if out := runToolSync(t, bash, map[string]string{"HOME": agent}, mode); strings.TrimSpace(out) != "" {
			t.Errorf("proveo_sync_tools %s spoke on the docker shape: %q", mode, out)
		}
	}
	if _, err := os.Stat(filepath.Join(agent, "toolchains")); err == nil {
		t.Error("the sync created a toolchain tree on a backend that needs none")
	}
}

// The operator's opt-out has to hold in both directions, or "off" means "off on
// the way in and paid for on the way out".
func TestToolSyncRespectsTheOptOut(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_TOOL_SYNC": "off"}

	seedFile(t, filepath.Join(state, "toolchains", plat, ".local", "bin", "gopls"), "old")
	runToolSync(t, bash, env, "restore")
	if _, err := os.Stat(filepath.Join(agent, "toolchains", plat, ".local", "bin", "gopls")); err == nil {
		t.Error("restore ran with PROVEO_TOOL_SYNC=off")
	}

	seedFile(t, filepath.Join(agent, "toolchains", plat, ".local", "bin", "pyright"), "new")
	runToolSync(t, bash, env, "save")
	if _, err := os.Stat(filepath.Join(state, "toolchains", plat, ".local", "bin", "pyright")); err == nil {
		t.Error("save ran with PROVEO_TOOL_SYNC=off")
	}
}

// An unknown mode must be refused rather than silently treated as one of them:
// teardown calls this by name and a typo would look like a successful copy.
func TestToolSyncRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	script := `source "$1/packages/lib/entrypoint-lib.sh"; proveo_sync_tools sideways; echo "rc=$?"`
	out, _ := exec.Command(bash, "-c", script, "bash", repoRoot(t)).CombinedOutput()
	if !strings.Contains(string(out), "rc=2") {
		t.Errorf("proveo_sync_tools with an unknown mode gave %q, want rc=2", out)
	}
}

func TestSeedRestoresToolchainsBeforeProvisioning(t *testing.T) {
	t.Parallel()
	seed := provisionBody(t, entrypointLib(t))
	restore := strings.Index(seed, "proveo_sync_tools restore")
	provision := strings.Index(seed, "proveo_provision_toolchain")
	if restore < 0 {
		t.Fatal("proveo_seed never restores the toolchain tree — every sbx open reprovisions from scratch")
	}
	if provision < 0 {
		t.Fatal("proveo_provision_toolchain is no longer called from proveo_seed")
	}
	if restore > provision {
		t.Error("the toolchain restore runs AFTER provisioning, so the installers cannot see what it brought back")
	}
}

// A restore of a store this home already holds is a no-op: the stamp beside
// each tree names the save it came from, and a new save changes it.
func TestToolRestoreSkipsAStoreTheHomeAlreadyHolds(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state}
	stored := filepath.Join(state, "toolchains", plat, ".local", "bin", "gopls")
	restored := filepath.Join(agent, "toolchains", plat, ".local", "bin", "gopls")
	storeStamp := filepath.Join(state, "toolchains", "."+plat+".proveo-sync")
	homeStamp := filepath.Join(agent, "toolchains", "."+plat+".proveo-sync")

	seedFile(t, stored, "old")
	runToolSync(t, bash, env, "restore")
	if _, err := os.Stat(homeStamp); err == nil {
		t.Error("a restore from an unstamped store wrote a home stamp — the next restore would skip a store no save vouched for")
	}
	runToolSync(t, bash, env, "save")
	a, errA := os.ReadFile(storeStamp)
	b, errB := os.ReadFile(homeStamp)
	if errA != nil || errB != nil || len(a) == 0 || string(a) != string(b) {
		t.Fatalf("save must stamp both trees with one token: store=%q (%v) home=%q (%v)", a, errA, b, errB)
	}
	if _, err := os.Stat(filepath.Join(state, "toolchains", plat, "."+plat+".proveo-sync")); err == nil {
		t.Error("the stamp landed INSIDE the synced tree, so the next sync would copy it as a tool")
	}

	if err := os.Remove(restored); err != nil {
		t.Fatal(err)
	}
	runToolSync(t, bash, env, "restore")
	if _, err := os.Stat(restored); err == nil {
		t.Error("restore re-copied a store whose stamp the home already holds")
	}

	seedFile(t, storeStamp, "another-sandbox-saved\n")
	runToolSync(t, bash, env, "restore")
	if _, err := os.Stat(restored); err != nil {
		t.Errorf("restore skipped a store another save had changed: %v", err)
	}
	if b, _ := os.ReadFile(homeStamp); strings.TrimSpace(string(b)) != "another-sandbox-saved" {
		t.Errorf("home stamp after a full restore = %q, want the store's token", b)
	}
}

// Teardown must not save a toolchain the background provision step is still
// writing; the store keeps its last complete tree until a later save.
func TestToolSaveSkipsATreeStillBeingProvisioned(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "provisioning")
	env := map[string]string{"HOME": agent, "PROVEO_STATE_HOME": state, "PROVEO_PROVISIONING": marker}
	half := filepath.Join(agent, "toolchains", plat, ".local", "bin", "half-installed")
	seedFile(t, half, "partial")

	seedFile(t, marker, strconv.Itoa(os.Getpid())+"\n")
	out := runToolSync(t, bash, env, "save")
	if _, err := os.Stat(filepath.Join(state, "toolchains", plat, ".local", "bin", "half-installed")); err == nil {
		t.Errorf("save copied a tree a live provision step still holds\n%s", out)
	}
	if !strings.Contains(out, "still provisioning") {
		t.Errorf("save skipped without saying why: %q", out)
	}

	done := exec.Command("true")
	if err := done.Run(); err != nil {
		t.Skipf("true unavailable: %v", err)
	}
	seedFile(t, marker, strconv.Itoa(done.Process.Pid)+"\n")
	runToolSync(t, bash, env, "save")
	if _, err := os.Stat(filepath.Join(state, "toolchains", plat, ".local", "bin", "half-installed")); err != nil {
		t.Errorf("a marker left by a dead provision step still blocked the save: %v", err)
	}
}
