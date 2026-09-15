//go:build e2e

// SPEC: _spec/tests/42-hello-world-e2e.puml, _spec/tests/testing-strategy.puml

package e2e

import "testing"

// A skip guard is only worth having if it answers for the id the run requests.
// The guard this replaces matched the base name anywhere in /api/tags, so a
// host with only "gemma4:26b" reported "gemma4" as available and the run then
// died on `model 'gemma4' not found` — a real failure dressed as a real pass.
func TestResolveOllamaTag(t *testing.T) {
	host := []string{"qwen3.8:27b-mlx", "qwen3.8:latest", "gemma4:26b"}

	for _, c := range []struct {
		name    string
		want    string
		tags    []string
		tag     string
		skipped bool
	}{
		{name: "exact tag", want: "gemma4:26b", tags: host, tag: "gemma4:26b"},
		{name: "bare name prefers latest", want: "qwen3.8", tags: host, tag: "qwen3.8:latest"},
		{name: "bare name takes the one variant", want: "gemma4", tags: host, tag: "gemma4:26b"},
		{name: "absent tag of a present model", want: "gemma4:9b", tags: host, skipped: true},
		{name: "absent model", want: "llama9", tags: host, skipped: true},
		{name: "empty host", want: "gemma4", tags: nil, skipped: true},
		{
			name:    "ambiguous base name is the operator's call",
			want:    "gemma4",
			tags:    []string{"gemma4:26b", "gemma4:9b"},
			skipped: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			tag, why := resolveOllamaTag(c.want, c.tags)
			if c.skipped {
				if why == "" {
					t.Fatalf("resolveOllamaTag(%q) = %q, want a skip reason", c.want, tag)
				}
				if tag != "" {
					t.Errorf("a skip must name no tag, got %q", tag)
				}
				return
			}
			if why != "" {
				t.Fatalf("resolveOllamaTag(%q) skipped: %s", c.want, why)
			}
			if tag != c.tag {
				t.Errorf("resolveOllamaTag(%q) = %q, want %q", c.want, tag, c.tag)
			}
		})
	}
}

// The substring guard's exact failure, pinned so it cannot return.
func TestResolveOllamaTagRejectsASubstringMatch(t *testing.T) {
	if tag, why := resolveOllamaTag("gemma", []string{"gemma4:26b"}); why == "" {
		t.Errorf(`"gemma" resolved to %q; "gemma4:26b" is a different model`, tag)
	}
}
