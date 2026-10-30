// SPEC: _spec/tests/20-contract.puml, _spec/tests/43-toolchain-e2e.puml, _spec/_runtimes/toolchain-provisioning.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func entrypointLib(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var kept []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

func caseBody(t *testing.T, src, name string) string {
	t.Helper()
	head := name + `() { case "$1" in`
	start := strings.Index(src, head)
	if start < 0 {
		t.Fatalf("%s not found in entrypoint-lib.sh — did the detector move?", name)
	}
	rest := src[start+len(head):]
	end := strings.Index(rest, "esac; }")
	if end < 0 {
		t.Fatalf("%s has no closing `esac; }`", name)
	}
	return rest[:end]
}

var quotedEchoRe = regexp.MustCompile(`(?m)^\s*([a-z]+)\)\s+echo\s+"([^"]+)"`)

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func missingExtra(got map[string]bool, want []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
		if !got[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range keys(got) {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	return missing, extra
}

func boolSet(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

var imageDockerfiles = map[string]string{
	"proveo/base":              "defs/base/Dockerfile",
	"proveo/base-node":         "defs/base-node/Dockerfile",
	"proveo/base-node-browser": "defs/base-node-browser/Dockerfile",
	"proveo/opencode":          "defs/opencode/Dockerfile",
	"proveo/cursor":            "defs/cursor/Dockerfile",
	"proveo/claudecode":        "defs/claudecode/mcp/Dockerfile",
	"proveo/codex":             "defs/codex/Dockerfile",
}

var baseImageRe = regexp.MustCompile(`(?m)^ARG BASE_IMAGE=(\S+)`)

func dockerfileBody(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func baseOf(body string) string {
	m := baseImageRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSuffix(m[1], ":latest")
}

func installedPackages(body string) map[string]bool {
	logical := strings.ReplaceAll(body, "\\\n", " ")
	pkgs := map[string]bool{}
	for _, line := range strings.Split(logical, "\n") {
		for _, seg := range strings.Split(line, "&&") {
			for _, marker := range []string{"apt-get install", "npm install -g"} {
				i := strings.Index(seg, marker)
				if i < 0 {
					continue
				}
				collectPackages(pkgs, seg[i+len(marker):])
			}
		}
	}
	return pkgs
}

func collectPackages(into map[string]bool, s string) {
	for _, tok := range strings.Fields(s) {
		tok = strings.Trim(tok, `"'`)
		if tok == "" || strings.HasPrefix(tok, "-") || strings.ContainsAny(tok, "|;$(){}") {
			continue
		}
		if at := strings.LastIndex(tok, "@"); at > 0 {
			tok = tok[:at]
		}
		into[tok] = true
	}
}

func TestNoImageReinstallsWhatItsBaseAlreadyCarries(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{}
	for img, rel := range imageDockerfiles {
		bodies[img] = dockerfileBody(t, rel)
	}

	for _, img := range sortedKeys(bodies) {
		own := installedPackages(bodies[img])
		seen := map[string]bool{img: true}
		for base := baseOf(bodies[img]); base != ""; base = baseOf(bodies[base]) {
			if seen[base] {
				t.Fatalf("cycle in image lineage at %s", base)
			}
			seen[base] = true
			body, ok := bodies[base]
			if !ok {
				t.Errorf("%s builds FROM %s, which is not in imageDockerfiles", img, base)
				break
			}
			inherited := installedPackages(body)
			for _, pkg := range keys(own) {
				if inherited[pkg] {
					t.Errorf("%s (%s) installs %q, which %s already carries — "+
						"delete it and inherit the base's copy",
						img, imageDockerfiles[img], pkg, base)
				}
			}
		}
	}
}

func TestPlantUMLShipsOnceAsTheUpstreamJar(t *testing.T) {
	t.Parallel()
	base := dockerfileBody(t, imageDockerfiles["proveo/base"])
	for _, want := range []string{"/opt/plantuml.jar", "/usr/local/bin/plantuml"} {
		if !strings.Contains(base, want) {
			t.Errorf("proveo/base must ship plantuml as the jar + shim; %q not found", want)
		}
	}
	for _, img := range sortedKeys(imageDockerfiles) {
		if img == "proveo/base" {
			continue
		}
		if installedPackages(dockerfileBody(t, imageDockerfiles[img]))["plantuml"] {
			t.Errorf("%s (%s) apt-installs plantuml; proveo/base already provides the jar + shim",
				img, imageDockerfiles[img])
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEveryHarnessCanWriteGitHistory(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	for _, m := range ms {
		if m.Workspace.GitMode == "ro" {
			t.Errorf("harness %q declares workspace.gitMode: ro — agents commit, amend and "+
				"branch as part of normal work, and gitMode is now honoured in every scope, "+
				"so this blocks git writes everywhere rather than only under --scope", m.Name)
		}
		if m.Workspace.Mode == "ro" {
			t.Errorf("harness %q declares workspace.mode: ro — the whole tree, including .git, "+
				"would be read-only", m.Name)
		}
	}
}
