// SPEC: _spec/_conventions/spec-conventions.puml
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

func trackedFiles(root string) []string {
	c := exec.Command("git", "ls-files")
	c.Dir = root
	out, _ := c.Output()
	return splitLines(string(out))
}
