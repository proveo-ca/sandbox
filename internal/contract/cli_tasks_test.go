// SPEC: _spec/internal/cdn/distribution-update.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
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

func subtask(name string) string { return miseRun{task: name}.String() }

func TestBuildCLIInstallsOnlyTheShippedBinaries(t *testing.T) {
	t.Parallel()
	task := readMise(t).task(t, "build-cli")
	want := []string{"go install -trimpath '-ldflags=-s -w' ./cmd/proveo ./cmd/proveo-entrypoint"}
	if got := runs(task); !slices.Equal(got, want) {
		t.Errorf("build-cli runs %q, want %q", got, want)
	}
	env := task.fields["env"].table
	if v := env["CGO_ENABLED"]; !v.isStr || v.str != "0" {
		t.Errorf("build-cli env CGO_ENABLED = %q, want the string \"0\"", v.text())
	}
	if v := env["GOBIN"]; v.isStr || v.bare != "false" {
		t.Errorf("build-cli env GOBIN = %q, want the bare false that unsets mise's GOBIN", v.text())
	}
	if strings.Contains(task.body, "-X") {
		t.Errorf("build-cli carries an -X stamp; cmd/proveo reads its version from Go build info:\n%s", task.body)
	}
}

func TestGoreleaserIsPinnedPerTask(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	if _, ok := cfg.tables["tools"].fields["goreleaser"]; ok {
		t.Error("[tools] pins goreleaser; pin it on the tasks that run it, so fmt, lint and post-commit never fetch it")
	}
	var runners, snapshots []string
	for name, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			if strings.Contains(cmd, "--snapshot") {
				snapshots = append(snapshots, name)
			}
			if !strings.HasPrefix(cmd, "goreleaser ") {
				continue
			}
			runners = append(runners, name)
			if pin := task.fields["tools"].table["goreleaser"]; !pin.isStr || pin.str != goreleaserPin {
				t.Errorf("%s runs %q with tools.goreleaser = %q, want %q", name, cmd, pin.text(), goreleaserPin)
			}
		}
	}
	slices.Sort(runners)
	if want := []string{"release", "snapshot"}; !slices.Equal(runners, want) {
		t.Errorf("goreleaser runs in tasks %v, want %v", runners, want)
	}
	if !slices.Equal(snapshots, []string{"snapshot"}) {
		t.Errorf("--snapshot appears in tasks %v, want snapshot alone", snapshots)
	}
}

var runFlag = regexp.MustCompile(`'-run=([^']*)'`)

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

func TestDeployCLIShipsOnlyATaggedRelease(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	deploy := cfg.task(t, "deploy-cli")
	if got := deploy.list("depends"); !slices.Equal(got, []string{"release"}) {
		t.Errorf("deploy-cli depends on %q, want [release]", got)
	}
	if strings.TrimSpace(deploy.str("confirm")) == "" {
		t.Error("deploy-cli declares no confirm; a deploy asks before wrangler publishes")
	}
	stagedCheck := "go test -count=1 '-run=^TestStagedCDNIsTheTaggedRelease$' ./internal/contract/"
	if got, want := runs(deploy), []string{stagedCheck, "pnpm exec wrangler deploy --cwd apps/cli"}; !slices.Equal(got, want) {
		t.Errorf("deploy-cli runs %q, want %q: the staged check sits in its own run so --skip-deps keeps it", got, want)
	}
	if v := deploy.fields["env"].table["PROVEO_RELEASE_GATE"]; !v.isStr || v.str != "1" {
		t.Errorf("deploy-cli env PROVEO_RELEASE_GATE = %q, want \"1\": without it the staged check skips", v.text())
	}
	if !slices.Contains(contractTests(t), "TestStagedCDNIsTheTaggedRelease") {
		t.Error("internal/contract defines no TestStagedCDNIsTheTaggedRelease; deploy-cli's -run would match nothing and pass")
	}

	release := cfg.task(t, "release")
	if got := release.list("depends"); !slices.Equal(got, []string{"release-ledger"}) {
		t.Errorf("release depends on %q, want [release-ledger]", got)
	}
	if got, want := runs(release), []string{"goreleaser release --clean --skip=publish", subtask("stage-cdn")}; !slices.Equal(got, want) {
		t.Errorf("release runs %q, want %q", got, want)
	}

	ledger := cfg.task(t, "release-ledger")
	if v := ledger.fields["env"].table["PROVEO_RELEASE_GATE"]; !v.isStr || v.str != "1" {
		t.Errorf("release-ledger env PROVEO_RELEASE_GATE = %q, want \"1\": without it a skip passes", v.text())
	}
	cmds := ledger.cmds()
	if len(cmds) != 1 || len(ledger.run()) != 1 || !strings.HasPrefix(cmds[0], "go test ") || !strings.HasSuffix(cmds[0], " ./internal/contract/") {
		t.Fatalf("release-ledger runs %q, want one go test of ./internal/contract/", cmds)
	}
	m := runFlag.FindStringSubmatch(cmds[0])
	if m == nil {
		t.Fatalf("release-ledger runs %q with no quoted -run=", cmds[0])
	}
	re, err := regexp.Compile(m[1])
	if err != nil {
		t.Fatalf("release-ledger's -run %q does not compile: %v", m[1], err)
	}
	defined := contractTests(t)
	for _, name := range []string{"TestNoRetirementOutlivesOneRelease", "TestReleaseShipsATaggedHead"} {
		if !re.MatchString(name) {
			t.Errorf("release-ledger's -run %q skips %s", m[1], name)
		}
		if !slices.Contains(defined, name) {
			t.Errorf("internal/contract defines no %s; release-ledger's -run would match nothing and pass", name)
		}
	}

	for name, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			if strings.Contains(cmd, "wrangler deploy") && name != "deploy-cli" {
				t.Errorf("%s runs %q; deploy-cli alone publishes, after release-ledger", name, cmd)
			}
		}
	}
}

func TestCDNStagingFollowsAFreshSnapshot(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	if got, want := runs(cfg.task(t, "snapshot")), []string{"goreleaser release --snapshot --clean"}; !slices.Equal(got, want) {
		t.Errorf("snapshot runs %q, want %q", got, want)
	}
	fresh := []string{subtask("snapshot"), subtask("stage-cdn")}
	dev := runs(cfg.task(t, "dev-cli"))
	if want := append(slices.Clone(fresh), "pnpm exec wrangler dev --cwd apps/cli"); !slices.Equal(dev, want) {
		t.Errorf("dev-cli runs %q, want %q", dev, want)
	}
	images := cfg.task(t, "test-images")
	if got := runs(images); len(got) < 2 || !slices.Equal(got[:2], fresh) {
		t.Errorf("test-images runs %q, want it to start with %q", got, fresh)
	}
	if v := images.fields["env"].table["PROVEO_INSTALLTEST_CDN"]; !v.isStr || v.str == "" {
		t.Error("test-images' env table sets no PROVEO_INSTALLTEST_CDN; the installer suite would stage its own CDN")
	}
	if got, want := runs(cfg.task(t, "stage-cdn")), []string{"go run ./internal/cdn/stage"}; !slices.Equal(got, want) {
		t.Errorf("stage-cdn runs %q, want %q", got, want)
	}
	if src := readRepoFile(t, "internal/cdn/stage/main.go"); !strings.Contains(src, "\npackage main\n") {
		t.Error("internal/cdn/stage/main.go is not a package main; go run cannot start it")
	}
}
