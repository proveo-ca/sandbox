// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/_conventions/tui-design-language.puml
package contract_test

import (
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// retiringImporters lists every Go file that still imports a TUI library the port
// removes. A row leaves when its file stops importing; no row is ever added.
var retiringImporters = map[string][]string{
	"github.com/gdamore/tcell/v2": {
		"e2e/post_exit_prompt_live_test.go",
		"internal/backend/sandbox/confirm.go",
		"internal/backend/sandbox/launch_receipt_test.go",
		"internal/choiceui/anim.go",
		"internal/choiceui/anim_test.go",
		"internal/choiceui/blocked_run.go",
		"internal/choiceui/blocked_run_test.go",
		"internal/choiceui/canvas.go",
		"internal/choiceui/canvas_test.go",
		"internal/choiceui/choiceui.go",
		"internal/choiceui/choiceui_test.go",
		"internal/choiceui/credential_lists_test.go",
		"internal/choiceui/fetch_pushed.go",
		"internal/choiceui/field_test.go",
		"internal/choiceui/layout_test.go",
		"internal/choiceui/modal.go",
		"internal/choiceui/modal_tea_test.go",
		"internal/choiceui/modal_test.go",
		"internal/choiceui/more_off_test.go",
		"internal/choiceui/oversized_model.go",
		"internal/choiceui/oversized_model_test.go",
		"internal/choiceui/pane_test.go",
		"internal/choiceui/pen.go",
		"internal/choiceui/pen_test.go",
		"internal/choiceui/render_test.go",
		"internal/choiceui/sandbox_close.go",
		"internal/choiceui/sandbox_close_test.go",
		"internal/choiceui/step_test.go",
		"internal/choiceui/topology.go",
		"internal/choiceui/topology_test.go",
		"internal/choiceui/viewport_render_test.go",
		"internal/ptyproxy/overlaytty.go",
		"internal/ptyproxy/overlaytty_test.go",
		"internal/ptyproxy/ptyproxy_windows.go",
	},
	"github.com/ktr0731/go-fuzzyfinder": {
		"cmd/proveo-dev/maintain.go",
		"cmd/proveo/main.go",
		"cmd/proveo/main_test.go",
		"cmd/proveo/running.go",
		"cmd/proveo/running_test.go",
	},
}

func TestTheRetiringTUILibrariesGainNoImporter(t *testing.T) {
	t.Parallel()
	got := map[string][]string{}
	for _, dir := range []string{"cmd", "internal", "e2e"} {
		walkGoFiles(t, repoRoot(t), dir, func(rel string, file *ast.File, _ *token.FileSet) {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if _, retiring := retiringImporters[path]; retiring {
					got[path] = append(got[path], rel)
				}
			}
		})
	}
	for lib, listed := range retiringImporters {
		for _, file := range got[lib] {
			if !slices.Contains(listed, file) {
				t.Errorf("%s is a new importer of %s; the port removes that library, so draw with Bubble Tea and lipgloss", file, lib)
			}
		}
		for _, file := range listed {
			if !slices.Contains(got[lib], file) {
				t.Errorf("%s no longer imports %s; delete its row so the list only shrinks", file, lib)
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
