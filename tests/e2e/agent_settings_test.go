//go:build e2e

// SPEC: _spec/_plans/harness-choice-cache.puml, _spec/tests/testing-strategy.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/tmux"
)

type agentSettingsDoc struct {
	Targets map[string]struct {
		Egress      string   `yaml:"egress"`
		Credentials string   `yaml:"credentials"`
		Addons      []string `yaml:"addons"`
		Fingerprint string   `yaml:"fingerprint"`
	} `yaml:"targets"`
}

func readAgentSettings(t *testing.T, home string) agentSettingsDoc {
	t.Helper()
	path := filepath.Join(home, ".proveo", "agent-settings.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc agentSettingsDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return doc
}

func TestAgentSettingsPersistAcrossRuns(t *testing.T) {
	if os.Getenv("PROVEO_LLM_TEST") != "1" {
		t.Skip("set PROVEO_LLM_TEST=1 to run the agent-settings E2E")
	}
	if !tmux.Available() {
		t.Skip("tmux not available")
	}

	const target = "opencode"
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".proveo"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildProveo(t)

	run := func(label string, extra ...string) string {
		sess := tmux.New(fmt.Sprintf("proveo-settings-%s-%d", label, os.Getpid()), nil)
		t.Cleanup(sess.Kill)

		cmd := []string{"env", "HOME=" + home, "PROVEO_AUTO_INSTALL_TOOLS=false"}
		cmd = append(cmd, childEnvArgs(t)...)
		cmd = append(cmd, bin, "run", target, "--shell")
		cmd = append(cmd, extra...)
		if err := sess.Start(200, 50, cmd...); err != nil {
			t.Fatalf("[%s] tmux start: %v", label, err)
		}
		time.Sleep(10 * time.Second)
		_ = sess.SendText("exit")
		_ = sess.Enter()
		out, _ := waitSessionExit(sess, 90*time.Second)
		return out
	}

	out1 := run("first")
	doc := readAgentSettings(t, home)
	got, ok := doc.Targets[target]
	if !ok {
		t.Fatalf("first run did not persist %q into agent-settings.yml\n--- session ---\n%s", target, out1)
	}
	if got.Egress != "allowlist" {
		t.Errorf("persisted egress = %q, want the default %q", got.Egress, "allowlist")
	}
	if got.Credentials != "broker" {
		t.Errorf("persisted credentials = %q, want the default %q", got.Credentials, "broker")
	}
	if got.Fingerprint == "" {
		t.Error("persisted choice carries no capability fingerprint — a manifest change could not invalidate it")
	}

	path := filepath.Join(home, ".proveo", "agent-settings.yml")
	edited := strings.Replace(string(mustRead(t, path)), "egress: allowlist", "egress: open", 1)
	if !strings.Contains(edited, "egress: open") {
		t.Fatalf("could not rewrite the cached tier in %s:\n%s", path, edited)
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	out2 := run("second")
	if mode := sessionEgressMode(out2); mode != "open" {
		t.Errorf("second run used egress mode %q, want the cached %q — the cache was not re-entered\n--- session ---\n%s",
			mode, "open", out2)
	}

	after := readAgentSettings(t, home)
	if after.Targets[target].Egress != "open" {
		t.Errorf("second run overwrote the cached tier with %q, want it left at %q",
			after.Targets[target].Egress, "open")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sessionEgressMode(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "PROVEO_EGRESS_MODE="); i >= 0 {
			rest := line[i+len("PROVEO_EGRESS_MODE="):]
			return strings.TrimSpace(strings.Fields(rest + " ")[0])
		}
	}
	return ""
}
