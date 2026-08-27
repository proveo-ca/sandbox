// SPEC: _spec/_conventions/spec-conventions.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// Notes in _spec/ are 2-5 lines of at most 50 columns. The ceiling forces one idea per
// anchor; the floor keeps every note the same shape so none reads as an afterthought
// label. This is a ratchet, not a sweep: only files listed here are enforced, and a file
// joins the list once its notes are rewritten. The point is that a compliant file cannot
// silently regress while the backlog is worked through.
var specNotesEnforced = []string{
	"_spec/_plans/main-decomposition-moves.puml",
	"_spec/_plans/main-decomposition.puml",
	"_spec/_plans/restore-green-e2e.puml",
	"_spec/_plans/revision-env-egress.puml",
	"_spec/defs/agent-definition-sharing.puml",
	"_spec/internal/choiceui/choice-prompt-render.puml",
	"_spec/internal/entrypoint/model-alias-bridges.puml",
	"_spec/internal/sbx/sbx-kit-contract.puml",
}

const (
	specNoteMinLines = 2
	specNoteMaxLines = 5
	specNoteMaxCols  = 50
)

var (
	noteOpen = regexp.MustCompile(`^note\s+(?:top|bottom|left|right)\s+of\s+\S+\s*$`)
	noteAs   = regexp.MustCompile(`^note\s+as\s+\S+\s*$`)
	// `note over X` and `note over X, Y` are the same box under a different keyword.
	// Omitting them exempted a whole note form from the rule: credential-boundary
	// carries 21 of them and the checker reported it as having no notes at all.
	noteOver   = regexp.MustCompile(`^note\s+over\s+[^:]+$`)
	noteInline = regexp.MustCompile(`^\s*note\s+(?:(?:top|bottom|left|right)\s+of|over)\s+[^:]*:`)
)

type specNote struct {
	anchor string
	line   int
	body   []string
}

func parseSpecNotes(src string) []specNote {
	var out []specNote
	var cur *specNote
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case noteOpen.MatchString(trimmed) || noteAs.MatchString(trimmed) || noteOver.MatchString(trimmed):
			out = append(out, specNote{anchor: trimmed, line: i + 1})
			cur = &out[len(out)-1]
		case trimmed == "end note":
			cur = nil
		case cur != nil:
			cur.body = append(cur.body, line)
		case noteInline.MatchString(line):
			// `note left of X : text` is a one-liner by construction; the floor forbids it.
			out = append(out, specNote{anchor: trimmed, line: i + 1, body: []string{line}})
		}
	}
	return out
}

// unwrappable reports a line holding one unbroken token — a bare URL is the case
// that matters. The column cap exists to force prose density, and a link has none
// to force: no rewrite makes it shorter, so capping it would only push sources out
// of the notes and back into comments nobody renders.
func unwrappable(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "- ")
	t = strings.Trim(t, "[]")
	return t != "" && !strings.ContainsAny(t, " \t")
}

func TestSpecNotesFitTheBox(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range specNotesEnforced {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		notes := parseSpecNotes(string(b))
		if len(notes) == 0 {
			t.Errorf("%s: enforced but has no notes — drop it from the list", rel)
		}
		for _, n := range notes {
			switch {
			case len(n.body) < specNoteMinLines:
				t.Errorf("%s:%d %q has %d line(s); split short text across %d",
					rel, n.line, n.anchor, len(n.body), specNoteMinLines)
			case len(n.body) > specNoteMaxLines:
				t.Errorf("%s:%d %q has %d lines; max %d — split onto another anchor",
					rel, n.line, n.anchor, len(n.body), specNoteMaxLines)
			}
			for k, line := range n.body {
				// Columns are a display property, so count runes: an em dash is one
				// column and three bytes, and byte length would reject a legal note.
				if w := utf8.RuneCountInString(line); w > specNoteMaxCols && !unwrappable(line) {
					t.Errorf("%s:%d %q line %d is %d cols; max %d",
						rel, n.line, n.anchor, k+1, w, specNoteMaxCols)
				}
			}
		}
	}
}

// The column cap is exempted only for text no rewrite can shorten. Widening that to
// "any long line" would retire the rule quietly, so the boundary is pinned here.
func TestUnwrappableExemptsLinksAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line string
		want bool
		why  string
	}{
		{"  - [[https://example.com/a/very/long/path/to/a/spec.md]]", true,
			"a bare link in a list is one token; wrapping it breaks the link"},
		{"  https://example.com/a/very/long/path", true, "same without the list marker"},
		{"  **A sentence that simply runs on and on past fifty columns.**", false,
			"prose is exactly what the cap exists to compress"},
		{"  - see https://example.com and also the note below", false,
			"a link inside a sentence can still be rewritten around"},
		{"", false, "an empty line is not a token"},
	} {
		if got := unwrappable(c.line); got != c.want {
			t.Errorf("unwrappable(%q) = %v, want %v — %s", c.line, got, c.want, c.why)
		}
	}
}
