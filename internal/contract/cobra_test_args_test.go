// SPEC: _spec/tests/00-test-fixtures.puml
package contract_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// executesWithoutArgs lists the places src runs a command's Execute on an identifier that has had no SetArgs since it was assigned.
func executesWithoutArgs(name, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	checkBody := func(body *ast.BlockStmt) {
		if body == nil {
			return
		}
		set := map[string]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						delete(set, id.Name)
					}
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case sel.Sel.Name == "SetArgs":
					set[id.Name] = true
				case (sel.Sel.Name == "Execute" || sel.Sel.Name == "ExecuteC") && len(n.Args) == 0,
					(sel.Sel.Name == "ExecuteContext" || sel.Sel.Name == "ExecuteContextC") && len(n.Args) == 1:
					if !set[id.Name] {
						found = append(found, fmt.Sprintf("%s: %s.%s runs on the test binary's own arguments; SetArgs first", fset.Position(n.Pos()), id.Name, sel.Sel.Name))
					}
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			checkBody(fn.Body)
		}
	}
	return found, nil
}

func TestEveryCommandATestExecutesTakesExplicitArgs(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal", "e2e"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), ".Execute") || strings.HasSuffix(path, "cobra_test_args_test.go") {
				return nil
			}
			found, err := executesWithoutArgs(path, string(src))
			if err != nil {
				return err
			}
			for _, f := range found {
				t.Error(f)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheExecuteScanSeesEachShape(t *testing.T) {
	t.Parallel()
	const head = "package x\n\nfunc TestX(t *testing.T) {\n"
	for _, c := range []struct {
		name, body string
		found      int
	}{
		{"SetArgs then Execute", "\tcmd := attachCmd()\n\tcmd.SetArgs([]string{})\n\t_ = cmd.Execute()\n", 0},
		{"Execute alone", "\tcmd := attachCmd()\n\t_ = cmd.Execute()\n", 1},
		{"Execute inside an if", "\tcmd := attachCmd()\n\tif err := cmd.Execute(); err != nil {\n\t\tt.Fatal(err)\n\t}\n", 1},
		{"a rebuilt command loses its SetArgs", "\tcmd := rootCmd()\n\tcmd.SetArgs(nil)\n\t_ = cmd.Execute()\n\tcmd = rootCmd()\n\t_ = cmd.Execute()\n", 1},
		{"Execute in a subtest after SetArgs", "\tcmd := rootCmd()\n\tcmd.SetArgs(nil)\n\tt.Run(\"x\", func(t *testing.T) { _ = cmd.Execute() })\n", 0},
		{"a template's Execute takes arguments", "\t_ = tmpl.Execute(&out, data)\n", 0},
	} {
		found, err := executesWithoutArgs("x_test.go", head+c.body+"}\n")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(found) != c.found {
			t.Errorf("%s: %d hit(s) %v, want %d", c.name, len(found), found, c.found)
		}
	}
}
