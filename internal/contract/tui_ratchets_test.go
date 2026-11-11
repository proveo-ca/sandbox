// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/_conventions/tui-design-language.puml
package contract_test

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// retiredTUILibraries are the modules the Bubble Tea port removed. None may return.
var retiredTUILibraries = []string{
	"github.com/gdamore/tcell",
	"github.com/ktr0731/go-fuzzyfinder",
	"github.com/nsf/termbox-go",
}

func TestTheRetiredTUILibrariesStayOut(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal", "e2e"} {
		walkGoFiles(t, root, dir, func(rel string, file *ast.File, _ *token.FileSet) {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, lib := range retiredTUILibraries {
					if strings.HasPrefix(path, lib) {
						t.Errorf("%s imports %s; the port removed that library, so draw with Bubble Tea and lipgloss", rel, path)
					}
				}
			}
		})
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		for _, lib := range retiredTUILibraries {
			if strings.Contains(line, lib) {
				t.Errorf("go.mod requires %s again: %s", lib, strings.TrimSpace(line))
			}
		}
	}
}

// bannedRunesIn counts the banned runes in file's comments and string literals.
func bannedRunesIn(file *ast.File) int {
	n := 0
	for _, group := range file.Comments {
		for _, c := range group.List {
			for _, r := range c.Text {
				if bannedRune(r) != "" {
					n++
				}
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			s = lit.Value
		}
		for _, r := range s {
			if bannedRune(r) != "" {
				n++
			}
		}
		return true
	})
	return n
}

func TestEveryGlyphBanExemptionStillShieldsABannedRune(t *testing.T) {
	t.Parallel()
	shielded := map[string]int{}
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "cmd", "lib"} {
		walkGoFiles(t, repoRoot(t), dir, func(rel string, file *ast.File, _ *token.FileSet) {
			if _, exempt := exemptFromTheGlyphBan[rel]; exempt {
				seen[rel] = true
				shielded[rel] = bannedRunesIn(file)
			}
		})
	}
	for rel := range exemptFromTheGlyphBan {
		switch {
		case !seen[rel]:
			t.Errorf("%s is exempt from the glyph ban and does not exist; delete its row", rel)
		case shielded[rel] == 0:
			t.Errorf("%s is exempt from the glyph ban and holds no banned rune; delete its row", rel)
		}
	}
}
