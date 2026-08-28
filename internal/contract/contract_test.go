// SPEC: _spec/tests/20-contract.puml
//
// Package contract holds Layer 2 no-Docker contracts that used to live as
// grep-for-substring asserts in defs/tests/test_harness_contracts.sh. These
// tests execute (or load) the real Go sources of truth.
package contract_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
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
		"codex":  "OPENAI_API_KEY",
		"cursor": "CURSOR_API_KEY",
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

func TestOpenCodeManifestEnablesDind(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "opencode" && m.Docker != manifest.DockerDind {
			t.Errorf("opencode docker = %q, want %q", m.Docker, manifest.DockerDind)
		}
	}
}

// TestDockerModeIsOneChoicePerHarness pins the split the enum exists to make
// unrepresentable: the sidecar is for harnesses with no sandbox backend, the
// sandbox is for the ones that have it, and no harness ships both.
func TestDockerModeIsOneChoicePerHarness(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]manifest.DockerMode{
		"cecli":      manifest.DockerDind,
		"opencode":   manifest.DockerDind,
		"cursor":     manifest.DockerSbx,
		"claudecode": manifest.DockerSbx,
		"codex":      manifest.DockerSbx,
	}
	for _, m := range ms {
		w, tracked := want[m.Name]
		if !tracked {
			continue
		}
		if m.Docker != w {
			t.Errorf("%s docker = %q, want %q", m.Name, m.Docker, w)
		}
		if m.IsSbx() && m.IsDind() {
			t.Errorf("%s claims both daemons — the enum must make that unrepresentable", m.Name)
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
		if m.Subscription {
			if !m.IsSbx() {
				t.Errorf("%s must set docker: sbx (subscription harnesses run on sbx with docker+egress fallback)", m.Name)
			}
			continue
		}
		if m.IsSbx() {
			t.Errorf("%s must not set docker: sbx (egress layer reserved for non-subscription harnesses)", m.Name)
		}
	}
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

// The manifest parser (now Go — internal/manifest, replacing the retired Bash
// lib/manifest-enum.sh) must tolerate unknown/future top-level keys: only the
// images: block yields build targets, and a new field like future_flag must
// neither error nor leak in as an image.
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
docker: dind
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
