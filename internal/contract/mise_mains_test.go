// SPEC: _spec/cmd/proveo/usage.puml
package contract_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// producerMains are the package mains mise tasks may `go run`; every other maintainer step is a Go test or an upstream tool.
var producerMains = []string{"./internal/cdn/stage", "./internal/maintain/fleet"}

var cmdPackage = regexp.MustCompile(`cmd/[A-Za-z0-9_.-]+`)

// goRunTargets lists the package each `go run` run entry names, sorted and unique.
func goRunTargets(cfg *miseConfig) []string {
	var out []string
	for _, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			f := strings.Fields(cmd)
			if len(f) < 3 || f[0] != "go" || f[1] != "run" {
				continue
			}
			for _, arg := range f[2:] {
				if !strings.HasPrefix(arg, "-") {
					out = append(out, arg)
					break
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// shippedMains lists the main packages .goreleaser.yaml builds.
func shippedMains(t *testing.T) []string {
	t.Helper()
	var cfg struct {
		Builds []struct {
			Main string `yaml:"main"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".goreleaser.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range cfg.Builds {
		out = append(out, b.Main)
	}
	return out
}

func TestOnlyTwoProducerMainsRunFromMise(t *testing.T) {
	t.Parallel()
	if got := goRunTargets(readMise(t)); !slices.Equal(got, producerMains) {
		t.Errorf("mise tasks go run %q, want exactly %q: a gate is a Go test, a thin step is a native task", got, producerMains)
	}
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, pkg := range producerMains {
		files, _ := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		var srcs []string
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				srcs = append(srcs, f)
			}
		}
		if len(srcs) == 0 {
			t.Errorf("%s holds no Go source; mise.toml runs it with go run", pkg)
		}
		for _, f := range srcs {
			file, err := parser.ParseFile(fset, f, nil, parser.PackageClauseOnly)
			if err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			if file.Name.Name != "main" {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s declares package %s; a producer mise runs is package main", rel, file.Name.Name)
			}
		}
	}
	shipped := shippedMains(t)
	named := cmdPackage.FindAllString(readRepoFile(t, "mise.toml"), -1)
	slices.Sort(named)
	for _, dir := range slices.Compact(named) {
		if !slices.Contains(shipped, "./"+dir) {
			t.Errorf("mise.toml names %s, which .goreleaser.yaml does not build; a maintainer main lives under internal/", dir)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !slices.Contains(shipped, "./cmd/"+e.Name()) {
			t.Errorf("cmd/%s is a directory .goreleaser.yaml does not build; cmd/ holds shipped binaries only", e.Name())
		}
	}
}
