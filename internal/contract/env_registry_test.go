// SPEC: _spec/internal/envreg/env-registry.puml
package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envreg"
)

var proveoVar = regexp.MustCompile(`PROVEO_[A-Z0-9_]*[A-Z0-9]`)

// proveoLiterals maps every PROVEO_* name in a string literal of non-test Go under cmd/ and internal/ to its first file.
func proveoLiterals(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	found := map[string]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			switch {
			case d.IsDir() && (d.Name() == "testdata" || rel == filepath.Join("internal", "envreg")):
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, name := range proveoVar.FindAllString(s, -1) {
					if _, seen := found[name]; !seen {
						found[name] = rel
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

func TestEveryProveoVariableIsRegistered(t *testing.T) {
	t.Parallel()
	for name, file := range proveoLiterals(t) {
		if _, ok := envreg.Lookup(name); !ok {
			t.Errorf("%s reads or sets %s, which internal/envreg does not register", file, name)
		}
	}
}

func TestEveryRegisteredVariableIsStillUsed(t *testing.T) {
	t.Parallel()
	used := proveoLiterals(t)
	for _, v := range envreg.Vars {
		if strings.Contains(v.Name, "<") {
			continue
		}
		if _, ok := used[v.Name]; !ok {
			t.Errorf("internal/envreg registers %s, which no non-test Go under cmd/ or internal/ names", v.Name)
		}
	}
}
