// SPEC: _spec/internal/choiceui/tui-stack.puml
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

const bubbleTeaImport = "charm.land/bubbletea/v2"

// teaMsgLeaks lists the places src hands a Bubble Tea message to anything but a
// model's Update, names tea.EnvMsg, or turns on Bubble Tea's own log file.
func teaMsgLeaks(name, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	tea := ""
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != bubbleTeaImport {
			continue
		}
		tea = "tea"
		if imp.Name != nil {
			tea = imp.Name.Name
		}
	}
	if tea == "" {
		return nil, nil
	}
	var leaks []string
	at := func(n ast.Node, what string) {
		leaks = append(leaks, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), what))
	}
	isTea := func(e ast.Expr, sel string) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && x.Name == tea && s.Sel.Name == sel
	}
	msgParams := func(ft *ast.FuncType) map[string]bool {
		names := map[string]bool{}
		for _, f := range ft.Params.List {
			if !isTea(f.Type, "Msg") {
				continue
			}
			for _, n := range f.Names {
				names[n.Name] = true
			}
		}
		return names
	}
	checkBody := func(ft *ast.FuncType, body *ast.BlockStmt) {
		names := msgParams(ft)
		if body == nil || len(names) == 0 {
			return
		}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if s, ok := call.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Update" {
				return true
			}
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && names[id.Name] {
					at(arg, "passes a Bubble Tea message to a call; a message can be tea.EnvMsg, the whole environment")
				}
			}
			return true
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if isTea(n, "EnvMsg") {
				at(n, "names tea.EnvMsg; proveo never reads it")
			}
			if isTea(n, "LogToFile") || isTea(n, "LogToFileWith") {
				at(n, "turns on Bubble Tea's log file")
			}
		case *ast.FuncDecl:
			checkBody(n.Type, n.Body)
		case *ast.FuncLit:
			checkBody(n.Type, n.Body)
		}
		return true
	})
	return leaks, nil
}

func TestNoSourceFormatsABubbleTeaMessage(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal", "e2e"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), bubbleTeaImport) || strings.HasSuffix(path, "tea_envmsg_test.go") {
				return nil
			}
			leaks, err := teaMsgLeaks(path, string(src))
			if err != nil {
				return err
			}
			for _, l := range leaks {
				t.Error(l)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheMessageLeakCheckSeesEachWayOut(t *testing.T) {
	t.Parallel()
	const head = "package x\nimport (\n\t\"fmt\"\n\ttea \"charm.land/bubbletea/v2\"\n)\n"
	for _, c := range []struct {
		name, body string
		leaks      int
	}{
		{"a model that passes the message on", `func (m M) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.inner.Update(msg) }`, 0},
		{"a narrowed message used by method", `func (m M) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		_ = msg.String()
	}
	return m, nil
}`, 0},
		{"a filter that returns the message", `var f = func(_ tea.Model, msg tea.Msg) tea.Msg { return msg }`, 0},
		{"a formatted message", `func (m M) Update(msg tea.Msg) (tea.Model, tea.Cmd) { _ = fmt.Sprintf("%v", msg); return m, nil }`, 1},
		{"a logged message in a closure", `var f = func(_ tea.Model, msg tea.Msg) tea.Msg { fmt.Println("got", msg); return msg }`, 1},
		{"a case for the environment", `func (m M) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tea.EnvMsg:
	}
	return m, nil
}`, 1},
		{"Bubble Tea's log file", `func main() { _, _ = tea.LogToFile("debug.log", "x") }`, 1},
	} {
		leaks, err := teaMsgLeaks("x.go", head+c.body+"\n")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(leaks) != c.leaks {
			t.Errorf("%s: %d leak(s) %v, want %d", c.name, len(leaks), leaks, c.leaks)
		}
	}
	if leaks, _ := teaMsgLeaks("y.go", "package y\nimport \"fmt\"\nfunc f(msg string) { fmt.Println(msg) }\n"); len(leaks) != 0 {
		t.Errorf("a file without Bubble Tea was checked: %v", leaks)
	}
}
