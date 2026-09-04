// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// opencode's 24 built-in formatters are DISABLED BY DEFAULT — omitting the key
// is the off state — so a workspace carrying prettier and oxfmt formatted
// nothing until proveo turned the registry on. This is the switch.
func TestOpencodeFormatterRegistryIsEnabled(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}

	wire := func(t *testing.T, existing string, env ...string) string {
		t.Helper()
		home := t.TempDir()
		cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
		if existing != "" {
			if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(existing), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
_proveo_agent_home() { printf '%s' "$agent_home"; }
configure_opencode_formatter`
		cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), home)
		cmd.Env = append(os.Environ(), "PROVEO_OPENCODE_FORMATTER=", "HOME="+home)
		cmd.Env = append(cmd.Env, env...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("configure_opencode_formatter failed: %v\n%s", err, b)
		}
		b, _ := os.ReadFile(cfg)
		return string(b)
	}

	t.Run("enabled on a fresh config", func(t *testing.T) {
		t.Parallel()
		var got map[string]any
		if err := json.Unmarshal([]byte(wire(t, "")), &got); err != nil {
			t.Fatalf("not valid JSON: %v", err)
		}
		if got["formatter"] != true {
			t.Errorf("formatter = %v, want true — omitting the key is opencode's OFF state", got["formatter"])
		}
	})

	t.Run("unrelated config survives", func(t *testing.T) {
		t.Parallel()
		out := wire(t, `{"model":"kept","lsp":{"typescript":{"command":["x"]}}}`)
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("not valid JSON: %v", err)
		}
		if got["model"] != "kept" || got["lsp"] == nil {
			t.Errorf("clobbered unrelated config: %s", out)
		}
		if got["formatter"] != true {
			t.Errorf("did not enable formatters: %s", out)
		}
	})

	// Setdefault: an operator who answered — including "off" — keeps their answer.
	t.Run("an explicit answer wins", func(t *testing.T) {
		t.Parallel()
		for _, existing := range []string{
			`{"formatter":false}`,
			`{"formatter":{"prettier":{"disabled":true}}}`,
		} {
			out := wire(t, existing)
			if strings.TrimSpace(out) != existing {
				t.Errorf("overwrote an explicit formatter answer:\n  had  %s\n  got  %s", existing, out)
			}
		}
	})

	t.Run("opt-out", func(t *testing.T) {
		t.Parallel()
		if out := wire(t, "", "PROVEO_OPENCODE_FORMATTER=off"); out != "" {
			t.Errorf("wrote config with PROVEO_OPENCODE_FORMATTER=off: %s", out)
		}
	})
}
