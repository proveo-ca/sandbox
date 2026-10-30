// SPEC: _spec/internal/envreg/env-registry.puml
package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envreg"
	"github.com/proveo-ca/proveo/internal/imagebuild"
)

var proveoVar = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(PROVEO_[A-Z0-9_]*[A-Z0-9])`)

// proveoNames returns the PROVEO_* tokens in s that no identifier character precedes.
func proveoNames(s string) []string {
	var out []string
	for _, m := range proveoVar.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// notVariables are PROVEO_* tokens the scanned code holds that name no environment variable.
var notVariables = map[string]string{
	"PROVEO_ANDROID_END":       "marker line in gradle.properties",
	"PROVEO_ANDROID_START":     "marker line in gradle.properties",
	"PROVEO_BROWSER_SKILL_SRC": "readonly shell constant",
	"PROVEO_HOUSE_RULES_FILE":  "readonly shell constant",
	"PROVEO_MODELS":            "log line the def entrypoints print",
	"PROVEO_RC_END":            "marker line in ~/.bashrc",
	"PROVEO_RC_START":          "marker line in ~/.bashrc",
	"PROVEO_RULES_END":         "marker line in a house-rules AGENTS.md",
	"PROVEO_RULES_START":       "marker line in a house-rules AGENTS.md",
	"PROVEO_SMOKE_READY":       "sentinel the smoke mode prints",
}

// imageAndInstallerRoots are the non-Go paths, relative to the repo root, whose PROVEO_* tokens the registry covers.
var imageAndInstallerRoots = []string{
	"packages/lib",
	"defs",
	"apps/cli/public/cli/install.sh",
	"apps/cli/public/cli/install.ps1",
	"apps/cli/public/cli/uninstall.sh",
}

// testCode reports whether the slash-separated repo path rel is test code or a golden the scans skip.
func testCode(rel string, dir bool) bool {
	base := path.Base(rel)
	if dir {
		switch rel {
		case "e2e", "internal/imagetest", "internal/installtest":
			return true
		}
		return base == "testdata" || base == "goldens" || base == "node_modules"
	}
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, ".golden")
}

// scannedNames maps every PROVEO_* token in non-test code to its first file: Go string literals
// under cmd/ and internal/, and every non-Markdown file under imageAndInstallerRoots.
func scannedNames(t *testing.T) map[string]string {
	t.Helper()
	found, _ := scan(t)
	return found
}

// installerScript reports whether rel is one of the install scripts.
func installerScript(rel string) bool { return strings.HasPrefix(rel, "apps/cli/public/cli/") }

// scan returns scannedNames plus, per name, the readers whose files name it.
func scan(t *testing.T) (map[string]string, map[string]envreg.Where) {
	t.Helper()
	root := repoRoot(t)
	found := map[string]string{}
	where := map[string]envreg.Where{}
	add := func(rel string, names []string) {
		w := envreg.Image
		switch {
		case strings.HasSuffix(rel, ".go"):
			w = envreg.Binary
		case installerScript(rel):
			w = envreg.Installer
		}
		for _, name := range names {
			if _, seen := found[name]; !seen {
				found[name] = rel
			}
			where[name] |= w
		}
	}
	walk := func(start string, visit func(path, rel string) error) {
		err := filepath.WalkDir(filepath.Join(root, start), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			switch {
			case testCode(rel, d.IsDir()) && d.IsDir():
				return filepath.SkipDir
			case d.IsDir() || testCode(rel, false):
				return nil
			}
			return visit(p, rel)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		walk(dir, func(p, rel string) error {
			if !strings.HasSuffix(rel, ".go") || strings.HasPrefix(rel, "internal/envreg/") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					add(rel, proveoNames(s))
				}
				return true
			})
			return nil
		})
	}
	for _, start := range imageAndInstallerRoots {
		walk(start, func(p, rel string) error {
			if strings.HasSuffix(rel, ".md") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			add(rel, proveoNames(string(b)))
			return nil
		})
	}
	return found, where
}

const imageFamily = "PROVEO_<TARGET>_IMAGE"

// imageOverrides are the PROVEO_<TARGET>_IMAGE names imagebuild.Specs reads.
func imageOverrides() map[string]bool {
	out := map[string]bool{}
	for _, s := range imagebuild.Specs {
		for _, name := range []string{s.Override, s.ParentOverride, s.NeedsOverride} {
			if name != "" {
				out[name] = true
			}
		}
	}
	return out
}

func registeredNames() map[string]bool {
	out := map[string]bool{}
	for _, v := range envreg.Vars {
		out[v.Name] = true
	}
	return out
}

func TestEveryProveoVariableIsRegistered(t *testing.T) {
	t.Parallel()
	registered := registeredNames()
	overrides := imageOverrides()
	for name := range overrides {
		if !registered[imageFamily] && !registered[name] {
			t.Errorf("imagebuild reads %s, which internal/envreg registers neither by name nor as %s", name, imageFamily)
		}
	}
	for name, file := range scannedNames(t) {
		if _, skip := notVariables[name]; skip {
			continue
		}
		if !registered[name] && (!overrides[name] || !registered[imageFamily]) {
			t.Errorf("%s reads or sets %s, which internal/envreg does not register", file, name)
		}
	}
}

func TestEveryRegisteredVariableIsStillUsed(t *testing.T) {
	t.Parallel()
	used := scannedNames(t)
	for _, v := range envreg.Vars {
		if strings.Contains(v.Name, "<") {
			continue
		}
		if _, ok := used[v.Name]; !ok {
			t.Errorf("internal/envreg registers %s, which no scanned non-test code names", v.Name)
		}
		if why, ok := notVariables[v.Name]; ok {
			t.Errorf("internal/envreg registers %s, which this test lists as no variable (%s)", v.Name, why)
		}
	}
	for name, why := range notVariables {
		if _, ok := used[name]; !ok {
			t.Errorf("notVariables lists %s (%s), which no scanned non-test code names", name, why)
		}
	}
}

func TestEveryAliasNamesARegisteredVariable(t *testing.T) {
	t.Parallel()
	registered := registeredNames()
	for _, v := range envreg.Vars {
		if v.AliasOf != "" && !registered[v.AliasOf] {
			t.Errorf("internal/envreg registers %s as an alias of %s, which it does not register", v.Name, v.AliasOf)
		}
	}
}

func TestEveryRegisteredReaderMatchesTheCodeThatNamesIt(t *testing.T) {
	t.Parallel()
	_, seen := scan(t)
	for _, v := range envreg.Vars {
		if strings.Contains(v.Name, "<") {
			continue
		}
		got := seen[v.Name]
		for _, r := range []envreg.Where{envreg.Binary, envreg.Image, envreg.Installer} {
			switch {
			case v.Where&r != 0 && got&r == 0:
				t.Errorf("internal/envreg says %s is read by %s, but no %s code names it", v.Name, r, r)
			case r != envreg.Binary && got&r != 0 && v.Where&r == 0:
				t.Errorf("%s code names %s, but internal/envreg's Where (%s) leaves %s out", r, v.Name, v.Where, r)
			}
		}
	}
}
