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

func TestStaleReasonNamesTheRebuild(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, got, want, reason string }{
		{"matching stamp", "abc", "abc", ""},
		{"no stamp", "", "abc", "image proveo/codex:local carries no proveo.inputs stamp, so its defs/ and packages/ are unknown (mise run build codex)"},
		{"template no value", "<no value>", "abc", "image proveo/codex:local carries no proveo.inputs stamp, so its defs/ and packages/ are unknown (mise run build codex)"},
		{"other content", "def", "abc", "image proveo/codex:local was built from other defs/ and packages/ content than this checkout (mise run build codex)"},
	} {
		if r := staleReason("proveo/codex:local", c.got, c.want); r != c.reason {
			t.Errorf("%s: staleReason = %q, want %q", c.name, r, c.reason)
		}
	}
}

func TestStaleLeavesPublishedImagesAlone(t *testing.T) {
	t.Parallel()
	for _, image := range []string{"proveo/codex:latest", "proveo/codex", "registry.example:5000/proveo/codex:rc-1"} {
		if r := Stale(image); r != "" {
			t.Errorf("Stale(%q) = %q; only a :local build answers to this checkout", image, r)
		}
	}
}
