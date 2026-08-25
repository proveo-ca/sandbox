// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/provider"
)

func bridgeTable(t *testing.T) provider.BridgeTable {
	t.Helper()
	tab, err := provider.LoadBridges(proveo.ModelBridges)
	if err != nil {
		t.Fatalf("bridge tables: %v", err)
	}
	return tab
}

// defs/bridges/<harness>.tsv is the single declaration of how role vars become the
// env vars a harness reads. Two things consume it: the shell, at container start,
// and internal/provider, to show resolved slots in the prompt header. These tests
// run the shell applier and compare it against the Go reader, so the two consumers
// cannot disagree about the table they share.
var bridgeEntrypoint = map[string]string{
	"claudecode": "defs/claudecode/mcp/entrypoint.sh",
	"cecli":      "defs/cecli/entrypoint.sh",
	"cursor":     "defs/cursor/entrypoint.sh",
	"opencode":   "defs/opencode/entrypoint.sh",
}

func TestBridgeTablesParse(t *testing.T) {
	t.Parallel()
	tab := bridgeTable(t)
	for harness := range bridgeEntrypoint {
		rows, ok := tab[harness]
		if !ok {
			t.Errorf("%s has an entrypoint but no defs/bridges/%s.tsv", harness, harness)
			continue
		}
		if len(rows) == 0 {
			t.Errorf("%s declares an empty table", harness)
		}
		seen := map[string]bool{}
		for _, r := range rows {
			if r.Slot != "-" && seen[r.Slot] {
				t.Errorf("%s declares slot %q twice", harness, r.Slot)
			}
			seen[r.Slot] = true
		}
	}
}

// The shell applier and the Go reader must agree, or the header promises one model
// and the container uses another. Every role is set so no default fires: this
// isolates the fallback-chain and transform rules the two sides both implement.
func TestShellApplierMatchesGoReader(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	root := repoRoot(t)
	roles := provider.Roles{
		"ARCHITECT_MODEL": "anthropic/claude-opus-5",
		"EDITOR_MODEL":    "anthropic/claude-sonnet-4-6",
		"SMALL_MODEL":     "anthropic/claude-haiku-4-5",
	}
	tab := bridgeTable(t)

	for harness, rows := range tab {
		var targets []string
		for _, r := range rows {
			targets = append(targets, r.Targets...)
		}
		script := `set -e
source "$1/packages/lib/entrypoint-lib.sh"
export PROVEO_BRIDGES_DIR="$1/defs/bridges"
export ARCHITECT_MODEL=anthropic/claude-opus-5
export EDITOR_MODEL=anthropic/claude-sonnet-4-6
export SMALL_MODEL=anthropic/claude-haiku-4-5
apply_model_bridges "$2"
for v in $3; do printf '%s=%s\n' "$v" "$(printenv "$v" || true)"; done`
		out, err := exec.Command(bash, "-c", script, "bash", root, harness, strings.Join(targets, " ")).Output()
		if err != nil {
			t.Errorf("%s: shell applier failed: %v", harness, err)
			continue
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				got[k] = v
			}
		}

		for _, r := range rows {
			if r.Slot == "-" {
				continue // back-fill rows read a target, not a role
			}
			want := ""
			for _, role := range r.Roles {
				if v := roles[role]; v != "" {
					want = v
					break
				}
			}
			if want == "" {
				continue
			}
			switch r.Transform {
			case "bare":
				if i := strings.LastIndex(want, "/"); i >= 0 {
					want = want[i+1:]
				}
			}
			for _, target := range r.Targets {
				if got[target] != want {
					t.Errorf("%s slot %q: shell set %s=%q, table predicts %q",
						harness, r.Slot, target, got[target], want)
				}
			}
		}

		// And the header must name exactly the slots the table declares as visible.
		var visible int
		for _, r := range rows {
			if r.Slot != "-" {
				visible++
			}
		}
		if n := len(tab.EffectiveSlots(harness, roles)); n != visible {
			t.Errorf("%s: header shows %d slots, table declares %d visible", harness, n, visible)
		}
	}
}

// Once the table is the declaration, an entrypoint that still exports a target
// directly has quietly forked it again. The local-model override is exempt: it
// deliberately overrides every tier and is not a role bridge.
func TestEntrypointsDoNotHandRollBridges(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tab := bridgeTable(t)
	for harness, rel := range bridgeEntrypoint {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !strings.Contains(string(b), "apply_model_bridges "+harness) {
			t.Errorf("%s must call apply_model_bridges %s", rel, harness)
		}
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "export ") || strings.Contains(trimmed, "PROVEO_LOCAL_MODEL") {
				continue
			}
			for _, r := range tab[harness] {
				for _, target := range r.Targets {
					if strings.HasPrefix(trimmed, "export "+target+"=") {
						t.Errorf("%s hand-rolls %s; it is declared in defs/bridges/%s.tsv", rel, target, harness)
					}
				}
			}
		}
	}
}

// :latest means published. A --load build that writes it puts a local image and a
// registry artifact under one name, and anything that re-resolves the reference —
// sbx pulls it at sandbox creation — may serve the registry one over the build under
// test, silently. The guard belongs in the script every def's build.sh sources.
func TestLoadBuildsRefuseTheLatestTag(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "defs/lib/docker-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"proveo_docker_arg_tag",              // the tag is read out of the argv
		"refusing to --load an image tagged", // and a latest --load is refused
	} {
		if !strings.Contains(src, want) {
			t.Errorf("docker-build.sh must carry %q so a local build cannot write :latest", want)
		}
	}
}

// The two tags must stay distinct constants: collapsing them is the bug.
func TestLocalAndPublishTagsDiffer(t *testing.T) {
	t.Parallel()
	if maintain.LocalTag == maintain.PublishTag {
		t.Fatal("LocalTag and PublishTag must differ; sharing one name is the collision")
	}
	if maintain.RefTag("proveo/x") != maintain.PublishTag {
		t.Error("an untagged reference means the published tag")
	}
}

// The plan defaulted to :local while the CLI flag still defaulted to "latest", so
// normTag never saw an empty tag and every build asked for the published name. The
// flag default and the tag policy have to be the same fact, not two.
func TestBuildAndDeployFlagDefaultsMatchTheTagPolicy(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd/proveo/maintain_cmd.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, `"tag", "latest"`) {
		t.Error(`a hardcoded "latest" flag default bypasses the tag policy; use maintain.LocalTag / maintain.PublishTag`)
	}
	for _, want := range []string{"maintain.LocalTag", "maintain.PublishTag"} {
		if !strings.Contains(src, want) {
			t.Errorf("maintain_cmd.go must take its tag defaults from %s", want)
		}
	}
}

// Under sbx the workspace mounts at its own host path and /app holds nothing, so an
// entrypoint that cd's to /app unconditionally launched the agent in an empty
// directory — no git repo, and a trust dialog for a folder that is not the project.
// proveo-entrypoint chdirs correctly but cannot move its parent shell, so the shell
// has to honour PROVEO_WORKDIR itself.
func TestEntrypointLibHonoursProveoWorkdir(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "packages/lib/entrypoint-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	fn := src[strings.Index(src, "set_working_directory() {"):]
	fn = fn[:strings.Index(fn, "\n}\n")]
	if !strings.Contains(fn, "PROVEO_WORKDIR") {
		t.Error("set_working_directory must prefer PROVEO_WORKDIR over its default")
	}
	if !strings.Contains(src, "accept_workspace_trust") {
		t.Error("the lib must be able to pre-accept the workspace trust dialog")
	}
	// The operator's real ~/.claude.json is mounted in: it is merged, never rewritten.
	trust := src[strings.Index(src, "accept_workspace_trust() {"):]
	if !strings.Contains(trust, "JSON.parse") || !strings.Contains(trust, "Object.assign") {
		t.Error("accept_workspace_trust must merge the existing file, not overwrite it")
	}
}

// A blocking prompt is fatal to an unattended run, so the trust dialog must be
// cleared before the agent launches. It now happens inside proveo_seed, which both
// backends call — the sbx Kit from setup.startup, docker from the entrypoint.
func TestSeedRunsBeforeTheAgentLaunches(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "defs/claudecode/mcp/entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	seed, launch := strings.Index(src, "proveo_seed"), strings.Index(src, "exec claude")
	if seed < 0 {
		t.Fatal("the entrypoint must call proveo_seed")
	}
	if launch < 0 || seed > launch {
		t.Error("proveo_seed must run BEFORE the agent is exec'd, or its files arrive too late")
	}

	lib, err := os.ReadFile(filepath.Join(root, "packages/lib/entrypoint-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fn := string(lib)[strings.Index(string(lib), "proveo_seed() {"):]
	for _, want := range []string{"render_subagents", "accept_workspace_trust"} {
		if !strings.Contains(fn, want) {
			t.Errorf("proveo_seed must perform %s", want)
		}
	}
}
