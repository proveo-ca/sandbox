// SPEC: _spec/tests/00-testing-overview.puml
package imagetest

import "testing"

func TestPinVarUpperSnakesTheTarget(t *testing.T) {
	t.Parallel()
	for target, want := range map[string]string{
		"opencode":            "PROVEO_TEST_OPENCODE_IMAGE",
		"claudecode-solidity": "PROVEO_TEST_CLAUDECODE_SOLIDITY_IMAGE",
		"base-node-browser":   "PROVEO_TEST_BASE_NODE_BROWSER_IMAGE",
	} {
		if got := PinVar(target); got != want {
			t.Errorf("PinVar(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestPinnedRefReturnsThePinElseTheDefault(t *testing.T) {
	t.Setenv("PROVEO_TEST_HERMES_IMAGE", "")
	if got := PinnedRef("hermes", "example/hermes:default"); got != "example/hermes:default" {
		t.Errorf("unset: Ref = %q, want the default", got)
	}
	t.Setenv("PROVEO_TEST_HERMES_IMAGE", " example/hermes:pin ")
	if got := PinnedRef("hermes", "example/hermes:default"); got != "example/hermes:pin" {
		t.Errorf("set: Ref = %q, want the trimmed pin", got)
	}
}
