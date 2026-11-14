// SPEC: _spec/internal/choiceui/session.puml (OUTPUT HOLD), _spec/cmd/proveo/init-sbx-bootstrap.puml (ONE SESSION PER WALK)
package contract_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// terminalChecks are the calls that may name os.Stdout or os.Stderr to ask whether it is a terminal.
var terminalChecks = []string{"IsWriterTTY", "IsReaderTTY"}

// printsPrefixes are, per package, the name prefixes of the functions that write the process's
// own stdout or stderr without naming it.
var printsPrefixes = map[string][]string{"fmt": {"Print"}, "log": {"Print", "Fatal", "Panic"}}

// directWrites lists the places src names os.Stdout or os.Stderr outside command(), the one
// function that hands init's terminal to a child, and outside a check of whether it is a terminal;
// and every fmt.Print*, log.Print*, log.Fatal*, log.Panic*, print and println call.
func directWrites(name, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	local := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "os" && printsPrefixes[path] == nil {
			continue
		}
		local[path] = path
		if imp.Name != nil {
			local[path] = imp.Name.Name
		}
	}
	pkgSel := func(e ast.Expr, pkg string) (string, bool) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		x, ok := s.X.(*ast.Ident)
		return s.Sel.Name, ok && local[pkg] != "" && x.Name == local[pkg]
	}
	std := func(e ast.Expr) bool {
		sel, ok := pkgSel(e, "os")
		return ok && (sel == "Stdout" || sel == "Stderr")
	}
	prints := func(call *ast.CallExpr) string {
		if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "print" || id.Name == "println") {
			return id.Name
		}
		for pkg, prefixes := range printsPrefixes {
			sel, ok := pkgSel(call.Fun, pkg)
			if ok && slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(sel, p) }) {
				return pkg + "." + sel
			}
		}
		return ""
	}
	var found []string
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "command" {
			continue
		}
		checked := map[ast.Expr]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if s, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains(terminalChecks, s.Sel.Name) {
					for _, arg := range call.Args {
						checked[arg] = true
					}
				}
				if p := prints(call); p != "" {
					found = append(found, fmt.Sprintf("%s: %s writes the terminal", fset.Position(call.Pos()), p))
				}
			}
			if e, ok := n.(ast.Expr); ok && std(e) && !checked[e] {
				found = append(found, fmt.Sprintf("%s: names %s outside command()", fset.Position(e.Pos()), e.(*ast.SelectorExpr).Sel.Name))
			}
			return true
		})
	}
	return found, nil
}

// TestInitWritesTheTerminalOnlyThroughCommand holds init's source to the output hold: while the
// card is up, ui.Default is held and replayed after it, so a write straight to os.Stdout or
// os.Stderr, named or through fmt.Print*, the log package or print, would land under the card. A
// child that is no hand-off runs with its output kept; a hand-off gets the terminal through command().
func TestInitWritesTheTerminalOnlyThroughCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, src string
		writes    int
	}{
		{"command", "package main\nimport \"os\"\nfunc command() { _ = os.Stderr }", 0},
		{"a tty check", "package main\nimport \"os\"\nfunc tty() bool { return agentio.IsWriterTTY(os.Stderr) }", 0},
		{"a write", "package main\nimport \"os\"\nfunc say() { os.Stderr.WriteString(\"x\") }", 1},
		{"a child's stream", "package main\nimport \"os\"\nfunc run(c *exec.Cmd) { c.Stdout = os.Stdout }", 1},
		{"a renamed os", "package main\nimport sys \"os\"\nfunc say() { fmt.Fprintln(sys.Stderr) }", 1},
		{"fmt.Println", "package main\nimport \"fmt\"\nfunc say() { fmt.Println(\"x\") }", 1},
		{"a renamed fmt", "package main\nimport f \"fmt\"\nfunc say() { f.Printf(\"x\") }", 1},
		{"log.Printf and log.Fatal", "package main\nimport \"log\"\nfunc say() { log.Printf(\"x\"); log.Fatal(\"y\") }", 2},
		{"print and println", "package main\nfunc say() { print(\"x\"); println(\"y\") }", 2},
		{"fmt.Fprintf to a buffer", "package main\nimport \"fmt\"\nfunc say(b *bytes.Buffer) { fmt.Fprintf(b, \"x\"); _ = fmt.Sprintf(\"y\") }", 0},
		{"a logger of its own", "package main\nimport \"log\"\nfunc say(l *log.Logger) { l.Printf(\"x\") }", 0},
	} {
		got, err := directWrites(c.name+".go", c.src)
		if err != nil || len(got) != c.writes {
			t.Errorf("%s: %v %q, want %d", c.name, err, got, c.writes)
		}
	}

	files, err := filepath.Glob(filepath.Join(repoRoot(t), "cmd", "proveo", "init*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no init source: %v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		found, err := directWrites(path, string(b))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			t.Error(f)
		}
	}
}
