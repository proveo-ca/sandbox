//go:build e2e && !windows

// SPEC: _spec/_plans/claudecode-mcp-toggle.puml
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/imagetest"
	"github.com/proveo-ca/proveo/internal/schedule"
)

func TestClaudeMCPToggleLoadsTheCurrentConfig(t *testing.T) {
	requireTmux(t)
	requireDocker(t)
	image := imagetest.Resolve("claudecode", "proveo/claudecode:latest")
	if !dockerImagePresent(t, image) {
		t.Skipf("Claude Code image %s is unavailable", image)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	plugin := filepath.Join(root, "plugin", ".claude-plugin")
	for _, dir := range []string{work, filepath.Join(home, ".claude"), plugin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "probe.cjs"), []byte(claudeMCPProbe))
	writeFile(t, filepath.Join(work, ".mcp.json"), []byte(`{"mcpServers":{"project-fixture":{"command":"node","args":["/probe/probe.cjs"]}}}`))
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), []byte(`{"enableAllProjectMcpServers":true}`))
	writeFile(t, filepath.Join(plugin, "plugin.json"), []byte(`{
		"name":"mcp-toggle-probe","version":"1.0.0",
		"mcpServers":{"plugin-fixture":{"command":"node","args":["/probe/probe.cjs","plugin"]}}
	}`))
	const key = "sk-ant-api03-proveo-local-fixture-key"
	configPath := filepath.Join(home, ".claude.json")
	assertSaved := func(t *testing.T, name string) {
		t.Helper()
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		if len(config.Servers) != 1 || len(config.Servers[name]) == 0 {
			t.Errorf("saved server %q did not survive the launch", name)
		}
	}
	saved := func(name string) {
		t.Helper()
		writeFile(t, configPath, []byte(fmt.Sprintf(`{
			"theme":"dark","hasCompletedOnboarding":true,"numStartups":10,
			"customApiKeyResponses":{"approved":[%q],"rejected":[]},
			"projects":{"/probe/work":{"hasTrustDialogAccepted":true,"hasCompletedProjectOnboarding":true}},
			"mcpServers":{%q:{"command":"node","args":["/probe/probe.cjs"]}}
		}`, key[len(key)-20:], name)))
	}
	saved("mcp-gateway")
	for _, tc := range []struct {
		name, mode, want string
		missing          string
	}{
		{"enabled", "on", "mcp-gateway", ""},
		{"disabled", "off", "No MCP servers configured", "mcp-gateway"},
		{"enabled-after-edit", "on", "edited-fixture", "mcp-gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "enabled-after-edit" {
				saved("edited-fixture")
			}
			cfg, _, _ := sandbox.Spec(sandbox.Input{
				Target: "claudecode", Image: image, MCPGateway: tc.mode == "on",
				Extra: []string{"--plugin-dir", "/probe/plugin"}, Lookup: func(string) string { return "" },
			})
			flags := cfg.Command
			name := fmt.Sprintf("proveo-mcp-toggle-%d-%s", os.Getpid(), tc.name)
			tm := schedule.NewTmux(name, nil)
			t.Cleanup(func() {
				tm.Kill()
				_ = exec.Command("docker", "rm", "-f", name).Run()
			})
			docker := []string{"docker", "run", "--rm", "-it", "--name", name, "--network", "none",
				"--mount", "type=bind,source=" + root + ",target=/probe", "-w", "/probe/work", "--entrypoint", "claude",
				"-e", "HOME=/probe/home", "-e", "ANTHROPIC_API_KEY=" + key, "-e", "ANTHROPIC_BASE_URL=http://127.0.0.1:9",
				"-e", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "-e", "CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1", image}
			if err := tm.Start(130, 40, append(docker, flags...)...); err != nil {
				t.Fatal(err)
			}
			if screen, err := tm.WaitFor("for agents", 25*time.Second); err != nil {
				t.Fatalf("Claude Code did not reach the prompt: %v\n%s", err, screen)
			}
			if err := tm.SendText("/mcp"); err != nil {
				t.Fatal(err)
			}
			if err := tm.Enter(); err != nil {
				t.Fatal(err)
			}
			screen, err := tm.WaitFor(tc.want, 15*time.Second)
			if err != nil {
				t.Fatalf("unexpected MCP state for %s: %v\n%s", flags, err, screen)
			}
			if tc.missing != "" && strings.Contains(screen, tc.missing) {
				t.Errorf("unexpected server %q\n%s", tc.missing, screen)
			}
			if tc.mode == "on" {
				for _, server := range []string{tc.want, "project-fixture", "plugin-fixture"} {
					if !strings.Contains(lineWith(screen, server), "connected") {
						t.Errorf("%s did not connect\n%s", server, screen)
					}
				}
			}
			savedName := "mcp-gateway"
			if tc.name == "enabled-after-edit" {
				savedName = tc.want
			}
			assertSaved(t, savedName)
		})
	}
	t.Run("enterprise-policy", func(t *testing.T) {
		managed := filepath.Join(root, "managed-mcp.json")
		writeFile(t, managed, []byte(`{"mcpServers":{"managed-fixture":{"command":"node","args":["/probe/probe.cjs"]}}}`))
		cfg, _, _ := sandbox.Spec(sandbox.Input{Target: "claudecode", Image: image, Lookup: func(string) string { return "" }})
		docker := []string{"run", "--rm", "--network", "none",
			"--mount", "type=bind,source=" + root + ",target=/probe",
			"--mount", "type=bind,source=" + managed + ",target=/etc/claude-code/managed-mcp.json,readonly",
			"-e", "HOME=/probe/home", "--entrypoint", "claude", image}
		out, err := exec.Command("docker", append(docker, cfg.Command...)...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "enterprise MCP config") {
			t.Fatalf("Claude Code did not refuse strict mode under an enterprise MCP policy: %v\n%s", err, out)
		}
	})
}

const claudeMCPProbe = `
const lines = require('node:readline').createInterface({ input: process.stdin });
lines.on('line', line => {
  const request = JSON.parse(line);
  if (request.id === undefined) return;
  const result = request.method === 'initialize'
    ? { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'probe', version: '1' } }
    : { tools: [] };
  console.log(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }));
});
`
