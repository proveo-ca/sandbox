// SPEC: _spec/_conventions/tui-design-language.puml, _spec/internal/ui/output-vocabulary.puml
package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// exemptFromTheGlyphBan names the files still printing in the old style, each
// with the reason it is still allowed to. An exemption is a FACT in this test
// rather than a gap in it: deleting an entry is how the work gets picked up,
// and nothing drifts back in behind one.
//
// The two in-container files are surface A of
// _spec/_plans/tui-style-deferred-surfaces.puml — deferred because sbx is
// expected to absorb what they report, and because they are baked into images,
// so iterating on them costs a rebuild rather than a test run.
var exemptFromTheGlyphBan = map[string]string{
	"cmd/proveo-entrypoint/main.go":     "surface A — the in-container preamble; sbx's kit may absorb it entirely",
	"internal/entrypoint/entrypoint.go": "surface A — PROVEO_SMOKE_READY, printed inside the image",

	// Surface C — the defs/**/*.sh layer, 105 emoji lines over 21 files. Not a
	// print site itself: this file ASSERTS what defs/lib/docker-build.sh prints,
	// so its expectations move when that script does, not before.
	"internal/contract/agent_pin_test.go": "surface C — mirrors defs/lib/docker-build.sh's pin note",

	// Fixtures, not print sites. Both are load-bearing exactly BECAUSE they
	// hold what the vocabulary excludes.
	"internal/choiceui/pen_test.go":    "fixture — proves the pen combines the zero-width runes this ban exists for",
	"internal/agentio/agentio_test.go": "fixture — the AGENT's own output being tailed, not proveo's",
}

// TestNoEmojiReachesTheTerminal walks every Go string literal that could reach a
// writer and fails on a rune go-runewidth and a terminal can disagree about.
//
// The ban is width, not taste. It is stated two ways because one alone leaks:
// a variation selector is the disagreement itself — go-runewidth measures U+FE0F
// as one column and terminals draw the pair it decorates as two — and the
// pictographic planes are where the rest of the offenders live. A stream that
// cannot predict its own column count cannot wrap, which is how every long
// status line in this repo came to break mid-word.
//
// COMMENTS ARE SCANNED TOO. Exempting them looked reasonable — the design
// language ought to be able to name what it excludes — and it is exactly the
// loophole that let the first draft of this file hold twelve emoji in the switch
// that banned them, and put U+2705 / U+274C in the doc comment of the test
// asserting they are unnecessary. A rune is nameable by its codepoint; the glyph adds
// nothing a comment needs, and its presence teaches the next reader that the
// rule has an inside voice.
func TestNoEmojiReachesTheTerminal(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd", "lib"} {
		walkGoFiles(t, root, dir, func(rel string, file *ast.File, fset *token.FileSet) {
			if why, ok := exemptFromTheGlyphBan[rel]; ok {
				t.Logf("skipping %s: %s", rel, why)
				return
			}
			report := func(pos token.Pos, r rune, reason, where, text string) {
				t.Errorf("%s:%d: %s (U+%04X) in %s %q\n"+
					"    name the codepoint, or paint a text glyph with ui.ColorSuccess / "+
					"ui.ColorFailure.\n"+
					"    see _spec/_conventions/tui-design-language.puml",
					rel, fset.Position(pos).Line, reason, r, where, text)
			}
			for _, group := range file.Comments {
				for _, c := range group.List {
					for _, r := range c.Text {
						if reason := bannedRune(r); reason != "" {
							report(c.Pos(), r, reason, "comment", strings.TrimSpace(c.Text))
							break // one report per comment is enough to fix it
						}
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					s = lit.Value // a raw literal go/parser already accepted
				}
				for _, r := range s {
					if reason := bannedRune(r); reason != "" {
						report(lit.Pos(), r, reason, "string literal", s)
					}
				}
				return true
			})
		})
	}
}

// emojiPresentationBMP is Emoji_Presentation=Yes below U+1F000, as ranges from
// Unicode's emoji-data.txt. These are the symbols that render as emoji with NO
// variation selector to give them away, so neither of the other two rules
// catches them: U+2705 and U+274C are the green tick and red cross this
// codebase kept reaching for.
//
// Written as codepoints on purpose. A test that bans emoji must not be a file
// full of emoji — and the point it is making is that a rune carrying its own
// colour is the wrong tool when the theme already has ui.ColorSuccess and
// ui.ColorFailure to paint a plain "✓" and "×" with.
//
// The ranges are narrow, and what they leave OUT is the design language:
// U+2713 "✓", U+26A0 "⚠", U+00D7 "×", U+25CF "●", U+2500 "─", U+2192 "→",
// U+25B8 "▸" are all text-presentation and all still legal. U+26AA and U+26AB
// are not, which is the trap this catches — they read as the language's dot and
// paint two columns wide.
var emojiPresentationBMP = [][2]rune{
	{0x231A, 0x231B}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F},
	{0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE},
	{0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD},
	{0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728}, {0x274C, 0x274C},
	{0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50},
	{0x2B55, 0x2B55},
}

// bannedRune names why a rune may not be printed, or "" when it may.
func bannedRune(r rune) string {
	switch {
	case r >= 0xFE00 && r <= 0xFE0F:
		return "variation selector — go-runewidth measures it as one column, terminals draw two"
	case r >= 0x1F000 && r <= 0x1FAFF:
		return "emoji — width depends on the font, so the line cannot be measured"
	case r == 0x200D:
		return "zero-width joiner — the sequence it builds has no predictable width"
	case r >= 0xE0020 && r <= 0xE007F:
		return "tag character — invisible, and it changes what the rune before it draws"
	}
	for _, rg := range emojiPresentationBMP {
		if r >= rg[0] && r <= rg[1] {
			return "emoji-presentation symbol — two columns wide despite measuring one; " +
				"paint a text glyph with ui.ColorSuccess / ui.ColorFailure instead"
		}
	}
	return ""
}

// The language's own runes must survive the ban, or the ban has eaten the
// design. Asserted by codepoint: the legal glyphs are shown in the comment above
// because they read better than their numbers, but nothing this file BANS
// appears anywhere in it.
func TestTheDesignLanguagesRunesStayLegal(t *testing.T) {
	t.Parallel()
	for _, r := range []rune{
		0x2713,                                                 // the ok mark
		0x26A0,                                                 // the warn mark
		0x00D7,                                                 // the fail mark
		0x25CF,                                                 // the role dot, and the banner's own glyph
		0x2500,                                                 // box rule
		0x2502, 0x250C, 0x2510, 0x2514, 0x2518, 0x252C, 0x253C, // the frame
		0x2192, 0x25B8, 0x25C2, 0x25B4, 0x2191, 0x2193, // arrows and arrowheads
		0x2022, // the pulse mote
		0x2026, // the ellipsis clip() marks a truncation with
		0x00B7, // the middot separating facts
	} {
		if why := bannedRune(r); why != "" {
			t.Errorf("U+%04X is part of the design language but the ban rejects it: %s", r, why)
		}
	}
}

// The structural half of the ban: internal/ui must expose no verb that takes a
// caller-supplied glyph. Iconf did, and that is how one package came to hold 23
// different marks that no single file ever declared. Banning the runes without
// banning the parameter would leave the door open.
func TestUIExposesNoCallerChosenGlyph(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	walkGoFiles(t, root, "internal/ui", func(rel string, file *ast.File, fset *token.FileSet) {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			for _, p := range fn.Type.Params.List {
				for _, name := range p.Names {
					switch strings.ToLower(name.Name) {
					case "icon", "glyph", "mark", "prefix", "emoji":
						t.Errorf("%s:%d: %s takes a caller-chosen %q — the mark belongs to the "+
							"language, not the call site\n    see _spec/_conventions/tui-design-language.puml",
							rel, fset.Position(fn.Pos()).Line, fn.Name.Name, name.Name)
					}
				}
			}
		}
	})
}

// Every role the printer offers must be reachable through a verb, and every
// verb must name a role that exists. A role with no verb is API nobody can use;
// a verb naming no role prints undecorated and says nothing about it.
func TestEveryRoleVerbIsWired(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "ui", "ui.go"))
	if err != nil {
		t.Fatalf("read internal/ui/ui.go: %v", err)
	}
	body := string(src)
	for verb, role := range map[string]string{
		"Hostf":   "RoleHost",
		"Appf":    "RoleApp",
		"Asyncf":  "RoleAsync",
		"Cloudf":  "RoleCloud",
		"Storef":  "RoleStore",
		"Dangerf": "RoleError",
	} {
		if !strings.Contains(body, "func (p *Printer) "+verb+"(") {
			t.Errorf("internal/ui has no %s verb", verb)
		}
		if !strings.Contains(body, "func "+verb+"(") {
			t.Errorf("internal/ui has no package-level %s helper, so ui.%s does not compile", verb, verb)
		}
		if !strings.Contains(body, role) {
			t.Errorf("%s names role %s, which does not exist", verb, role)
		}
	}
}

func walkGoFiles(t *testing.T, root, dir string, visit func(rel string, f *ast.File, fset *token.FileSet)) {
	t.Helper()
	base := filepath.Join(root, dir)
	if _, err := os.Stat(base); err != nil {
		return // lib/ has no Go in every checkout
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		visit(filepath.ToSlash(rel), f, fset)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// The section vocabulary is closed the same way the roles are: a call site may
// only name a ui.Section* constant. A literal string here is how a parallel set
// of headings starts — one caller writing "creds" beside another writing
// "credentials", and the narration no longer matching the form the operator
// answered.
func TestSectionsNameAConstant(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd", "lib"} {
		walkGoFiles(t, root, dir, func(rel string, file *ast.File, fset *token.FileSet) {
			if rel == "internal/ui/ui.go" || strings.HasSuffix(rel, "_test.go") {
				return // where the constants are declared, and where they are exercised
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Section" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "ui" {
					return true
				}
				arg, ok := call.Args[0].(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(arg.Sel.Name, "Section") {
					t.Errorf("%s:%d: ui.Section takes a literal — name a ui.Section* constant\n"+
						"    see _spec/_conventions/tui-design-language.puml",
						rel, fset.Position(call.Pos()).Line)
				}
				return true
			})
		})
	}
}

// Every declared section must be reachable, and every section a caller names
// must be declared. A constant nobody uses is a heading that never appears; the
// test that would have caught it is this one.
func TestEverySectionConstantIsUsed(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "ui", "ui.go"))
	if err != nil {
		t.Fatalf("read internal/ui/ui.go: %v", err)
	}
	var declared []string
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Section") || !strings.Contains(line, "=") {
			continue
		}
		if name := strings.Fields(line)[0]; name != "Section" {
			declared = append(declared, name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("no ui.Section* constants found; has the vocabulary moved?")
	}
	used := map[string]bool{}
	for _, dir := range []string{"internal", "cmd", "lib"} {
		walkGoFiles(t, root, dir, func(rel string, file *ast.File, fset *token.FileSet) {
			if rel == "internal/ui/ui.go" || strings.HasSuffix(rel, "_test.go") {
				return
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "ui" {
						used[sel.Sel.Name] = true
					}
				}
				return true
			})
		})
	}
	for _, name := range declared {
		if !used[name] {
			t.Errorf("ui.%s is declared but no call site names it, so that heading never appears", name)
		}
	}
}
