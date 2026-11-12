// SPEC: _spec/_conventions/spec-conventions.puml
package contract_test

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const maxCommentLines = 3

var (
	commentExempt = regexp.MustCompile(`^(SPEC:|covers:|go:|nolint|shellcheck|syntax=|escape=|!|\+build)|^[─━═=\-]{3,}|──`)
	goDecl        = regexp.MustCompile(`^(func|type|var|const|package)\b`)
)

// commentMarker is the line-comment marker of a path the comment lint reads, or "" for a path it skips.
func commentMarker(p string) string {
	p = filepath.ToSlash(p)
	first, rest, nested := strings.Cut(p, "/")
	if nested && (strings.HasPrefix(first, ".") || slices.Contains([]string{"_spec", "apps", "node_modules"}, first)) {
		return ""
	}
	if strings.Contains("/"+rest, "/testdata/") || strings.Contains("/"+rest, "/third_party/") {
		return ""
	}
	base := path.Base(p)
	switch {
	case strings.HasSuffix(p, ".go"):
		return "//"
	case strings.HasPrefix(base, "Dockerfile"), base == ".dockerignore",
		slices.Contains([]string{".sh", ".toml", ".yaml", ".yml", ".manifest"}, path.Ext(p)):
		return "#"
	case strings.HasPrefix(p, "packages/lib/") && !strings.Contains(base, "."):
		return "#"
	}
	return ""
}

// longCommentBlocks are the 1-based first lines of src's comment blocks over maxCommentLines counted lines.
func longCommentBlocks(marker, src string) []int {
	lines := strings.Split(src, "\n")
	var out []int
	start := -1
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), marker) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start < 0 {
			continue
		}
		next := ""
		if i < len(lines) {
			next = lines[i]
		}
		if countedCommentLines(marker, lines[start:i], next) > maxCommentLines {
			out = append(out, start+1)
		}
		start = -1
	}
	return out
}

// countedCommentLines skips blank, pointer, pragma and divider lines, and up to
// maxCommentLines lines of a Go doc comment's first sentence.
func countedCommentLines(marker string, block []string, next string) int {
	godoc := marker == "//" && goDecl.MatchString(next)
	n, sentence := 0, 0
	for _, l := range block {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), marker))
		if godoc {
			sentence++
			godoc = sentence < maxCommentLines && !strings.HasSuffix(text, ".") && !strings.Contains(text, ". ")
			continue
		}
		if text == "" || commentExempt.MatchString(text) {
			continue
		}
		n++
	}
	return n
}

// commentBlockFindings lists "path:line" for every long comment block in files, relative to root.
func commentBlockFindings(root string, files []string) []string {
	var out []string
	for _, f := range files {
		marker := commentMarker(f)
		if marker == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		for _, line := range longCommentBlocks(marker, string(b)) {
			out = append(out, fmt.Sprintf("%s:%d", f, line))
		}
	}
	return out
}

func TestNoCommentBlockOverThreeLines(t *testing.T) {
	t.Parallel()
	for _, finding := range commentBlockFindings(repoRoot(t), trackedFiles(t)) {
		t.Errorf("%s: comment block over %d lines; move the reasoning to _spec/ and keep SPEC: pointers and one-line labels",
			finding, maxCommentLines)
	}
}

func TestEveryTrackedGoFileIsGofmted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range trackedFiles(t) {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		formatted, err := format.Source(src)
		switch {
		case err != nil:
			t.Errorf("%s does not parse as Go: %v", rel, err)
		case !bytes.Equal(src, formatted):
			t.Errorf("%s is not gofmt-clean: run `mise run fmt`, or `gofmt -w %s` when golangci-lint does not load it", rel, rel)
		}
	}
}

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
