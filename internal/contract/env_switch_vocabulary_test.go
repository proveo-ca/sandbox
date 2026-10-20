// SPEC: _spec/internal/envflag/env-switches.puml
package contract_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

var envSwitchWords = map[string]bool{
	"0": true, "1": true, "off": true, "on": true, "false": true, "true": true,
	"no": true, "yes": true, "disable": true, "disabled": true, "enable": true, "enabled": true,
}

// TestEnvSwitchesParseThroughEnvflag fails on a hand-rolled on/off parser outside internal/envflag.
func TestEnvSwitchesParseThroughEnvflag(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal"} {
		walkGoFiles(t, root, dir, func(rel string, file *ast.File, fset *token.FileSet) {
			if strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/testdata/") ||
				strings.HasPrefix(rel, "internal/envflag/") {
				return
			}
			for _, finding := range envSwitchFindings(file) {
				t.Errorf("%s:%d: %s — use envflag.On/Off/Bool", rel, fset.Position(finding.pos).Line, finding.what)
			}
		})
	}
}

func TestEnvSwitchScanFlagsHandRolledParsers(t *testing.T) {
	t.Parallel()
	flagged := map[string]string{
		"switch tag":    `func f() bool { switch strings.ToLower(os.Getenv("X")) { case "off": return false }; return true }`,
		"assigned var":  `func f() bool { v := os.Getenv("X"); return v == "1" }`,
		"switch init":   `func f(b *B) bool { switch v := b.env("X"); v { case "yes": return true }; return false }`,
		"getenv param":  `func f(lookup func(string) string) bool { return lookup("X") != "true" }`,
		"lookupenv":     `func f() bool { v, ok := os.LookupEnv("X"); return ok && v == "on" }`,
		"helper switch": `func f(v string) bool { switch v { case "0", "false": return true }; return false }`,
	}
	clean := map[string]string{
		"prompt answer": `func f() bool { _ = os.Getenv("X"); s, _ := r.ReadString('\n'); return s == "yes" }`,
		"prompt switch": `func f() bool { _ = os.Getenv("X"); switch s { case "y", "yes": return true }; return false }`,
		"presence":      `func f() bool { return os.Getenv("X") != "" }`,
		"enum word":     `func f() bool { return os.Getenv("X") == "auto" }`,
		"helper call":   `func f() bool { return envflag.On(os.Getenv("X")) }`,
		"non-env data":  `func f(out []byte) bool { return strings.TrimSpace(string(out)) == "1" }`,
	}
	for name, src := range flagged {
		if got := envSwitchFindings(parseFuncSrc(t, src)); len(got) == 0 {
			t.Errorf("%s: scan missed %s", name, src)
		}
	}
	for name, src := range clean {
		if got := envSwitchFindings(parseFuncSrc(t, src)); len(got) != 0 {
			t.Errorf("%s: scan flagged %s: %v", name, src, got[0].what)
		}
	}
}

func parseFuncSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f
}

type envSwitchFinding struct {
	pos  token.Pos
	what string
}

func envSwitchFindings(file *ast.File) []envSwitchFinding {
	var out []envSwitchFinding
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				out = append(out, envComparisons(fn.Type, fn.Body)...)
			}
			return false
		case *ast.FuncLit:
			out = append(out, envComparisons(fn.Type, fn.Body)...)
			return false
		}
		return true
	})
	seen := map[token.Pos]bool{}
	for _, f := range out {
		seen[f.pos] = true
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if cc, ok := n.(*ast.CaseClause); ok && !seen[cc.Pos()] {
			if words := caseWords(cc); len(words) >= 2 {
				out = append(out, envSwitchFinding{cc.Pos(), fmt.Sprintf("case lists on/off words %q", words)})
			}
		}
		return true
	})
	return out
}

func caseWords(cc *ast.CaseClause) []string {
	var words []string
	for _, e := range cc.List {
		if w, ok := switchWord(e); ok {
			words = append(words, w)
		}
	}
	return words
}

func envComparisons(typ *ast.FuncType, body *ast.BlockStmt) []envSwitchFinding {
	getenvs := map[string]bool{}
	tainted := map[string]bool{}
	collectGetenvParams(typ, getenvs)
	ast.Inspect(body, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			collectGetenvParams(fl.Type, getenvs)
		}
		return true
	})
	isTainted := func(e ast.Expr) bool {
		hit := false
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				hit = hit || isEnvCall(x, getenvs)
			case *ast.Ident:
				hit = hit || tainted[x.Name]
			}
			return !hit
		})
		return hit
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(n ast.Node) bool {
			var lhs []ast.Expr
			var rhs []ast.Expr
			switch s := n.(type) {
			case *ast.AssignStmt:
				lhs, rhs = s.Lhs, s.Rhs
			case *ast.ValueSpec:
				rhs = s.Values
				for _, id := range s.Names {
					lhs = append(lhs, id)
				}
			default:
				return true
			}
			for _, r := range rhs {
				if !isTainted(r) {
					continue
				}
				for _, l := range lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name != "_" && !tainted[id.Name] {
						tainted[id.Name], changed = true, true
					}
				}
			}
			return true
		})
	}
	var out []envSwitchFinding
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SwitchStmt:
			if x.Tag == nil || !isTainted(x.Tag) {
				return true
			}
			for _, st := range x.Body.List {
				cc := st.(*ast.CaseClause)
				if words := caseWords(cc); len(words) > 0 {
					out = append(out, envSwitchFinding{cc.Pos(), fmt.Sprintf("env value switched on %q", words)})
				}
			}
		case *ast.BinaryExpr:
			if x.Op != token.EQL && x.Op != token.NEQ {
				return true
			}
			for _, pair := range [][2]ast.Expr{{x.X, x.Y}, {x.Y, x.X}} {
				if w, ok := switchWord(pair[0]); ok && isTainted(pair[1]) {
					out = append(out, envSwitchFinding{x.Pos(), fmt.Sprintf("env value compared %s %q", x.Op, w)})
				}
			}
		}
		return true
	})
	return out
}

func collectGetenvParams(typ *ast.FuncType, into map[string]bool) {
	if typ == nil || typ.Params == nil {
		return
	}
	for _, p := range typ.Params.List {
		ft, ok := p.Type.(*ast.FuncType)
		if !ok || ft.Params == nil || len(ft.Params.List) != 1 || ft.Results == nil {
			continue
		}
		if in, ok := ft.Params.List[0].Type.(*ast.Ident); !ok || in.Name != "string" {
			continue
		}
		if out, ok := ft.Results.List[0].Type.(*ast.Ident); !ok || out.Name != "string" {
			continue
		}
		for _, n := range p.Names {
			into[n.Name] = true
		}
	}
}

func isEnvCall(call *ast.CallExpr, getenvs map[string]bool) bool {
	var name string
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if getenvs[f.Name] {
			return true
		}
		name = f.Name
	case *ast.SelectorExpr:
		name = f.Sel.Name
	default:
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), "env")
}

func switchWord(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || !envSwitchWords[strings.ToLower(s)] {
		return "", false
	}
	return s, true
}
