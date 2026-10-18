// SPEC: _spec/tests/00-testing-overview.puml
package testimage

import "testing"

func TestVarUpperSnakesTheTarget(t *testing.T) {
	t.Parallel()
	for target, want := range map[string]string{
		"opencode":            "PROVEO_TEST_OPENCODE_IMAGE",
		"claudecode-solidity": "PROVEO_TEST_CLAUDECODE_SOLIDITY_IMAGE",
		"base-node-lsp":       "PROVEO_TEST_BASE_NODE_LSP_IMAGE",
	} {
		if got := Var(target); got != want {
			t.Errorf("Var(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestRefReturnsThePinElseTheDefault(t *testing.T) {
	t.Setenv("PROVEO_TEST_HERMES_IMAGE", "")
	if got := Ref("hermes", "proveo/hermes:latest"); got != "proveo/hermes:latest" {
		t.Errorf("unset: Ref = %q, want the default", got)
	}
	t.Setenv("PROVEO_TEST_HERMES_IMAGE", " example/hermes:pin ")
	if got := Ref("hermes", "proveo/hermes:latest"); got != "example/hermes:pin" {
		t.Errorf("set: Ref = %q, want the trimmed pin", got)
	}
}
