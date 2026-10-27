// SPEC: _spec/tests/20-contract.puml, _spec/_experiments/sbx-kit-capabilities.puml, _spec/_paradigms/retire-dind.puml
package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func TestEmbeddedManifestsLoad(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	targets, err := manifest.Targets(ms)
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	for _, name := range []string{"cursor", "opencode", "cecli", "claudecode", "codex"} {
		img, ok := targets[name]
		if !ok {
			t.Errorf("missing target %q in embedded manifests", name)
			continue
		}
		if !strings.HasPrefix(img, "proveo/") {
			t.Errorf("target %q image = %q, want proveo/*", name, img)
		}
	}
	for _, m := range ms {
		if !m.Home.Active() {
			t.Errorf("harness %q must declare home.mounts — proveo session persistence AND "+
				"the durability of on-demand toolchains both depend on it", m.Name)
			continue
		}
		for _, hm := range m.Home.Mounts {
			if hm.Container != "" && !strings.HasPrefix(hm.Container, "/proveo-home/") {
				t.Errorf("%s home mount container %q must be under /proveo-home/, or absent for a direct bind", m.Name, hm.Container)
			}
		}
	}
}

func TestEntrypointsRunTheShellPreludeThenPrep(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"defs/codex/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/cecli/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
		"defs/hermes/entrypoint.sh",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		src := string(b)
		prep := "\nproveo-entrypoint prep || true\n"
		at := strings.Index(src, prep)
		if at < 0 {
			t.Errorf("%s must run %q unconditionally", rel, strings.TrimSpace(prep))
			continue
		}
		for _, step := range []string{"set_working_directory", "bridge_google_keys", "bridge_git_identity"} {
			calls := regexp.MustCompile(`(?m)^`+step+`\b`).FindAllStringIndex(src, -1)
			if len(calls) != 1 || calls[0][0] > at {
				t.Errorf("%s must call %s once, unindented, before prep; found %d such calls", rel, step, len(calls))
			}
		}
		for _, fallback := range []string{"ensure_runtime_user", "report_git_context", "! command -v proveo-entrypoint",
			"PROVEO_SMOKE_TEST= ", "export PROVEO_SMOKE_TARGET="} {
			if strings.Contains(src, fallback) {
				t.Errorf("%s carries %q, a bash copy of prep", rel, fallback)
			}
		}
		if strings.Contains(src, "gosu") {
			t.Errorf("%s must never escalate via gosu", rel)
		}
	}
}

func TestPrepIsTheOnlyRuntimeUserAndGitContextStep(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	lib := readFileOrFail(t, filepath.Join(root, "packages/lib/entrypoint-lib.sh"))
	for _, fn := range []string{"ensure_runtime_user", "report_git_context"} {
		if regexp.MustCompile(`(?m)^` + fn + `\(\)`).MatchString(lib) {
			t.Errorf("entrypoint-lib.sh still defines %s; proveo-entrypoint prep owns it", fn)
		}
	}
	hermes := readFileOrFail(t, filepath.Join(root, "defs/hermes/Dockerfile"))
	if !regexp.MustCompile(`(?m)^COPY .*--from=proveo-entrypoint-builder /out/proveo-entrypoint .*/usr/local/bin/`).MatchString(hermes) {
		t.Error("defs/hermes/Dockerfile must ship proveo-entrypoint, or hermes has no prep to run")
	}
}

var dockerfileEntrypoint = regexp.MustCompile(`(?m)^ENTRYPOINT\s+(\[.*\])\s*$`)

func TestOwnAgentDefsDeclareAnImageEntrypoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	var own []manifest.Manifest
	for _, m := range ms {
		if _, published, _ := sbx.UpstreamKitFor(m.Name, sbx.UpstreamOptions{}); !published {
			own = append(own, m)
		}
	}
	if len(own) == 0 {
		t.Fatal("no def takes the ownAgent path — this test would assert nothing")
	}
	for _, m := range own {
		t.Run(m.Name, func(t *testing.T) {
			t.Parallel()
			files := dockerfilesFor(t, root, m.Name)
			if len(files) == 0 {
				t.Fatalf("def %q ships no Dockerfile under defs/%s", m.Name, m.Name)
			}
			for _, f := range files {
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatalf("read %s: %v", f, err)
				}
				mm := dockerfileEntrypoint.FindStringSubmatch(string(b))
				if mm == nil {
					t.Errorf("%s declares no exec-form ENTRYPOINT, so a sandbox Kit for %q "+
						"has no binary and sbx opens a shell", f, m.Name)
					continue
				}
				var ep []string
				if err := json.Unmarshal([]byte(mm[1]), &ep); err != nil {
					t.Errorf("%s ENTRYPOINT %s is not a JSON array: %v", f, mm[1], err)
					continue
				}
				if len(ep) == 0 {
					t.Errorf("%s declares an EMPTY ENTRYPOINT, which is the same defect as none", f)
				}
			}
		})
	}
}

// dockerfilesFor finds a def's Dockerfiles: at the def root, or one level down
// where a def keeps its variants (defs/claudecode/mcp, defs/claudecode/solidity).
func dockerfilesFor(t *testing.T, root, def string) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{
		filepath.Join(root, "defs", def, "Dockerfile"),
		filepath.Join(root, "defs", def, "*", "Dockerfile"),
	} {
		hits, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		out = append(out, hits...)
	}
	sort.Strings(out)
	return out
}

func TestProviderCursorPin(t *testing.T) {
	t.Parallel()
	got := provider.Detect(func(k string) string {
		if k == "CURSOR_API_KEY" {
			return "sk"
		}
		return ""
	})
	if len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("Detect(CURSOR_API_KEY) = %v, want [cursor]", got)
	}
	entry, ok := provider.Lookup("cursor")
	if !ok {
		t.Fatal("provider cursor missing")
	}
	if !slices.Contains(entry.Hosts, ".cursor.sh") || !slices.Contains(entry.Hosts, ".cursor.com") {
		t.Errorf("cursor hosts = %v, want .cursor.sh and .cursor.com", entry.Hosts)
	}
	r, ok := provider.Resolve("cursor", func(k string) string {
		if k == "CURSOR_API_KEY" {
			return "sk"
		}
		return ""
	})
	if !ok || r.Value == "" {
		t.Fatalf("Resolve(cursor) = %+v ok=%v", r, ok)
	}
}

func TestCursorManifestDeclaresAPIKey(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	var cursor *manifest.Manifest
	for i := range ms {
		if ms[i].Name == "cursor" {
			cursor = &ms[i]
			break
		}
	}
	if cursor == nil {
		t.Fatal("cursor manifest missing from embed")
	}
	found := false
	for _, e := range cursor.Env {
		if e.Name == "CURSOR_API_KEY" && e.Secret {
			found = true
			break
		}
	}
	if !found {
		t.Error("cursor manifest must declare CURSOR_API_KEY as secret")
	}
}

func TestSubscriptionHarnesses(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"claudecode": "CLAUDE_CODE_OAUTH_TOKEN",
		// codex authenticates either way: a ChatGPT-plan login persisted in the
		// proveo home, or this key. The manifest declares the key because that is
		// the half proveo can broker; the login half is credentials.PersistedLogin.
		"codex":    "OPENAI_API_KEY",
		"cursor":   "CURSOR_API_KEY",
		"opencode": "OPENCODE_API_KEY",
	}
	found := map[string]bool{}
	for _, m := range ms {
		if !m.Subscription {
			continue
		}
		found[m.Name] = true
		envName, ok := want[m.Name]
		if !ok {
			t.Errorf("unexpected subscription harness %q", m.Name)
			continue
		}
		has := false
		for _, e := range m.Env {
			if e.Name == envName && e.Secret {
				has = true
				break
			}
		}
		if !has {
			t.Errorf("%s subscription harness must declare secret env %s", m.Name, envName)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("missing subscription harness %q", name)
		}
	}
}
