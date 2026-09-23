// SPEC: _spec/_plans/host-shell-to-go.puml
package imagebuild_test

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

var pinned = map[string]string{
	"CLAUDE_CODE_VERSION": "9.9.1", "OPENCODE_VERSION": "9.9.2", "CECLI_VERSION": "9.9.3",
	"SERENA_VERSION": "9.9.4", "CURSOR_AGENT_VERSION": "9.9.5",
}

type recorder struct {
	calls []string
	cold  bool
	have  map[string]bool
}

func (r *recorder) Output(args ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	switch {
	case r.cold && len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && !r.have[args[2]]:
		return "", errors.New("no such image")
	case r.cold && len(args) >= 1 && args[0] == "pull":
		return "", errors.New("pull denied")
	case len(args) >= 2 && args[0] == "context":
		return "default\n", nil
	case len(args) >= 2 && args[0] == "buildx" && args[1] == "inspect":
		return "Driver: docker\nStatus: running\n", nil
	}
	return "", nil
}

func (r *recorder) Stream(_ io.Writer, args ...string) error {
	r.calls = append(r.calls, strings.Join(args, " "))
	if ref, ok := imagebuild.ArgRef(args); ok && r.have != nil {
		r.have[ref] = true
	}
	return nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// normBuilds reduces each `buildx build` to a sorted flag multiset with
// cleaned paths, so argument order and `a/../b` spellings do not count.
func normBuilds(lines []string) []string {
	valued := map[string]bool{"--builder": true, "--platform": true, "--build-arg": true, "-f": true, "-t": true, "--tag": true}
	var out []string
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "buildx build ")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		var parts []string
		for i := 0; i < len(f); i++ {
			switch {
			case valued[f[i]] && i+1 < len(f):
				v := f[i+1]
				if f[i] == "-f" {
					v = filepath.Clean(v)
				}
				parts = append(parts, f[i]+" "+v)
				i++
			case strings.HasPrefix(f[i], "-"):
				parts = append(parts, f[i])
			default:
				parts = append(parts, "ctx "+filepath.Clean(f[i]))
			}
		}
		slices.Sort(parts)
		out = append(out, strings.Join(parts, " | "))
	}
	return out
}

func registry(t *testing.T, root string) []maintain.Target {
	t.Helper()
	ms, err := manifest.Load(filepath.Join(root, "defs"))
	if err != nil {
		t.Fatal(err)
	}
	return maintain.Registry(ms, filepath.Join(root, "defs"))
}

func goBuilds(t *testing.T, root, name, tag string, push bool, cacheDir string, cold bool) []string {
	t.Helper()
	env := map[string]string{"PROVEO_BUILDKIT_CACHE_DIR": cacheDir}
	for k, v := range pinned {
		env[k] = v
	}
	r := &recorder{cold: cold, have: map[string]bool{}}
	b := &imagebuild.Builder{
		RepoRoot: root, Docker: r, Getenv: func(k string) string { return env[k] },
		Out: io.Discard, UI: ui.New(io.Discard), Arch: "arm64",
		Fetch:    func(string) ([]byte, error) { t.Fatal("parity must not reach the network"); return nil, nil },
		MkdirAll: os.MkdirAll,
	}
	if err := b.BuildTarget(name, tag, imagebuild.Options{Push: push}); err != nil {
		t.Fatalf("BuildTarget(%s): %v", name, err)
	}
	return normBuilds(r.calls)
}

// TestEverySpecIsARegistryTarget keeps the spec table and maintain's registry
// one list: a target with no spec cannot build, a spec with no target is dead.
func TestEverySpecIsARegistryTarget(t *testing.T) {
	var names []string
	for _, tg := range registry(t, repoRoot(t)) {
		names = append(names, tg.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, imagebuild.SpecNames()) {
		t.Errorf("registry targets %v\n!= build specs %v", names, imagebuild.SpecNames())
	}
}

var update = flag.Bool("update", false, "rewrite testdata/build_argv.golden")

type mode struct {
	name string
	tag  string
	push bool
	cold bool
}

var modes = []mode{{"local", "local", false, false}, {"latest", "latest", true, false}, {"cold", "local", false, true}}

func renderGolden(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	for _, name := range imagebuild.SpecNames() {
		for _, m := range modes {
			cache := t.TempDir()
			sb.WriteString("## " + name + " " + m.name + "\n")
			for _, b := range goBuilds(t, root, name, m.tag, m.push, cache, m.cold) {
				b = strings.ReplaceAll(b, cache, "<cache>")
				b = strings.ReplaceAll(b, root, "<root>")
				sb.WriteString(b + "\n")
			}
		}
	}
	return sb.String()
}

// TestGoBuildMatchesGolden is the parity gate: the golden was written while
// every def's build.sh and BuildTarget rendered the same builds (P2, 2026-09-22).
func TestGoBuildMatchesGolden(t *testing.T) {
	root := repoRoot(t)
	got := renderGolden(t, root)
	path := filepath.Join("testdata", "build_argv.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — regenerate with -update and review the diff", err)
	}
	if got != string(want) {
		t.Errorf("builds drifted from %s:\n%s", path, lineDiff(string(want), got))
	}
}

func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var sb strings.Builder
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			fmt.Fprintf(&sb, "line %d\n  - %s\n  + %s\n", i+1, a, b)
		}
	}
	return sb.String()
}
