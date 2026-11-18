// SPEC: _spec/internal/cdn/distribution-update.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/cdn/release"
)

const goreleaserPin = "2.17.0"

// runs renders each run entry of task: the command, or "{ task = name }".
func runs(task *miseTable) []string {
	var out []string
	for _, r := range task.run() {
		out = append(out, r.String())
	}
	return out
}

func TestGoreleaserIsPinnedPerTask(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	if _, ok := cfg.tables["tools"].fields["goreleaser"]; ok {
		t.Error("[tools] pins goreleaser; pin it on the tasks that run it, so fmt, lint and post-commit never fetch it")
	}
	var runners, pinned []string
	for name, task := range cfg.tasks {
		if _, ok := task.fields["tools"].table["goreleaser"]; ok {
			pinned = append(pinned, name)
			if pin := task.fields["tools"].table["goreleaser"]; !pin.isStr || pin.str != goreleaserPin {
				t.Errorf("%s pins tools.goreleaser = %q, want %q", name, pin.text(), goreleaserPin)
			}
		}
		for _, cmd := range task.cmds() {
			if !strings.HasPrefix(cmd, "goreleaser ") {
				continue
			}
			runners = append(runners, name)
			if cmd != "goreleaser release --snapshot --clean" {
				t.Errorf("%s runs %q; a mise run string only snapshots, deploy --cli builds the release", name, cmd)
			}
		}
	}
	slices.Sort(runners)
	slices.Sort(pinned)
	if want := []string{"dev-cli", "test-images"}; !slices.Equal(runners, want) {
		t.Errorf("goreleaser runs in tasks %v, want %v", runners, want)
	}
	if want := []string{"deploy", "dev-cli", "test-images"}; !slices.Equal(pinned, want) {
		t.Errorf("tools.goreleaser is pinned on %v, want %v: deploy --cli runs it from internal/cdn/release", pinned, want)
	}
}

// allowBuildsFloor is the first mise whose allow_builds list runs only the scripts it names.
const allowBuildsFloor = "2026.10.17"

// calver splits a mise version such as 2026.10.17 into its numbers.
func calver(t *testing.T, v string) []int {
	t.Helper()
	var out []int
	for part := range strings.SplitSeq(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			t.Fatalf("mise version %q: %v", v, err)
		}
		out = append(out, n)
	}
	return out
}

func TestWranglerInstallsThroughMise(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	tools := cfg.tables["tools"].fields
	wrangler := tools["npm:wrangler"].table
	if wrangler == nil {
		t.Fatal(`[tools] pins no "npm:wrangler" table; deploy --cli and dev-cli run bare wrangler`)
	}
	var builds []string
	for _, item := range wrangler["allow_builds"].items {
		builds = append(builds, item.text())
	}
	if want := []string{"esbuild", "sharp", "workerd"}; !slices.Equal(builds, want) {
		t.Errorf("npm:wrangler allow_builds = %q, want the list %q", builds, want)
	}
	if _, ok := tools["node"]; !ok {
		t.Error("[tools] pins no node; esbuild's postinstall runs `node install.js`, so wrangler fails to install")
	}
	if got := cfg.top["min_version"].text(); slices.Compare(calver(t, got), calver(t, allowBuildsFloor)) < 0 {
		t.Errorf("min_version = %q, want >= %s: an older mise also runs aube's trusted-dependencies scripts", got, allowBuildsFloor)
	}
}

// contractTests lists the top-level Test functions internal/contract defines.
func contractTests(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "internal", "contract", "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	decl := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(t \*testing\.T\)`)
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(b), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestTheCLIReleaseRunsInsideDeploy(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	for _, gone := range []string{"release", "release-ledger", "deploy-cli", "build-cli", "snapshot", "stage-cdn"} {
		if _, ok := cfg.tasks[gone]; ok {
			t.Errorf("mise.toml declares %s; build --cli and deploy --cli replace it", gone)
		}
	}
	if c := strings.TrimSpace(cfg.task(t, "deploy").str("confirm")); c != "" {
		t.Errorf("deploy declares confirm %q; it would ask on every image deploy, and deploy --cli asks on its own", c)
	}
	for name, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			if strings.Contains(cmd, "wrangler deploy") || strings.Contains(cmd, "git push") || strings.Contains(cmd, "git tag") {
				t.Errorf("%s runs %q; deploy --cli alone publishes, tags and pushes, after the retirement ledger", name, cmd)
			}
		}
	}
	ledger := release.Plan("patch")[0]
	if !strings.HasPrefix(ledger, "PROVEO_RELEASE_GATE=1 go test ") || !strings.Contains(ledger, "-run=^TestNoRetirementOutlivesOneRelease$") {
		t.Errorf("deploy --cli starts with %q, want the gated retirement ledger", ledger)
	}
	if !slices.Contains(contractTests(t), "TestNoRetirementOutlivesOneRelease") {
		t.Error("internal/contract defines no TestNoRetirementOutlivesOneRelease; the ledger's -run would match nothing and pass")
	}
}

func TestCDNStagingFollowsAFreshSnapshot(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	fresh := []string{"goreleaser release --snapshot --clean", "go run ./internal/cdn/stage"}
	dev := runs(cfg.task(t, "dev-cli"))
	if want := append(slices.Clone(fresh), "wrangler dev --cwd apps/cli"); !slices.Equal(dev, want) {
		t.Errorf("dev-cli runs %q, want %q", dev, want)
	}
	images := cfg.task(t, "test-images")
	if got := runs(images); len(got) < 2 || !slices.Equal(got[:2], fresh) {
		t.Errorf("test-images runs %q, want it to start with %q", got, fresh)
	}
	if v := images.fields["env"].table["PROVEO_INSTALLTEST_CDN"]; !v.isStr || v.str == "" {
		t.Error("test-images' env table sets no PROVEO_INSTALLTEST_CDN; the installer suite would stage its own CDN")
	}
	if src := readRepoFile(t, "internal/cdn/stage/main.go"); !strings.Contains(src, "\npackage main\n") {
		t.Error("internal/cdn/stage/main.go is not a package main; go run cannot start it")
	}
}
