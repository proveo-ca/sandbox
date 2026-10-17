package entrypoint

import (
	"os"
	"testing"
)

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
