// SPEC: _spec/tests/20-contract.puml
package contract_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
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
			if !strings.HasPrefix(hm.Container, "/proveo-home/") {
				t.Errorf("%s home mount container %q must be under /proveo-home/", m.Name, hm.Container)
			}
		}
	}
}

func TestRunnerHardeningBaseline(t *testing.T) {
	t.Parallel()
	got := strings.Join(runner.Hardening(runner.MinPidsBase), " ")
	for _, want := range []string{"--cap-drop=ALL", "--security-opt=no-new-privileges:true", fmt.Sprintf("--pids-limit=%d", runner.MinPidsBase)} {
		if !strings.Contains(got, want) {
			t.Errorf("Hardening(MinPidsBase) = %q, missing %q", got, want)
		}
	}
	argv := strings.Join(runner.DockerRunArgs(runner.Config{Image: "x", PidsLimit: runner.MinPidsBase}), " ")
	if !strings.Contains(argv, "--cap-drop=ALL") {
		t.Errorf("DockerRunArgs must always include cap-drop: %s", argv)
	}
	if !strings.Contains(argv, "--pids-limit=") {
		t.Errorf("DockerRunArgs must always include --pids-limit: %s", argv)
	}
	if runner.MinPidsBase < 512 {
		t.Errorf("MinPidsBase = %d, want >= 512", runner.MinPidsBase)
	}
	if runner.MinPidsBrowser < runner.MinPidsBase {
		t.Errorf("MinPidsBrowser = %d, want >= MinPidsBase %d", runner.MinPidsBrowser, runner.MinPidsBase)
	}
}

func TestRunShimsExecProveo(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, shim := range []string{"opencode", "cursor", "cecli", "claudecode", "codex"} {
		path := filepath.Join(root, "defs", shim, "run.sh")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		body := string(b)
		if !strings.Contains(body, `exec "$PROVEO_BIN" run`) {
			t.Errorf("%s must exec proveo run", path)
		}
		if strings.Contains(body, "bin/proveo") {
			t.Errorf("%s must not fall back to repo-local bin/proveo (use PATH / PROVEO_BIN)", path)
		}
		if strings.Contains(body, "--cap-drop=ALL") {
			t.Errorf("%s must not redeclare hardening (lives in internal/runner)", path)
		}
	}
}

func TestEntrypointsPreferProveoEntrypoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	paths := []string{
		"defs/opencode/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
		"defs/codex/entrypoint.sh",
	}
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if !strings.Contains(string(b), "proveo-entrypoint prep") {
			t.Errorf("%s must prefer proveo-entrypoint prep", path)
		}
		if strings.Contains(string(b), "gosu") {
			t.Errorf("%s must never escalate via gosu", path)
		}
	}
}

var dockerfileEntrypoint = regexp.MustCompile(`(?m)^ENTRYPOINT\s+(\[.*\])\s*$`)

// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
func TestOwnAgentDefsDeclareAnImageEntrypoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	var own []manifest.Manifest
	for _, m := range ms {
		if sbx.DeclaresOwnAgent(m.Name) {
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
	acl, ok := provider.ACLBody("cursor")
	if !ok {
		t.Fatal("ACLBody(cursor) missing")
	}
	if !strings.Contains(acl, ".cursor.sh") || !strings.Contains(acl, ".cursor.com") {
		t.Errorf("cursor ACL = %q, want .cursor.sh and .cursor.com", acl)
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

func TestBrokerSentinelConstant(t *testing.T) {
	t.Parallel()
	if entrypoint.DefaultSentinel != "proveo-brokered" {
		t.Errorf("DefaultSentinel = %q, want proveo-brokered", entrypoint.DefaultSentinel)
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
	if cursor.Docker != manifest.DockerSbx {
		t.Errorf("cursor docker = %q, want %q — cursor is incompatible with the sidecar", cursor.Docker, manifest.DockerSbx)
	}
	if cursor.Provider != "cursor" {
		t.Errorf("cursor manifest provider = %q, want cursor", cursor.Provider)
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

// TestEveryDaemonPromiseIsTheSandbox is what retiring the privileged sidecar
// leaves behind: one way to get a daemon, so a harness that promises one
// promises the sandbox.
// SPEC: _spec/_paradigms/retire-dind.puml
func TestEveryDaemonPromiseIsTheSandbox(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	promising := 0
	for _, m := range ms {
		if !m.WantsDocker() {
			continue
		}
		promising++
		if !m.IsSbx() {
			t.Errorf("%s declares docker: %q — the only daemon left is %q, and the privileged "+
				"sidecar it used to name is retired", m.Name, m.Docker, manifest.DockerSbx)
		}
	}
	if promising == 0 {
		t.Fatal("no def promises a daemon — this invariant has nothing to guard")
	}
}

// TestEveryHarnessRunsInTheSandbox pins the move itself: all four defs took
// `docker: sbx`, so no harness is left on the docker+egress path by
// declaration.
func TestEveryHarnessRunsInTheSandbox(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cecli": false, "opencode": false, "cursor": false, "claudecode": false, "codex": false}
	for _, m := range ms {
		if _, tracked := want[m.Name]; !tracked {
			continue
		}
		want[m.Name] = true
		if !m.IsSbx() {
			t.Errorf("%s docker = %q, want %q", m.Name, m.Docker, manifest.DockerSbx)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing harness %q in embedded manifests", name)
		}
	}
}

// TestRetiredDockerDindIsRefused pins the refusal rather than the silence.
func TestRetiredDockerDindIsRefused(t *testing.T) {
	t.Parallel()
	err := manifest.Manifest{
		Name:      "stale",
		Docker:    manifest.DockerMode("dind"),
		Images:    map[string]string{"stale": "proveo/stale:latest"},
		Workspace: manifest.Workspace{Layout: "app"},
	}.Validate()
	if err == nil {
		t.Fatal("docker: dind must be refused at load, not ignored")
	}
	for _, want := range []string{"retired", "docker: sbx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the repair is in the message, got: %v", want, err)
		}
	}
}

func TestSubscriptionHarnessesRunOnTheSandboxBackend(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"claudecode": true, "codex": true, "cursor": true}
	for _, m := range ms {
		if m.Subscription && !m.IsSbx() {
			t.Errorf("%s must set docker: sbx (subscription harnesses run on sbx with docker+egress fallback)", m.Name)
		}
	}
	// SPEC: _spec/_paradigms/retire-dind.puml
	for name := range want {
		found := false
		for _, m := range ms {
			if m.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("expected subscription harness %q in embedded manifests", name)
		}
	}
}

func TestCursorTakesDockerFromTheSandboxBackend(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name != "cursor" {
			continue
		}
		if !m.IsSbx() {
			t.Errorf("cursor must run docker through sbx alone, got docker = %q", m.Docker)
		}
	}
}

func TestManifestIgnoresUnknownTopLevelKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	def := filepath.Join(dir, "x")
	if err := os.MkdirAll(def, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestYAML := `name: x
description: future-key test
egress: true
docker: sbx
provider: cursor
future_flag: true
images:
  x: proveo/x:latest
  y: proveo/y:latest
workspace:
  layout: app
`
	if err := os.WriteFile(filepath.Join(def, "harness.manifest"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	ms, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("Load must not error on unknown top-level keys: %v", err)
	}
	if len(ms) != 1 || ms[0].Name != "x" {
		t.Fatalf("expected one manifest 'x', got %+v", ms)
	}
	if ms[0].Images["x"] != "proveo/x:latest" || ms[0].Images["y"] != "proveo/y:latest" {
		t.Errorf("images not parsed correctly: %+v", ms[0].Images)
	}
	if len(ms[0].Images) != 2 {
		t.Errorf("unknown top-level keys leaked as images: %+v", ms[0].Images)
	}
}
