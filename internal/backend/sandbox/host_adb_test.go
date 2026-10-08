// SPEC: _spec/internal/sbx/host-android-adb.puml
package sandbox

import (
	"encoding/json"
	"slices"
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
	for _, model := range []string{"", "muse-glimmer:30b-mlx"} {
		t.Run("local model="+model, func(t *testing.T) {
			input := append(hostadb.GuestEnv(5037), "PROVEO_LOCAL_MODEL="+model, "OLLAMA_API_BASE=http://host.docker.internal:11434/v1/")
			env := launchConfigEnv("opencode", input)
			cfg := opencodeConfigFromEnv(t, env)
			if cfg.MCP == nil || len(cfg.MCP.Servers) != 1 {
				t.Fatalf("want one MCP server, got %+v", cfg.MCP)
			}
			m, ok := cfg.MCP.Servers["mobile"]
			command := []string{"bash", "-c", "source /entrypoint-lib.sh && proveo_mobile_mcp_exec"}
			if !ok || m.Type != "local" || m.Disabled || !slices.Equal(m.Command, command) {
				t.Errorf("mobile server = %+v", m)
			}
			if model == "" {
				if cfg.Model != "" || cfg.Agents != nil || cfg.Providers != nil || len(env) != 1 {
					t.Errorf("mobile only must not choose a model or emit model metadata: %v", env)
				}
				var fields map[string]any
				if err := json.Unmarshal([]byte(envValue(env, "OPENCODE_CONFIG_CONTENT")), &fields); err != nil {
					t.Fatal(err)
				}
				if _, ok := fields["model"]; ok {
					t.Error("mobile only must omit the model key")
				}
			} else {
				assertOpencodeLocalModelConfig(t, env, cfg, model, "http://host.docker.internal:11434/v1")
			}
			input = append(input, `OPENCODE_CONFIG_CONTENT={"model":"openai/gpt-5","mcp":{"servers":{"mobile":{"type":"remote","url":"https://example.com/mcp","disabled":true}}}}`)
			if override := launchConfigEnv("opencode", input); !slices.Equal(override, env) {
				t.Errorf("launch configuration must override existing inline content:\n%v\n%v", env, override)
			}
		})
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
