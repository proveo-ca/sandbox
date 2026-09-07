//go:build e2e

// SPEC: _spec/internal/agentsettings/choice-cache.puml, _spec/tests/testing-strategy.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
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
	const target = "opencode"
	requireHarness(t, target)
	// SPEC: _spec/tests/40-agent-e2e-components.puml
	requireHarnessCredential(t, target)

	home, work := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".proveo"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildProveo(t)

	run := func(label string, extra ...string) string {
		sess := tmux.New(fmt.Sprintf("proveo-settings-%s-%d", label, os.Getpid()), nil)
		t.Cleanup(sess.Kill)

		cmd := []string{"env"}
		cmd = append(cmd, childEnvArgs(t)...)
		cmd = append(cmd,
			"PROVEO_WIZARD=on",
			"HOME="+home, "PROVEO_AUTO_INSTALL_TOOLS=false", "DOCKER_HOST="+dockerHost(t))
		cmd = append(cmd, bin, "run", target, "--input", work, "--shell")
		cmd = append(cmd, extra...)
		if err := sess.Start(200, 50, cmd...); err != nil {
			t.Fatalf("[%s] tmux start: %v", label, err)
		}
		acceptChoicePrompt(t, sess, target)
		time.Sleep(4 * time.Second)
		// Read the tier out of the agent's OWN environment: that is the honest signal
		// that the choice took effect, rather than anything proveo printed host-side.
		_ = sess.SendText("echo TIER=$PROVEO_EGRESS_MODE")
		_ = sess.Enter()
		time.Sleep(2 * time.Second)
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

// dockerHost resolves the active docker endpoint so an overridden HOME cannot
// strand the child on the wrong socket.
func dockerHost(t *testing.T) string {
	t.Helper()
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	out, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return "unix:///var/run/docker.sock"
	}
	return strings.TrimSpace(string(out))
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
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TIER=") || strings.Contains(line, "$") {
			continue
		}
		if v := strings.TrimPrefix(line, "TIER="); v != "" {
			return v
		}
	}
	return ""
}
