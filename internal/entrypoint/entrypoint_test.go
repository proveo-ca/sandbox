package entrypoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldSkipEnvLoad(t *testing.T) {
	t.Parallel()
	only := func(name, v string) func(string) string {
		return func(k string) string {
			if k == name {
				return v
			}
			return ""
		}
	}
	for _, name := range []string{"SANDBOX_NAME", "SANDBOX_VM_ID"} {
		if !ShouldSkipEnvLoad(only(name, "proveo-claudecode-0123abcd")) {
			t.Errorf("with %s set the workspace .env must stay out of the agent env", name)
		}
		for _, v := range []string{"", "   "} {
			if ShouldSkipEnvLoad(only(name, v)) {
				t.Errorf("%s=%q is not a sandbox", name, v)
			}
		}
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
