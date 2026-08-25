// SPEC: _spec/defs/agent-definition-sharing.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Subagent definitions are never duplicated on disk. One body per agent lives in
// defs/subagents/, the frontmatter schema is per-harness, and the two halves are
// joined by render_subagents at container start. Nothing composed is committed, so
// these tests compose through the real shell function — the same one the entrypoints
// call — and assert on its output rather than on a generated file.
var subagentRoster = map[string]int{
	"claudecode": 5,
	"cecli":      10,
	"opencode":   10,
	"cursor":     2,
}

// composeSubagents runs render_subagents for one harness into a temp dir and
// returns it. Shelling out is deliberate: a Go reimplementation could agree with
// itself while disagreeing with the image.
func composeSubagents(t *testing.T, harness string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	root := repoRoot(t)
	dest := t.TempDir()
	script := `set -e
source "$1/packages/lib/entrypoint-lib.sh"
export PROVEO_SUBAGENTS_DIR="$1/defs/subagents"
render_subagents "$2" "$3" 1`
	cmd := exec.Command(bash, "-c", script, "bash", root, harness, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render_subagents %s: %v\n%s", harness, err, out)
	}
	return dest
}

func composedFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func TestSubagentsComposeForEveryHarness(t *testing.T) {
	t.Parallel()
	for harness, want := range subagentRoster {
		files := composedFiles(t, composeSubagents(t, harness))
		if len(files) != want {
			t.Errorf("%s composed %d definitions, want %d", harness, len(files), want)
		}
		for name, src := range files {
			if strings.Contains(src, "{{") {
				t.Errorf("%s/%s has an unresolved {{token}}", harness, name)
			}
			if !strings.HasPrefix(src, "---\n") {
				t.Errorf("%s/%s does not open with frontmatter", harness, name)
			}
			if _, _, ok := strings.Cut(strings.TrimPrefix(src, "---\n"), "\n---"); !ok {
				t.Errorf("%s/%s frontmatter is unterminated", harness, name)
			}
		}
	}
}

// The read-only split is what stands between a reviewer and the working tree, and
// it comes from frontmatter that composition copies through verbatim. Asserting on
// the composed file rather than the yaml keeps the check on what actually ships.
func TestComposedClaudeCodeSubagentTools(t *testing.T) {
	t.Parallel()
	files := composedFiles(t, composeSubagents(t, "claudecode"))
	for name, src := range files {
		tools, ok := frontmatterField(src, "tools")
		if !ok {
			t.Errorf("%s must declare a tools: allowlist", name)
			continue
		}
		if strings.Contains(tools, "Bash") {
			t.Errorf("%s must not grant Bash; got tools: %s", name, tools)
		}
		if name == "spec-keeper.md" {
			for _, need := range []string{"Read", "Edit", "Write"} {
				if !strings.Contains(tools, need) {
					t.Errorf("spec-keeper must grant %s; got tools: %s", need, tools)
				}
			}
			continue
		}
		for _, banned := range []string{"Edit", "Write", "NotebookEdit"} {
			if strings.Contains(tools, banned) {
				t.Errorf("%s is a read-only advisor but grants %s; got tools: %s", name, banned, tools)
			}
		}
	}
}

func TestComposedCursorSubagentsReadonly(t *testing.T) {
	t.Parallel()
	for name, src := range composedFiles(t, composeSubagents(t, "cursor")) {
		if v, ok := frontmatterField(src, "readonly"); !ok || v != "true" {
			t.Errorf("%s must declare readonly: true; got %q", name, v)
		}
	}
}

// Sharing only pays if one edit reaches every harness: a body nothing references is
// dead weight, and a roster naming a body that does not exist fails at container
// start, where it is far more expensive to notice.
func TestSharedAgentBodiesAllReferenced(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	used := map[string]bool{}
	for harness := range subagentRoster {
		entries, err := os.ReadDir(filepath.Join(root, "defs/subagents/_frontmatter", harness))
		if err != nil {
			t.Fatalf("roster for %s: %v", harness, err)
		}
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".yaml")
			if name == e.Name() {
				continue
			}
			used[name] = true
			if _, err := os.Stat(filepath.Join(root, "defs/subagents", name+".md")); err != nil {
				t.Errorf("%s/%s names a body that does not exist", harness, e.Name())
			}
		}
		if _, err := os.Stat(filepath.Join(root, "defs/subagents/_vars", harness+".env")); err != nil {
			t.Errorf("%s has no token values: %v", harness, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "defs/subagents"))
	if err != nil {
		t.Fatal(err)
	}
	var orphans []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if name != e.Name() && !used[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("shared bodies referenced by no harness: %v", orphans)
	}
}

// Every image must actually carry the shared directory, or composition finds nothing
// and the run silently starts with no subagents at all.
func TestDockerfilesShipSharedSubagents(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"defs/claudecode/mcp/Dockerfile",
		"defs/cecli/Dockerfile",
		"defs/opencode/Dockerfile",
		"defs/cursor/Dockerfile",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !strings.Contains(string(b), "defs/subagents/ /opt/proveo/subagents/") {
			t.Errorf("%s must COPY defs/subagents/ into /opt/proveo/subagents/", rel)
		}
	}
}
