package maintain

import (
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestRegistry(t *testing.T) {
	t.Parallel()
	ms := []manifest.Manifest{
		{Name: "claudecode", Dir: "/d/claudecode", Images: map[string]string{
			"claudecode":          "proveo/claudecode:latest",
			"claudecode-solidity": "proveo/claudecode-solidity:latest",
		}},
		{Name: "cecli", Dir: "/d/cecli", Images: map[string]string{
			"cecli":      "proveo/cecli:latest",
			"cecli-node": "proveo/cecli-node:latest",
		}},
	}

	got := Registry(ms, "/d")

	// Stable order: base, harness (sorted), then the sidecars last.
	wantOrder := []string{
		"base", "base-node", "base-node-lsp", "base-node-browser", "cecli", "cecli-node", "claudecode",
		"claudecode-solidity", "egress-proxy", "mitmproxy",
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d targets, want %d: %+v", len(got), len(wantOrder), got)
	}
	byName := map[string]Target{}
	for i, tgt := range got {
		if tgt.Name != wantOrder[i] {
			t.Errorf("order[%d] = %q, want %q", i, tgt.Name, wantOrder[i])
		}
		byName[tgt.Name] = tgt
	}

	// Image is org/name with the manifest tag stripped; DefDir matches the Bash baseline.
	for _, tc := range []struct{ name, kind, image, dir string }{
		{"base", KindBase, "proveo/base", "/d/base"},
		{"base-node", KindBase, "proveo/base-node", "/d/base-node"},
		{"base-node-lsp", KindBase, "proveo/base-node-lsp", "/d/base-node-lsp"},
		{"base-node-browser", KindBase, "proveo/base-node-browser", "/d/base-node-browser"},
		{"cecli", KindHarness, "proveo/cecli", "/d/cecli"},
		{"cecli-node", KindHarness, "proveo/cecli-node", "/d/cecli"}, // shares cecli's def dir
		{"claudecode", KindHarness, "proveo/claudecode", "/d/claudecode"},
		{"claudecode-solidity", KindHarness, "proveo/claudecode-solidity", "/d/claudecode"},
		{"egress-proxy", KindSidecar, "proveo/egress-proxy", "/d/sidecars/egress-proxy"},
		{"mitmproxy", KindSidecar, "proveo/mitmproxy", "/d/sidecars/mitmproxy"},
	} {
		g := byName[tc.name]
		if g.Kind != tc.kind || g.Image != tc.image || g.DefDir != tc.dir {
			t.Errorf("%s = {kind:%s image:%s dir:%s}, want {kind:%s image:%s dir:%s}",
				tc.name, g.Kind, g.Image, g.DefDir, tc.kind, tc.image, tc.dir)
		}
	}

	// Build recipe: script path off DefDir, and the variant selector only on the
	// three claudecode images.
	if got := byName["claudecode"]; strings.Join(got.BuildArgs, " ") != "--variant mcp" || got.BuildScript != "/d/claudecode/build.sh" {
		t.Errorf("claudecode recipe = args:%v script:%s", got.BuildArgs, got.BuildScript)
	}
	if got := byName["claudecode-solidity"]; strings.Join(got.BuildArgs, " ") != "--variant solidity" {
		t.Errorf("claudecode-solidity args = %v, want --variant solidity", got.BuildArgs)
	}
	if got := byName["cursor"]; len(got.BuildArgs) != 0 {
		t.Errorf("cursor should have no variant args, got %v", got.BuildArgs)
	}
	if got := byName["cecli-node"]; got.BuildScript != "/d/cecli/build.sh" {
		t.Errorf("cecli-node build script = %s, want /d/cecli/build.sh (shared)", got.BuildScript)
	}
}

func argvs(cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = strings.Join(c.Argv, " ")
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	t.Parallel()
	cc := Target{Name: "claudecode", Image: "proveo/claudecode", DefDir: "/d/claudecode",
		BuildScript: "/d/claudecode/build.sh", BuildArgs: []string{"--variant", "mcp"}}

	// Default (latest): build via the variant script, then verify.
	got := argvs(cc.BuildPlan("latest", false))
	want := []string{
		"bash /d/claudecode/build.sh --variant mcp",
		"docker image inspect proveo/claudecode:latest",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(latest) = %v, want %v", got, want)
	}

	// Tagged + no-cache: --tag on build.sh (buildx --load) and verify.
	got = argvs(cc.BuildPlan("v2", true))
	want = []string{
		"bash /d/claudecode/build.sh --variant mcp --tag v2 --no-cache",
		"docker image inspect proveo/claudecode:v2",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(v2,no-cache) = %v, want %v", got, want)
	}

	// An untagged build is :local, never :latest — :latest means published, and a
	// local build answering to it is what let a registry image shadow the build
	// under test.
	cur := Target{Name: "cursor", Image: "proveo/cursor", DefDir: "/d/cursor", BuildScript: "/d/cursor/build.sh"}
	got = argvs(cur.BuildPlan("", false))
	want = []string{
		"bash /d/cursor/build.sh --tag local",
		"docker image inspect proveo/cursor:local",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(default) = %v, want %v", got, want)
	}

	// The verify step discards stdout.
	last := cc.BuildPlan("latest", false)[1]
	if !last.Quiet {
		t.Error("verify (docker image inspect) should be Quiet")
	}
}

func TestDeployAndTestPlan(t *testing.T) {
	t.Parallel()
	cur := Target{Name: "cursor", Image: "proveo/cursor", DefDir: "/d/cursor",
		BuildScript: "/d/cursor/build.sh", TestScript: "/d/cursor/test.sh"}

	// Deploy promotes the tested build: it REQUIRES :local, retags it, then pushes.
	// Publishing without that inspect would ship an image nothing ran against.
	if got, want := argvs(cur.DeployPlan("v3")), []string{
		"docker image inspect proveo/cursor:local",
		"docker tag proveo/cursor:local proveo/cursor:v3",
		"bash /d/cursor/build.sh --tag v3 --push",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("DeployPlan = %v, want %v", got, want)
	}

	cc := Target{Name: "claudecode", Image: "proveo/claudecode", DefDir: "/d/claudecode",
		BuildScript: "/d/claudecode/build.sh", BuildArgs: []string{"--variant", "mcp"}}
	if got, want := argvs(cc.DeployPlan("")), []string{
		"docker image inspect proveo/claudecode:local",
		"docker tag proveo/claudecode:local proveo/claudecode:latest",
		"bash /d/claudecode/build.sh --variant mcp --tag latest --push",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("DeployPlan(claudecode) = %v, want %v", got, want)
	}
	// The require-and-promote steps are plumbing, not output.
	for i, c := range cc.DeployPlan("")[:2] {
		if !c.Quiet {
			t.Errorf("DeployPlan step %d (%v) should be Quiet", i, c.Argv)
		}
	}

	// TestPlan runs test.sh when it exists, else skips (nil).
	if got := cur.TestPlan(func(string) bool { return true }); len(got) != 1 || strings.Join(got[0].Argv, " ") != "bash /d/cursor/test.sh" {
		t.Errorf("TestPlan(exists) = %v", got)
	}
	if got := cur.TestPlan(func(string) bool { return false }); got != nil {
		t.Errorf("TestPlan(missing) = %v, want nil (skip)", got)
	}
}

// :latest and :local both existing is the normal state on a maintainer's machine.
// Existence alone cannot decide: a stale :local from last week must not shadow an
// image just pulled, and a build from a minute ago must not lose to the registry.
func TestResolveImagePrefersTheNewerBuild(t *testing.T) {
	t.Parallel()
	old := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	stamps := func(m map[string]time.Time) func(string) (time.Time, bool) {
		return func(ref string) (time.Time, bool) { ts, ok := m[ref]; return ts, ok }
	}

	cases := []struct {
		name      string
		ref       string
		have      map[string]time.Time
		want      string
		wantLocal bool
	}{
		{"local is newer", "proveo/cc:latest",
			map[string]time.Time{"proveo/cc:latest": old, "proveo/cc:local": recent},
			"proveo/cc:local", true},
		{"published is newer", "proveo/cc:latest",
			map[string]time.Time{"proveo/cc:latest": recent, "proveo/cc:local": old},
			"proveo/cc:latest", false},
		{"no local build", "proveo/cc:latest",
			map[string]time.Time{"proveo/cc:latest": old},
			"proveo/cc:latest", false},
		{"never built or pulled the published tag", "proveo/cc:latest",
			map[string]time.Time{"proveo/cc:local": old},
			"proveo/cc:local", true},
		{"untagged means latest", "proveo/cc",
			map[string]time.Time{"proveo/cc:local": recent},
			"proveo/cc:local", true},
		// An explicit tag or digest is a decision, not a default.
		{"explicit tag untouched", "proveo/cc:v2",
			map[string]time.Time{"proveo/cc:local": recent},
			"proveo/cc:v2", false},
		{"digest untouched", "proveo/cc@sha256:abc",
			map[string]time.Time{"proveo/cc:local": recent},
			"proveo/cc@sha256:abc", false},
		{"already local", "proveo/cc:local",
			map[string]time.Time{"proveo/cc:local": recent},
			"proveo/cc:local", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, isLocal := ResolveImage(c.ref, stamps(c.have))
			if got != c.want || isLocal != c.wantLocal {
				t.Errorf("ResolveImage(%q) = (%q,%v), want (%q,%v)", c.ref, got, isLocal, c.want, c.wantLocal)
			}
		})
	}
}
