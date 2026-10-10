//go:build e2e

// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOpenCodePreparedNativeHistory(t *testing.T) {
	native := os.Getenv("PROVEO_TEST_OPENCODE_NATIVE")
	if native == "" {
		t.Skip("set PROVEO_TEST_OPENCODE_NATIVE to an installed V2 executable")
	}
	root := repoRoot(t)
	cmd := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-v",
		"test_opencode_runtime_native.NativeRuntimeTests.test_two_native_runs_and_resume_preserve_both_session_transcripts",
		"test_opencode_runtime_native.NativeRuntimeTests.test_native_account_schemas_and_control_tokens_are_scrubbed",
		"test_opencode_runtime_native.NativeRuntimeTests.test_prepared_large_history_reaches_native_output_within_five_seconds")
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "packages/lib"))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepared native history: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
