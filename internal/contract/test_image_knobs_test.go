// SPEC: _spec/tests/00-testing-overview.puml
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

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/imagetest"
)

// imageKnobName matches a rendered environment name whose last word, or last word before a dynamic X, is IMAGE.
var imageKnobName = regexp.MustCompile(`(^|_)IMAGE(_X)?$`)

// imageKnobExceptions are the image reads that pin something other than a target's image under test.
var imageKnobExceptions = []struct{ file, name string }{
	{"internal/contract/opencode_v2_test.go", "PROVEO_OPENCODE_V2_IMAGE"}, // the v2.0.25 image the offline native check asserts
}

var imageKnobDirs = []string{"e2e", filepath.Join("internal", "imagetest"), filepath.Join("internal", "contract")}

type knobFile struct {
	rel  string
	pkg  string
	file *ast.File
}

// knobFiles parses every Go file under imageKnobDirs, testdata excluded.
func knobFiles(t *testing.T) (*token.FileSet, []knobFile) {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	var out []knobFile
	for _, dir := range imageKnobDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && d.Name() == "testdata":
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(path, ".go"):
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out = append(out, knobFile{rel: filepath.ToSlash(rel), pkg: strings.TrimSuffix(f.Name.Name, "_test"), file: f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return fset, out
}

func isEnvRead(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv")
}

// calleeKey names call's callee as package.func, resolving a bare name to the caller's package.
func calleeKey(pkg string, call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return pkg + "." + fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

// envReader is a function that passes some of its parameters to os.Getenv or os.LookupEnv.
type envReader struct {
	params   map[int]bool
	variadic int
}

func (r envReader) reads(arg int) bool {
	if r.variadic >= 0 && arg >= r.variadic {
		arg = r.variadic
	}
	return r.params[arg]
}

// envReaders finds every function in files that reads the environment by a parameter, keyed by calleeKey.
func envReaders(files []knobFile) map[string]envReader {
	out := map[string]envReader{}
	for _, kf := range files {
		for _, decl := range kf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			index, variadic, i := map[string]int{}, -1, 0
			for _, field := range fn.Type.Params.List {
				if _, ok := field.Type.(*ast.Ellipsis); ok {
					variadic = i
				}
				for _, name := range field.Names {
					index[name.Name] = i
					i++
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if rng, ok := n.(*ast.RangeStmt); ok {
					if x, ok := rng.X.(*ast.Ident); ok {
						if p, ok := index[x.Name]; ok {
							for _, v := range []ast.Expr{rng.Key, rng.Value} {
								if id, ok := v.(*ast.Ident); ok && id.Name != "_" {
									index[id.Name] = p
								}
							}
						}
					}
				}
				return true
			})
			r := envReader{params: map[int]bool{}, variadic: variadic}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isEnvRead(call) || len(call.Args) == 0 {
					return true
				}
				ast.Inspect(call.Args[0], func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok {
						if p, ok := index[id.Name]; ok {
							r.params[p] = true
						}
					}
					return true
				})
				return true
			})
			if len(r.params) > 0 {
				out[kf.pkg+"."+fn.Name.Name] = r
			}
		}
	}
	return out
}

// stringConsts maps each package's const and var names to the string literal they hold.
func stringConsts(files []knobFile) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, kf := range files {
		if out[kf.pkg] == nil {
			out[kf.pkg] = map[string]string{}
		}
		ast.Inspect(kf.file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || len(spec.Names) != len(spec.Values) {
				return true
			}
			for i, v := range spec.Values {
				if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						out[kf.pkg][spec.Names[i].Name] = s
					}
				}
			}
			return true
		})
	}
	return out
}

var formatVerb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)

// renderName spells the variable name e builds, with X for each part not known statically.
func renderName(e ast.Expr, consts map[string]string) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if s, err := strconv.Unquote(v.Value); err == nil && v.Kind == token.STRING {
			return s
		}
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok {
			return s
		}
	case *ast.ParenExpr:
		return renderName(v.X, consts)
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			return renderName(v.X, consts) + renderName(v.Y, consts)
		}
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(v.Args) > 0 {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "fmt" {
				return formatVerb.ReplaceAllString(renderName(v.Args[0], consts), "X")
			}
		}
	}
	return "X"
}

func TestImageUnderTestKnobsReadThroughTheHelper(t *testing.T) {
	t.Parallel()
	fset, files := knobFiles(t)
	readers, consts := envReaders(files), stringConsts(files)
	seen := map[[2]string]bool{}
	for _, kf := range files {
		ast.Inspect(kf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			reader, wrapped := readers[calleeKey(kf.pkg, call)]
			for i, arg := range call.Args {
				if read := (isEnvRead(call) && i == 0) || (wrapped && reader.reads(i)); !read {
					continue
				}
				name := renderName(arg, consts[kf.pkg])
				if !imageKnobName.MatchString(name) {
					continue
				}
				excepted := false
				for _, ex := range imageKnobExceptions {
					if ex.file == kf.rel && ex.name == name {
						excepted, seen[[2]string{ex.file, ex.name}] = true, true
					}
				}
				if !excepted {
					t.Errorf("%s:%d reads %s directly; pin the image under test with imagetest.PinnedRef(<target>, <default>)",
						kf.rel, fset.Position(arg.Pos()).Line, name)
				}
			}
			return true
		})
	}
	for _, ex := range imageKnobExceptions {
		if !seen[[2]string{ex.file, ex.name}] {
			t.Errorf("imageKnobExceptions lists %s in %s, which no longer reads it; drop the entry", ex.name, ex.file)
		}
	}
}

func TestImageUnderTestKnobsNameABuildTarget(t *testing.T) {
	t.Parallel()
	overrides := map[string]bool{}
	for _, s := range imagebuild.Specs {
		overrides[s.Override], overrides[s.ParentOverride], overrides[s.NeedsOverride] = true, true, true
	}
	shape := regexp.MustCompile(`^PROVEO_TEST_[A-Z0-9_]+_IMAGE$`)
	for _, target := range imagebuild.SpecNames() {
		name := imagetest.PinVar(target)
		if !shape.MatchString(name) || overrides[name] {
			t.Errorf("imagetest.PinVar(%q) = %q: want PROVEO_TEST_<TARGET>_IMAGE, distinct from every build override", target, name)
		}
	}
	fset, files := knobFiles(t)
	takesTarget := map[string]bool{"imagetest.PinnedRef": true, "imagetest.PinVar": true, "imagetest.Resolve": true}
	for _, kf := range files {
		ast.Inspect(kf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !takesTarget[calleeKey(kf.pkg, call)] || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if target, _ := strconv.Unquote(lit.Value); imagebuild.Specs[target].Repo == "" {
				t.Errorf("%s:%d pins %q, which is not an imagebuild target", kf.rel, fset.Position(lit.Pos()).Line, target)
			}
			return true
		})
	}
}
