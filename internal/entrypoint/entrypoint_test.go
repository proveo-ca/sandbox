package entrypoint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestShouldSkipEnvLoad(t *testing.T) {
	t.Parallel()
	for _, tier := range CanonicalTiers {
		if !ShouldSkipEnvLoad(tier) {
			t.Errorf("%s must skip the .env load", tier)
		}
	}
	if !ShouldSkipEnvLoad("  ALLOWLIST  ") {
		t.Error("the tier is matched case- and space-insensitively")
	}
	for _, legacy := range []string{"broker", "firewall", "proxy"} {
		if ShouldSkipEnvLoad(legacy) {
			t.Errorf("%q must not be understood at the container boundary", legacy)
		}
	}
	if ShouldSkipEnvLoad("") {
		t.Error("an unset mode must not skip")
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("FOO=bar\n# c\nexport BAZ=qux\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("FOO")
	_ = os.Unsetenv("BAZ")
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "bar" || os.Getenv("BAZ") != "qux" {
		t.Fatalf("got FOO=%q BAZ=%q", os.Getenv("FOO"), os.Getenv("BAZ"))
	}
	t.Setenv("FOO", "keep")
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "keep" {
		t.Fatal("existing env must win")
	}
}

func TestApplyBrokerSentinel(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "sk-real")
	t.Setenv("OPENAI_API_KEY", "sk-oai")
	got := ApplyBrokerSentinel("allowlist", "CURSOR_API_KEY,OPENAI_API_KEY", "")
	if diff := cmp.Diff([]string{"CURSOR_API_KEY", "OPENAI_API_KEY"}, got); diff != "" {
		t.Fatal(diff)
	}
	if os.Getenv("CURSOR_API_KEY") != DefaultSentinel {
		t.Fatalf("cursor key = %q", os.Getenv("CURSOR_API_KEY"))
	}
	t.Setenv("CURSOR_API_KEY", "sk-real")
	if got := ApplyBrokerSentinel("open", "CURSOR_API_KEY", ""); len(got) != 1 {
		t.Fatalf("open must rewrite a brokered key, got %v", got)
	}
	t.Setenv("CURSOR_API_KEY", "sk-real")
	if ApplyBrokerSentinel("allowlist", "", "") != nil {
		t.Fatal("no brokered keys must not rewrite")
	}
	if os.Getenv("CURSOR_API_KEY") != "sk-real" {
		t.Fatalf("forwarded key was rewritten: %q", os.Getenv("CURSOR_API_KEY"))
	}
	for _, mode := range []string{"firewall", "proxy", "broker", ""} {
		t.Setenv("CURSOR_API_KEY", "sk-real")
		if ApplyBrokerSentinel(mode, "CURSOR_API_KEY", "") != nil {
			t.Fatalf("%q must not be understood at the container boundary", mode)
		}
	}
}

func TestNormalizeModel(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"anthropic/claude-x", "anthropic/claude-x"},
		{"claude-sonnet-4-5", "anthropic/claude-sonnet-4-5"},
		{"gpt-4o", "openai/gpt-4o"},
		{"o3-mini", "openai/o3-mini"},
		{"gemini-2.0", "google/gemini-2.0"},
	}
	for _, tc := range tests {
		if got := NormalizeModel(tc.in); got != tc.want {
			t.Errorf("NormalizeModel(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// ApplyEnvBridges now carries provider key aliases only. Model bridges moved to
// defs/bridges/<harness>.tsv, applied by apply_model_bridges in the def's own shell
// and asserted end-to-end by internal/contract.TestShellApplierMatchesGoReader.
func TestApplyEnvBridges(t *testing.T) {
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "ARCHITECT_MODEL", "OPENCODE_MODEL"} {
		_ = os.Unsetenv(k)
	}
	t.Setenv("GEMINI_API_KEY", "k-123")
	ApplyEnvBridges()
	if got := os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY"); got != "k-123" {
		t.Fatalf("GOOGLE_GENERATIVE_AI_API_KEY=%q", got)
	}

	// A model role must no longer be bridged here, or the table has been forked again.
	t.Setenv("ARCHITECT_MODEL", "claude-sonnet-4-5")
	ApplyEnvBridges()
	if got := os.Getenv("OPENCODE_MODEL"); got != "" {
		t.Fatalf("model bridges must live in defs/bridges/, but OPENCODE_MODEL=%q", got)
	}
}

func TestFindEnvFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if FindEnvFile(dir) != "" {
		t.Fatal("empty dir")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindEnvFile(dir); got != filepath.Join(dir, ".env") {
		t.Fatalf("got %q", got)
	}
}
