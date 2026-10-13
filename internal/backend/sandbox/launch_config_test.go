// SPEC: _spec/defs/claudecode/chrome-bridge.puml
package sandbox

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

type opencodeLaunchConfig struct {
	Schema string `json:"$schema"`
	Model  string `json:"model"`
	Agents map[string]struct {
		Model string `json:"model"`
	} `json:"agents"`
	Providers map[string]struct {
		Package  string `json:"package"`
		Name     string `json:"name"`
		Settings struct {
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
		} `json:"settings"`
		Models map[string]struct {
			Name string `json:"name"`
		} `json:"models"`
	} `json:"providers"`
	MCP *struct {
		Servers map[string]struct {
			Type     string   `json:"type"`
			Command  []string `json:"command"`
			Disabled bool     `json:"disabled"`
		} `json:"servers"`
	} `json:"mcp"`
}

func opencodeConfigFromEnv(t *testing.T, env []string) opencodeLaunchConfig {
	t.Helper()
	var count int
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG_CONTENT=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d OPENCODE_CONFIG_CONTENT entries, want one: %v", count, env)
	}
	content := envValue(env, "OPENCODE_CONFIG_CONTENT")
	var cfg opencodeLaunchConfig
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		t.Fatalf("config is not native OpenCode v2 JSON: %v\n%s", err, content)
	}
	if cfg.Schema != "https://opencode.ai/config.json" {
		t.Errorf("schema = %q", cfg.Schema)
	}
	return cfg
}

func assertOpencodeLocalModelConfig(t *testing.T, env []string, cfg opencodeLaunchConfig, model, baseURL string) {
	t.Helper()
	want := "ollama/" + model
	if cfg.Model != want || cfg.Agents["title"].Model != want {
		t.Errorf("model = %q title model = %q, want %q for both", cfg.Model, cfg.Agents["title"].Model, want)
	}
	if len(cfg.Agents) != 1 || len(cfg.Providers) != 1 {
		t.Errorf("want only the title agent override and Ollama provider: %+v", cfg)
	}
	provider := cfg.Providers["ollama"]
	if provider.Package != "@opencode/ai/providers/openai-compatible" || provider.Name != "Ollama (local)" {
		t.Errorf("Ollama provider = %+v", provider)
	}
	if provider.Settings.BaseURL != baseURL || provider.Settings.APIKey != "ollama" {
		t.Errorf("settings = %+v, want baseURL %q and apiKey ollama", provider.Settings, baseURL)
	}
	if len(provider.Models) != 1 || provider.Models[model].Name != model+" (local)" {
		t.Errorf("model list = %+v, want %s (local)", provider.Models, model)
	}
	for _, key := range []string{"OPENCODE_MODEL", "OPENCODE_SMALL_MODEL"} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestOpencodeLocalModelIsWiredInTheLaunchEnv(t *testing.T) {
	env := launchConfigEnv("opencode", []string{
		"PROVEO_LOCAL_MODEL=gemma4:26b",
		"OLLAMA_API_BASE=http://host.docker.internal:11434",
	})
	cfg := opencodeConfigFromEnv(t, env)
	assertOpencodeLocalModelConfig(t, env, cfg, "gemma4:26b", "http://host.docker.internal:11434/v1")
	if cfg.MCP != nil {
		t.Errorf("no mobile add-on must mean no MCP override: %+v", cfg.MCP)
	}
}

func TestOpencodeLocalEndpointNormalizationIsIdempotent(t *testing.T) {
	for _, tt := range []struct {
		base string
		want string
	}{
		{"", "http://ollama:11434/v1"},
		{"http://host.docker.internal:11434", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434/", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434///", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434/v1", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434/v1/", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434/v1///", "http://host.docker.internal:11434/v1"},
		{"http://host.docker.internal:11434/v1/v1/", "http://host.docker.internal:11434/v1"},
		{"https://proxy.example/ollama/v1/", "https://proxy.example/ollama/v1"},
	} {
		t.Run(tt.base, func(t *testing.T) {
			env := launchConfigEnv("opencode", []string{"PROVEO_LOCAL_MODEL=x", "OLLAMA_API_BASE=" + tt.base})
			cfg := opencodeConfigFromEnv(t, env)
			assertOpencodeLocalModelConfig(t, env, cfg, "x", tt.want)
			normalized := cfg.Providers["ollama"].Settings.BaseURL
			again := launchConfigEnv("opencode", []string{"PROVEO_LOCAL_MODEL=x", "OLLAMA_API_BASE=" + normalized})
			if !slices.Equal(env, again) {
				t.Errorf("normalizing %q twice changed the launch env:\n%v\n%v", tt.base, env, again)
			}
		})
	}
}

func TestOpencodeLaunchConfigRequiresALocalModelOrMobileAddon(t *testing.T) {
	for _, tt := range []struct {
		name  string
		agent string
		env   []string
	}{
		{"plain", "opencode", nil},
		{"endpoint only", "opencode", []string{"OLLAMA_API_BASE=http://ollama:11434/v1"}},
		{"empty opt-ins", "opencode", []string{"PROVEO_LOCAL_MODEL=", "PROVEO_HOST_ADB_PORT="}},
		{"existing config", "opencode", []string{`OPENCODE_CONFIG_CONTENT={"model":"openai/gpt-5"}`, "OPENCODE_MODEL=openai/gpt-5", "OPENCODE_SMALL_MODEL=openai/gpt-5-mini"}},
		{"another agent", "claude", []string{"PROVEO_LOCAL_MODEL=x", "PROVEO_HOST_ADB_PORT=5037"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchConfigEnv(tt.agent, tt.env); got != nil {
				t.Errorf("no OpenCode launch override requested, got %v", got)
			}
		})
	}
}

func TestChromeBridgeLaunchesClaudeWithTheChromeFlag(t *testing.T) {
	bridge := []string{"PROVEO_CHROME_BRIDGE=host.docker.internal:1", "PROVEO_CHROME_BRIDGE_TOKEN=t"}
	claude := sbx.BuiltinAgent("claudecode")
	if got := withChromeFlag(claude, bridge, []string{"-p", "hi"}); strings.Join(got, " ") != "--chrome -p hi" {
		t.Errorf("claude with a bridge = %v, want --chrome ahead of the caller's args", got)
	}
	if got := withChromeFlag(claude, bridge, nil); strings.Join(got, " ") != "--chrome" {
		t.Errorf("interactive claude with a bridge = %v, want [--chrome]", got)
	}
	if got := withChromeFlag(claude, nil, []string{"-p", "hi"}); strings.Join(got, " ") != "-p hi" {
		t.Errorf("no bridge must leave the args alone, got %v", got)
	}
	if got := withChromeFlag(sbx.ShellAgent, bridge, []string{"-c", "x"}); strings.Join(got, " ") != "-c x" {
		t.Errorf("a non-claude agent got --chrome: %v", got)
	}
	if got := withChromeFlag(claude, bridge, []string{"--chrome", "-p", "hi"}); strings.Join(got, " ") != "--chrome -p hi" {
		t.Errorf("an explicit --chrome must not be doubled, got %v", got)
	}
}
