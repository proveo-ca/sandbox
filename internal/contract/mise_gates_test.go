// SPEC: _spec/_devops/unit-baseline.puml
package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const raceToggle = "{% if not usage.no_race %}-race {% endif %}"

var baselineName = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)

func TestUnitBaselineRowsNameRealTests(t *testing.T) {
	t.Parallel()
	list, ok := readMise(t).vars["unit_baseline"]
	if !ok || list == "" {
		t.Fatal("mise.toml [vars] has no unit_baseline; delete the baseline task instead of emptying the list")
	}
	names := strings.Split(list, "|")
	if !slices.IsSorted(names) {
		t.Error("[vars] unit_baseline is not sorted")
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Error("[vars] unit_baseline lists a test twice")
	}
	defined := map[string][]string{}
	for _, name := range names {
		if !baselineName.MatchString(name) {
			t.Errorf("[vars] unit_baseline row %q is not a top-level Go test name", name)
		}
		defined[name] = nil
	}
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, rel := range trackedFiles(t) {
		if !strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(names, func(n string) bool { return strings.Contains(string(src), "func "+n+"(") }) {
			continue
		}
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				if files, listed := defined[fn.Name.Name]; listed {
					defined[fn.Name.Name] = append(files, rel)
				}
			}
		}
	}
	for _, name := range names {
		if files := defined[name]; len(files) != 1 {
			t.Errorf("[vars] unit_baseline row %s is a top-level test in %d tracked files %v, want exactly 1: "+
				"-skip is unscoped, so the row must name one test", name, len(files), files)
		}
	}
	re, err := regexp.Compile("^(" + list + ")$")
	if err != nil {
		t.Fatalf("baseline's -skip pattern does not compile: %v", err)
	}
	for _, name := range names {
		if !re.MatchString(name) || re.MatchString(name+"X") || re.MatchString("X"+name) {
			t.Errorf("baseline's -skip pattern does not match %s alone", name)
		}
	}
}

func TestBaselineSkipsExactlyTheListedTests(t *testing.T) {
	t.Parallel()
	task := readMise(t).task(t, "baseline")
	want := []string{"go test " + raceToggle + "-count=1 -skip={{ ('^(' ~ vars.unit_baseline ~ ')$') | quote }}" +
		"{% for a in usage.go_test_args %} {{ a | quote }}{% endfor %}"}
	if got := task.cmds(); !slices.Equal(got, want) || len(task.run()) != 1 {
		t.Errorf("baseline runs %q, want %q: -skip anchors the [vars] list as one quoted word and every arg is quoted", got, want)
	}
	usage := task.str("usage")
	for _, line := range []string{
		`flag "--no-race" `,
		`arg "[go_test_args]..." var=#true default="./..." `,
	} {
		if !strings.Contains(usage, line) {
			t.Errorf("baseline's usage lacks %q: packages default to ./... and go test flags pass after --", line)
		}
	}
}

func TestUnitLanesOfferNoRace(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	for _, name := range []string{"test-go", "baseline"} {
		task := cfg.task(t, name)
		if !strings.Contains(task.str("usage"), `flag "--no-race" `) {
			t.Errorf("%s declares no --no-race flag; a toolchain without cgo cannot build -race", name)
		}
		toggled := 0
		for _, cmd := range task.cmds() {
			if strings.HasPrefix(cmd, "go test ") {
				toggled += strings.Count(cmd, raceToggle)
				cmd = strings.ReplaceAll(cmd, raceToggle, "")
			}
			if strings.Contains(cmd, "-race") {
				t.Errorf("%s runs -race outside %q: %q", name, raceToggle, cmd)
			}
		}
		if toggled != 1 {
			t.Errorf("%s puts -race inside %q %d times, want once", name, raceToggle, toggled)
		}
	}
	ci := cfg.task(t, "ci")
	if !strings.Contains(ci.str("usage"), `flag "--no-race" `) {
		t.Error("ci declares no --no-race flag; a toolchain without cgo has no route to test-go --no-race")
	}
	if got, want := ci.cmds(), []string{"mise run lint ::: build-check ::: test-go{% if usage.no_race %} --no-race{% endif %}"}; !slices.Equal(got, want) {
		t.Errorf("ci runs %q, want %q", got, want)
	}
	if strings.Contains(readRepoFile(t, "mise.toml"), "exec(") {
		t.Error("mise.toml calls exec(): a render-time probe runs with the caller's PATH, not the task's tools")
	}
}

func TestCoverageReadsTheUnitLane(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	unit := cfg.task(t, "test-go")
	var got []string
	for _, cmd := range unit.cmds() {
		got = append(got, strings.Replace(cmd, raceToggle, "", 1))
	}
	want := []string{"rm -rf cov/unit", "mkdir -p cov/unit",
		"go test -cover -covermode=atomic ./... -args -test.gocoverdir={{ config_root | quote }}/cov/unit"}
	if !slices.Equal(got, want) || len(unit.run()) != len(want) {
		t.Errorf("test-go runs %q (race toggle dropped), want %q", got, want)
	}
	coverage := cfg.task(t, "coverage")
	if !slices.Contains(coverage.list("wait_for"), "test-go") {
		t.Error("coverage must wait_for test-go, so `mise run test-go ::: coverage` reads finished data")
	}
	want = []string{"go tool covdata percent -i=cov/unit", "go tool covdata textfmt -i=cov/unit -o=cov/coverage.out"}
	if got := coverage.cmds(); !slices.Equal(got, want) || len(coverage.run()) != len(want) {
		t.Errorf("coverage runs %q, want %q", got, want)
	}
}

// timeoutArg is the Tera for PROVEO_TEST_GO_TIMEOUT as one quoted word, or def when it is unset or blank.
func timeoutArg(def string) string {
	return "{% set timeout = get_env(name='PROVEO_TEST_GO_TIMEOUT', default='') | trim %}" +
		"{% if timeout %}{{ timeout | quote }}{% else %}" + def + "{% endif %}"
}

var getEnvCall = regexp.MustCompile(`get_env\(name='([A-Z0-9_]+)', default='([^']*)'\)( \| trim)?`)

func TestOperatorEnvTreatsEmptyAsUnset(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")
	calls := getEnvCall.FindAllStringSubmatch(src, -1)
	if n := strings.Count(src, "get_env("); n == 0 || n != len(calls) {
		t.Fatalf("mise.toml holds %d get_env( calls and %d of the form get_env(name='X', default='…')", n, len(calls))
	}
	for _, m := range calls {
		if m[2] != "" || m[3] == "" {
			t.Errorf("mise.toml reads %s as %q; want get_env(name='%s', default='') | trim with the fallback "+
				"in {%% else %%}: get_env applies its default only when %s is unset, so an empty %s would reach the task",
				m[1], m[0], m[1], m[1], m[1])
		}
	}
}

func TestE2ELanesKeepTheirTimeoutDefaults(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	want := []string{"go test -tags=e2e ./e2e/ -count=1 -timeout " + timeoutArg("45m")}
	for _, name := range []string{"test-go-e2e", "test-e2e"} {
		task := cfg.task(t, name)
		if got := task.cmds(); !slices.Equal(got, want) || len(task.run()) != 1 {
			t.Errorf("%s runs %q, want %q", name, got, want)
		}
		if _, ok := task.fields["usage"]; ok {
			t.Errorf("%s declares a usage spec; without one, extra args append to its go test line", name)
		}
	}
	if !slices.Contains(cfg.task(t, "test-e2e").list("depends"), "test-images") {
		t.Error("test-e2e must depend on test-images")
	}
}
