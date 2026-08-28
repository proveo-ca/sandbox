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

// codex is rostered separately because it is the one harness whose agent
// definitions are not markdown-with-frontmatter: it declares each agent as a TOML
// document, so the shared body arrives as a developer_instructions VALUE rather
// than as the text after a --- delimiter. Same bodies, same composition step,
// different container — which is exactly the split _frontmatter/ exists to hold.
var codexSubagentRoster = map[string]bool{
	"adversarial-reviewer": true,
	"architect":            true,
	"monorepo-coordinator": true,
	"security-reviewer":    true,
	"spec-keeper":          true,
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
	harnesses := []string{"codex"}
	for harness := range subagentRoster {
		harnesses = append(harnesses, harness)
	}
	for _, harness := range harnesses {
		entries, err := os.ReadDir(filepath.Join(root, "defs/subagents/_frontmatter", harness))
		if err != nil {
			t.Fatalf("roster for %s: %v", harness, err)
		}
		for _, e := range entries {
			name := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".toml")
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
		"defs/codex/Dockerfile",
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

// composedTomlFiles is the codex counterpart of composedFiles: its definitions are
// TOML documents, so nothing under the composed dir ends in .md.
func composedTomlFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
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

// The read-only split is the whole point of the advisor set, and codex expresses
// it as sandbox_mode rather than as a tools allowlist: it has no per-tool
// permission surface, so the sandbox IS the control. spec-keeper is the single
// writer, exactly as it is for claudecode.
func TestComposedCodexSubagents(t *testing.T) {
	t.Parallel()
	files := composedTomlFiles(t, composeSubagents(t, "codex"))
	if len(files) != len(codexSubagentRoster) {
		t.Errorf("codex composed %d definitions, want %d", len(files), len(codexSubagentRoster))
	}
	for file, src := range files {
		name := strings.TrimSuffix(file, ".toml")
		if !codexSubagentRoster[name] {
			t.Errorf("unexpected codex subagent %q", file)
		}
		if strings.Contains(src, "{{") {
			t.Errorf("codex/%s has an unresolved {{token}}", file)
		}
		// The shared body must arrive as a VALUE, in a LITERAL string: a basic
		// string would process the backslash sequences the bodies document, which
		// is a silent corruption in a file nobody re-reads after composing.
		lit := "developer_instructions = " + strings.Repeat("'", 3)
		if !strings.Contains(src, lit) {
			t.Errorf("codex/%s must carry the shared body as a literal developer_instructions", file)
		}
		if !strings.HasSuffix(strings.TrimRight(src, "\n"), strings.Repeat("'", 3)) {
			t.Errorf("codex/%s does not terminate its developer_instructions literal", file)
		}
		if strings.Count(src, strings.Repeat("'", 3)) != 2 {
			t.Errorf("codex/%s: a body containing the literal delimiter must be skipped, not emitted", file)
		}
		want := "read-only"
		if name == "spec-keeper" {
			want = "workspace-write"
		}
		if !strings.Contains(src, `sandbox_mode = "`+want+`"`) {
			t.Errorf("codex/%s must declare sandbox_mode = %q", file, want)
		}
	}
}
