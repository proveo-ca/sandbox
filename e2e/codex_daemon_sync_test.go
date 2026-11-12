//go:build e2e

// SPEC: _spec/defs/codex/codex-topology.puml, _spec/packages/lib/config-seeding-and-persistence.puml
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
)

// codexForDaemon is a codex with an app-server daemon (≥0.157): PROVEO_TEST_CODEX, else codex on PATH.
func codexForDaemon(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PROVEO_TEST_CODEX")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("codex"); err != nil {
			t.Skip("codex not on PATH; set PROVEO_TEST_CODEX to a codex ≥0.157 binary")
		}
	}
	if out, err := exec.Command(bin, "app-server", "daemon", "--help").CombinedOutput(); err != nil {
		t.Skipf("%s has no app-server daemon (%v): %s", bin, err, out)
	}
	return bin
}

func codexHomeMount(t *testing.T) manifest.Home {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "codex" {
			return m.Home
		}
	}
	t.Fatal("no codex manifest")
	return manifest.Home{}
}

// sandboxHome is one sandbox's agent home: the image's empty ~/.codex, then the seed's config restore.
func sandboxHome(t *testing.T, state, set string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configSync(t, home, state, set, "restore")
	return home
}

func configSync(t *testing.T, home, state, set, mode string) {
	t.Helper()
	c := exec.Command("bash", "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_sync_config "$2"`, "bash", repoRoot(t), mode)
	c.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "PROVEO_STATE_HOME=" + state, "PROVEO_CONFIG_DIRS=" + set}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("proveo_sync_config %s: %v\n%s", mode, err, out)
	}
}

// codexIn runs codex against one sandbox home and stops its daemon when the test ends.
func codexIn(t *testing.T, bin, home string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := []string{"PATH=" + filepath.Dir(bin) + ":/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex")}
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = env
	out, err := c.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(bin, "app-server", "daemon", "stop")
		stop.Env = env
		_ = stop.Run()
		_ = exec.Command("pkill", "-f", filepath.Join(home, ".codex")).Run()
	})
	return string(out), err
}

// The report: a second sandbox restored a daemon install whose `current` link the sync had dropped.
func roundTrip(t *testing.T, bin, set string) (string, string, error) {
	t.Helper()
	state := t.TempDir()
	first := sandboxHome(t, state, set)
	if out, err := codexIn(t, bin, first, "app-server", "daemon", "start"); err != nil || !strings.Contains(out, `"status":"started"`) {
		t.Fatalf("the first sandbox could not start codex's daemon: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(first, ".codex", "config.toml"), []byte("hide_agent_reasoning = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = codexIn(t, bin, first, "app-server", "daemon", "stop")
	configSync(t, first, state, set, "save")
	second := sandboxHome(t, state, set)
	out, err := codexIn(t, bin, second, "app-server", "daemon", "start")
	return out, second, err
}

func TestCodexDaemonStartsInEverySandboxAfterAHomeRoundTrip(t *testing.T) {
	bin := codexForDaemon(t)
	set := proveohome.ConfigSet(codexHomeMount(t))
	out, second, err := roundTrip(t, bin, set)
	if err != nil || !strings.Contains(out, `"status":"started"`) {
		t.Fatalf("the second sandbox's daemon did not start: %v\n%s", err, out)
	}
	if strings.Contains(out, "daemon executable not found") {
		t.Fatalf("the reported failure is back:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(second, ".codex", "config.toml")); err != nil {
		t.Errorf("the round trip dropped config.toml: %v", err)
	}
}

// Without the transient paths the same round trip reproduces the operator's error; this pins what the test guards.
func TestCodexDaemonRoundTripWithoutTransientReproducesTheReport(t *testing.T) {
	bin := codexForDaemon(t)
	home := codexHomeMount(t)
	for i := range home.Mounts {
		home.Mounts[i].Transient = nil
	}
	out, _, _ := roundTrip(t, bin, proveohome.ConfigSet(home))
	if !strings.Contains(out, "daemon executable not found") {
		t.Skipf("this codex repairs a synced install itself; the transient paths stay as the sbx-native shape:\n%s", out)
	}
}
