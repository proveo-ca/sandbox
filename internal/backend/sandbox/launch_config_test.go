// SPEC: _spec/defs/claudecode/chrome-bridge.puml
package sandbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestOpencodeLocalModelIsWiredInTheLaunchEnv(t *testing.T) {
	env := launchConfigEnv("opencode", []string{
		"PROVEO_LOCAL_MODEL=gemma4:26b",
		"OLLAMA_API_BASE=http://host.docker.internal:11434",
	})
	content := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if content == "" {
		t.Fatalf("no OPENCODE_CONFIG_CONTENT in %v", env)
	}
	var cfg struct {
		Model    string `json:"model"`
		Small    string `json:"small_model"`
		Provider struct {
			Ollama struct {
				Options struct {
					BaseURL string `json:"baseURL"`
				} `json:"options"`
				Models map[string]any `json:"models"`
			} `json:"ollama"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("config is not JSON: %v\n%s", err, content)
	}
	if cfg.Model != "ollama/gemma4:26b" || cfg.Small != "ollama/gemma4:26b" {
		t.Errorf("model = %q small = %q, want ollama/gemma4:26b for both", cfg.Model, cfg.Small)
	}
	if cfg.Provider.Ollama.Options.BaseURL != "http://host.docker.internal:11434/v1" {
		t.Errorf("baseURL = %q, want the host Ollama's /v1", cfg.Provider.Ollama.Options.BaseURL)
	}
	if _, ok := cfg.Provider.Ollama.Models["gemma4:26b"]; !ok {
		t.Errorf("model list lacks gemma4:26b: %v", cfg.Provider.Ollama.Models)
	}
	if envValue(env, "OPENCODE_MODEL") != "ollama/gemma4:26b" {
		t.Errorf("OPENCODE_MODEL not set alongside the config")
	}
	if got := launchConfigEnv("claude", []string{"PROVEO_LOCAL_MODEL=x"}); got != nil {
		t.Errorf("a non-opencode agent got opencode config: %v", got)
	}
	if got := launchConfigEnv("opencode", nil); got != nil || strings.Join(got, "") != "" {
		t.Errorf("no local model must mean no config override, got %v", got)
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
