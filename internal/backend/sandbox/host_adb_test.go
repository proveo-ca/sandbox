// SPEC: _spec/internal/sbx/host-android-adb.puml
package sandbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHostADBAddsTheServerPortToTheKit(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("claudecode")
	in.AgentEnv = []string{hostadb.EnvPort + "=5037"}
	_, kit, _ := Spec(in)
	for _, want := range []string{"localhost:5037", "registry.npmjs.org"} {
		if !has(kit.Permissions.Network.Allow, want) {
			t.Errorf("network.allow = %v, missing %s", kit.Permissions.Network.Allow, want)
		}
	}
	in.AgentEnv = nil
	_, kit, _ = Spec(in)
	if has(kit.Permissions.Network.Allow, "localhost:5037") || has(kit.Permissions.Network.Allow, "registry.npmjs.org") {
		t.Errorf("no android add-on ⇒ neither the adb port nor the registry: %v", kit.Permissions.Network.Allow)
	}
}

func TestWithMobileMCPHandsClaudeTheServer(t *testing.T) {
	on := hostadb.GuestEnv(5037)
	got := withMobileMCP("claudecode", on, []string{"-p", "hi"})
	if len(got) != 4 || got[0] != "--mcp-config" || got[1] != hostadb.MCPConfig || got[2] != "-p" {
		t.Errorf("android on ⇒ %q; want --mcp-config <json> before the caller's args", got)
	}
	if got := withMobileMCP("claudecode", nil, []string{"-p", "hi"}); strings.Join(got, " ") != "-p hi" {
		t.Errorf("android off ⇒ %q; want the command untouched", got)
	}
	if got := withMobileMCP("opencode", on, []string{"run"}); strings.Join(got, " ") != "run" {
		t.Errorf("opencode takes the server through its config, not flags ⇒ %q", got)
	}
	twice := withMobileMCP("claudecode", on, withMobileMCP("claudecode", on, nil))
	if n := strings.Count(strings.Join(twice, " "), "--mcp-config"); n != 1 {
		t.Errorf("applied twice ⇒ %d --mcp-config flags", n)
	}
}

func TestOpencodeConfigCarriesTheMobileServer(t *testing.T) {
	env := launchConfigEnv("opencode", hostadb.GuestEnv(5037))
	raw := strings.TrimPrefix(envValue(env, "OPENCODE_CONFIG_CONTENT"), "")
	var cfg struct {
		MCP map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v\n%s", err, raw)
	}
	m := cfg.MCP["mobile"]
	if m.Type != "local" || !m.Enabled || len(m.Command) != 3 || !strings.Contains(m.Command[2], "proveo_mobile_mcp_exec") {
		t.Errorf("mobile server = %+v", m)
	}
	if cfg.Model != "" {
		t.Errorf("no local model ⇒ no model key, got %q", cfg.Model)
	}

	both := launchConfigEnv("opencode", append(hostadb.GuestEnv(5037), "PROVEO_LOCAL_MODEL=muse-glimmer:30b-mlx"))
	joined := envValue(both, "OPENCODE_CONFIG_CONTENT")
	if !strings.Contains(joined, `"mobile"`) || !strings.Contains(joined, `"ollama/muse-glimmer:30b-mlx"`) {
		t.Errorf("local model + android ⇒ one config carrying both:\n%s", joined)
	}
	if n := strings.Count(strings.Join(both, "\n"), "OPENCODE_CONFIG_CONTENT="); n != 1 {
		t.Errorf("%d OPENCODE_CONFIG_CONTENT entries; a second would clobber the first", n)
	}
	if launchConfigEnv("opencode", nil) != nil {
		t.Error("neither a local model nor android ⇒ no opencode config")
	}
}

func TestHostADBEnvReachesTheKit(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("claudecode")
	in.AgentEnv = hostadb.GuestEnv(5037)
	cfg, _, _ := Spec(in)
	env := strings.Join(cfg.Env, "\n")
	if !strings.Contains(env, "ADB_SERVER_SOCKET=tcp:host.docker.internal:5037") {
		t.Errorf("run env lacks ADB_SERVER_SOCKET:\n%s", env)
	}
}

func TestLaunchEnvNamesTheHostOS(t *testing.T) {
	orig := hostOS
	t.Cleanup(func() { hostOS = orig })
	for _, goos := range []string{"darwin", "linux"} {
		hostOS = goos
		got := strings.Join(launchEnv(specInput("claudecode"), "claude", nil, nil), "\n")
		if !strings.Contains(got, EnvHostOS+"="+goos) {
			t.Errorf("host %s ⇒ launch env lacks %s=%s:\n%s", goos, EnvHostOS, goos, got)
		}
	}
}

func TestLocalModelClaudeKeepsALeanToolSet(t *testing.T) {
	claude := sbx.BuiltinAgent("claudecode")
	local := []string{"PROVEO_LOCAL_MODEL=muse-glimmer:30b-mlx"}
	got := withLocalModelTools(claude, local, []string{"-p", "hi"})
	if strings.Join(got, " ") != "--tools "+LocalModelTools+" -p hi" {
		t.Errorf("local model ⇒ %q", got)
	}
	if got := withLocalModelTools(claude, nil, []string{"-p", "hi"}); strings.Join(got, " ") != "-p hi" {
		t.Errorf("hosted model ⇒ %q; want the command untouched", got)
	}
	if got := withLocalModelTools("opencode", local, []string{"run"}); strings.Join(got, " ") != "run" {
		t.Errorf("opencode ⇒ %q; the flag is claude's alone", got)
	}
	if got := withLocalModelTools(claude, local, []string{"--tools", "Bash"}); strings.Join(got, " ") != "--tools Bash" {
		t.Errorf("an operator's own --tools must win ⇒ %q", got)
	}
}
