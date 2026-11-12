// SPEC: _spec/tests/testing-strategy.puml
package imagetest

import "testing"

func TestNotBuiltHintNamesTheBuildTarget(t *testing.T) {
	t.Parallel()
	for image, want := range map[string]string{
		"proveo/codex:local":                               "image proveo/codex:local not built (mise run build codex)",
		"proveo/claudecode-browser:local":                  "image proveo/claudecode-browser:local not built (mise run build claudecode-browser)",
		"registry.example:5000/proveo/opencode:local":      "image registry.example:5000/proveo/opencode:local not built (mise run build opencode)",
		"registry.example:5000/proveo/cursor-browser:rc-1": "image registry.example:5000/proveo/cursor-browser:rc-1 not built (mise run build cursor-browser)",
	} {
		if got := notBuiltHint(image); got != want {
			t.Errorf("notBuiltHint(%q) = %q, want %q", image, got, want)
		}
	}
}
