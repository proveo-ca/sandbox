package maintain

import (
	"path/filepath"
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

	if got := byName["cecli"]; got.RepoRoot != "/" {
		t.Errorf("cecli RepoRoot = %q, want the defs dir's parent", got.RepoRoot)
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
	cc := Target{Name: "claudecode", Image: "proveo/claudecode", DefDir: "/d/claudecode", RepoRoot: "/"}

	// Default (latest): build in-process, then verify.
	got := argvs(cc.BuildPlan("latest", false))
	want := []string{
		"imagebuild claudecode --tag latest",
		"docker image inspect proveo/claudecode:latest",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(latest) = %v, want %v", got, want)
	}

	// Tagged + no-cache.
	got = argvs(cc.BuildPlan("v2", true))
	want = []string{
		"imagebuild claudecode --tag v2 --no-cache",
		"docker image inspect proveo/claudecode:v2",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(v2,no-cache) = %v, want %v", got, want)
	}

	cur := Target{Name: "cursor", Image: "proveo/cursor", DefDir: "/d/cursor", RepoRoot: "/"}
	got = argvs(cur.BuildPlan("", false))
	want = []string{
		"imagebuild cursor --tag local",
		"docker image inspect proveo/cursor:local",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BuildPlan(default) = %v, want %v", got, want)
	}

	// The build step runs in-process; the verify step discards stdout.
	plan := cc.BuildPlan("latest", false)
	if plan[0].Run == nil {
		t.Error("the build step must run in-process, not shell out")
	}
	if !plan[1].Quiet {
		t.Error("verify (docker image inspect) should be Quiet")
	}
}

func TestDeployAndTestPlan(t *testing.T) {
	t.Parallel()
	cur := Target{Name: "cursor", Image: "proveo/cursor", DefDir: "/d/cursor",
		RepoRoot: "/r", Suite: "cursor"}

	// Deploy promotes the tested build: it REQUIRES :local, retags it, then pushes.
	// Publishing without that inspect would ship an image nothing ran against.
	if got, want := argvs(cur.DeployPlan("v3")), []string{
		"docker image inspect proveo/cursor:local",
		"docker tag proveo/cursor:local proveo/cursor:v3",
		"imagebuild cursor --tag v3 --push",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("DeployPlan = %v, want %v", got, want)
	}

	cc := Target{Name: "claudecode", Image: "proveo/claudecode", DefDir: "/d/claudecode",
		RepoRoot: "/"}
	if got, want := argvs(cc.DeployPlan("")), []string{
		"docker image inspect proveo/claudecode:local",
		"docker tag proveo/claudecode:local proveo/claudecode:latest",
		"imagebuild claudecode --tag latest --push",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("DeployPlan(claudecode) = %v, want %v", got, want)
	}
	// The require-and-promote steps are plumbing, not output.
	for i, c := range cc.DeployPlan("")[:2] {
		if !c.Quiet {
			t.Errorf("DeployPlan step %d (%v) should be Quiet", i, c.Argv)
		}
	}

	// TestPlan runs the def's Go image suite when its file exists, else skips (nil).
	var asked string
	got := cur.TestPlan(func(p string) bool { asked = p; return true })
	if len(got) != 1 || got[0].Dir != "/r" ||
		strings.Join(got[0].Argv, " ") != "go test -tags=image -count=1 -v -run ^TestImageCursor$ ./internal/imagetest/" {
		t.Errorf("TestPlan(exists) = %+v", got)
	}
	if asked != "/r/internal/imagetest/cursor_test.go" {
		t.Errorf("TestPlan looked for %q", asked)
	}
	if got := cur.TestPlan(func(string) bool { return false }); got != nil {
		t.Errorf("TestPlan(missing) = %v, want nil (skip)", got)
	}
}

func TestScheduleBrowserVariantsFollowTheirHarness(t *testing.T) {
	t.Parallel()
	ts := []Target{
		{Name: "opencode", Kind: KindHarness},
		{Name: "opencode-browser", Kind: KindHarness},
		{Name: "cursor", Kind: KindHarness},
		{Name: "cursor-browser", Kind: KindHarness},
		{Name: "claudecode", Kind: KindHarness},
		{Name: "claudecode-browser", Kind: KindHarness},
		{Name: "codex", Kind: KindHarness},
		{Name: "codex-browser", Kind: KindHarness},
	}
	lines := make([]string, 0)
	for _, w := range Schedule(ts) {
		lines = append(lines, strings.Join(w.Names(), " "))
	}
	lineOf := func(name string) int {
		for i, line := range lines {
			for _, field := range strings.Fields(line) {
				if field == name {
					return i
				}
			}
		}
		return -1
	}
	for _, pair := range [][2]string{
		{"opencode", "opencode-browser"},
		{"cursor", "cursor-browser"},
		{"claudecode", "claudecode-browser"},
		{"codex", "codex-browser"},
	} {
		parent, child := lineOf(pair[0]), lineOf(pair[1])
		if parent < 0 || child < 0 || child <= parent {
			t.Errorf("%s wave %d, %s wave %d; the variant layers onto the harness\n%v",
				pair[0], parent, pair[1], child, lines)
		}
	}
}

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

func TestScheduleBasesStaySerialRestConcurrent(t *testing.T) {
	t.Parallel()
	ts := []Target{
		{Name: "base", Kind: KindBase},
		{Name: "base-node", Kind: KindBase},
		{Name: "base-node-lsp", Kind: KindBase},
		{Name: "base-node-browser", Kind: KindBase},
		{Name: "cecli", Kind: KindHarness},
		{Name: "claudecode", Kind: KindHarness},
		{Name: "claudecode-browser", Kind: KindHarness},
		{Name: "claudecode-solidity", Kind: KindHarness},
		{Name: "codex", Kind: KindHarness},
		{Name: "egress-proxy", Kind: KindSidecar},
		{Name: "mitmproxy", Kind: KindSidecar},
	}
	waves := Schedule(ts)
	if len(waves) < 5 {
		t.Fatalf("waves = %d, want at least 4 serial bases then a concurrent group", len(waves))
	}
	for i, name := range []string{"base", "base-node", "base-node-lsp", "base-node-browser"} {
		if waves[i].Concurrent || len(waves[i].Targets) != 1 || waves[i].Targets[0].Name != name {
			t.Fatalf("wave[%d] = %+v, want serial %s", i, waves[i], name)
		}
	}
	sawConcurrent := false
	for i, w := range waves {
		if w.Concurrent {
			sawConcurrent = true
		}
		if sawConcurrent {
			for _, tgt := range w.Targets {
				if tgt.Kind == KindBase {
					t.Errorf("wave[%d] concurrent group contains base %q", i, tgt.Name)
				}
			}
		}
	}
	if !sawConcurrent {
		t.Fatal("expected a concurrent group after the bases")
	}
	var printed strings.Builder
	for _, w := range waves {
		printed.WriteString(FormatWaveHeader(w) + "\n")
	}
	got := printed.String()
	idxConc := strings.Index(got, "# concurrent ")
	idxSol := strings.Index(got, "claudecode-solidity")
	if idxConc < 0 {
		t.Fatalf("print lacks concurrent group:\n%s", got)
	}
	for _, base := range []string{"# serial base\n", "# serial base-node\n", "# serial base-node-lsp\n", "# serial base-node-browser\n"} {
		idx := strings.Index(got, base)
		if idx < 0 {
			t.Errorf("print lacks %q", strings.TrimSpace(base))
			continue
		}
		if idx > idxConc {
			t.Errorf("%q appears after the concurrent group", strings.TrimSpace(base))
		}
	}
	if idxSol >= 0 && idxSol < idxConc {
		t.Errorf("claudecode-solidity is in a wave before claudecode finished:\n%s", got)
	}
}

func TestScheduleFleetPrintPutsConcurrentAfterBases(t *testing.T) {
	t.Parallel()
	defs := filepath.Join("..", "..", "defs")
	ms, err := manifest.Load(defs)
	if err != nil {
		t.Fatalf("manifest.Load(%s): %v", defs, err)
	}
	ts := Registry(ms, defs)
	waves := Schedule(ts)
	var printed strings.Builder
	for _, w := range waves {
		printed.WriteString(FormatWaveHeader(w) + "\n")
	}
	got := printed.String()
	idxConc := strings.Index(got, "# concurrent ")
	if idxConc < 0 {
		t.Fatalf("build-all print lacks concurrent groups:\n%s", got)
	}
	for _, line := range strings.Split(got[:idxConc], "\n") {
		if strings.HasPrefix(line, "# concurrent ") {
			t.Fatalf("concurrent group before bases:\n%s", got)
		}
	}
	after := got[idxConc:]
	if strings.Contains(after, "# serial base\n") || strings.Contains(after, "# serial base-node") {
		t.Fatalf("a base serial wave follows a concurrent group:\n%s", got)
	}
}

func TestFormatElapsedRefusesASummaryWithoutDuration(t *testing.T) {
	t.Parallel()
	if _, err := FormatTargetElapsed("opencode", nil); err == nil {
		t.Fatal("FormatTargetElapsed(nil) succeeded")
	}
	if _, err := FormatRunSummary("build", 16, nil); err == nil {
		t.Fatal("FormatRunSummary(nil wall) succeeded")
	}
	d := time.Second
	got, err := FormatTargetElapsed("opencode", &d)
	if err != nil || got != "opencode in 1s" {
		t.Errorf("FormatTargetElapsed = %q, %v", got, err)
	}
	sum, err := FormatRunSummary("build", 16, &d)
	if err != nil || sum != "build 16 target(s) in 1s" {
		t.Errorf("FormatRunSummary = %q, %v", sum, err)
	}
}
