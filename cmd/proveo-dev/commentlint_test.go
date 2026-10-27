// SPEC: _spec/_conventions/spec-conventions.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCommentMarkerCoversEverySourceKind(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"internal/run/run.go":                  "//",
		"e2e/sbx_test.go":                      "//",
		"packages/lib/entrypoint-lib.sh":       "#",
		"packages/lib/proveo-await-seed":       "#",
		"defs/base/Dockerfile":                 "#",
		"defs/hermes/harness.manifest":         "#",
		"mise.toml":                            "#",
		".dockerignore":                        "#",
		"defs/codex/defaults/config.toml":      "#",
		"README.md":                            "",
		"_spec/_plans/x.puml":                  "",
		"internal/x/testdata/a.go":             "",
		"internal/sbx/third_party/claude.yaml": "",
		".github/workflows/ci.yml":             "",
		"apps/cli/public/cli/install.sh":       "",
	} {
		if got := commentMarker(path); got != want {
			t.Errorf("commentMarker(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestLongCommentBlocksAreFlagged(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, marker, src string
		want              []int
	}{
		{"four reasoning lines", "#", "x=1\n# a\n# b\n# c\n# d\ny=2\n", []int{2}},
		{"three lines pass", "#", "# a\n# b\n# c\n", nil},
		{"pointers, pragmas and dividers do not count", "//",
			"// SPEC: _spec/a.puml\n//go:build e2e\n//nolint:errcheck\n// ── section ──\n// a\n// b\n// c\n\npackage x\n", nil},
		{"shell directives do not count", "#", "#!/bin/bash\n# shellcheck disable=SC2034\n# syntax=docker/dockerfile:1\n# a\n# b\n# c\n", nil},
		{"blank comment lines do not count", "#", "# a\n#\n# b\n#\n# c\n", nil},
		{"a godoc first sentence is free", "//",
			"// Run starts the\n// agent in its sandbox.\n// a\n// b\n// c\nfunc Run() {}\n", nil},
		{"a godoc body past the first sentence is not", "//",
			"// Run starts the agent.\n// a\n// b\n// c\n// d\nfunc Run() {}\n", []int{1}},
		{"an indented block counts", "//", "func f() {\n\t// a\n\t// b\n\t// c\n\t// d\n}\n", []int{2}},
		{"two separate blocks", "#", "# a\n# b\n# c\n# d\nx\n# e\n# f\n# g\n# h\n", []int{1, 6}},
	} {
		if got := longCommentBlocks(c.marker, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s: longCommentBlocks = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCommentLintNamesEachOffendingBlock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/a.go", "package a\n\n// a\n// b\n// c\n// d\n// e\n// f\n// g\nvar x = 1\n")
	write("defs/x/entrypoint.sh", "#!/bin/sh\n# short\n")
	write("README.md", "# a\n# b\n# c\n# d\n")
	got := commentBlockFindings(root, []string{"internal/a.go", "defs/x/entrypoint.sh", "README.md"})
	if want := []string{"internal/a.go:3"}; !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}
