// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamCodexConfig is what the codex kit's install hook writes in oauth mode.
const upstreamCodexConfig = `approval_policy = "never"
sandbox_mode = "danger-full-access"
mcp_oauth_credentials_store = "file"
forced_login_method = "api"

model_provider = "sandboxd"

[model_providers.sandboxd]
name = "Sandbox Proxy"
`

func mergeCodexDefaults(t *testing.T, codexHome string, env ...string) string {
	t.Helper()
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_merge_codex_defaults`, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), append([]string{"CODEX_HOME=" + codexHome, "CODEX_RESEED=",
		"PROVEO_CODEX_DEFAULTS=" + filepath.Join(repoRoot(t), "defs/codex/defaults")}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proveo_merge_codex_defaults: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProveosCodexDefaultsLeaveApprovalAndSandboxToTheKit(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/codex/defaults/config.toml"))
	for _, dup := range []string{"approval_policy", "sandbox_mode"} {
		if strings.Contains(src, dup+" =") {
			t.Errorf("defs/codex/defaults/config.toml sets %s; the codex kit's install hook owns it", dup)
		}
	}
}

func TestCodexDefaultsMergeAboveTheKitsFirstTable(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(upstreamCodexConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mergeCodexDefaults(t, home)
	table := strings.Index(got, "[model_providers.sandboxd]")
	for _, want := range []string{"hide_agent_reasoning = false", "project_doc_fallback_filenames = "} {
		at := strings.Index(got, want)
		if at < 0 || at > table {
			t.Errorf("%q at %d, table at %d; a key below a table header belongs to that table\n%s", want, at, table, got)
		}
	}
	for _, line := range strings.Split(upstreamCodexConfig, "\n") {
		if !strings.Contains(got, line) {
			t.Errorf("the merge dropped the kit's %q\n%s", line, got)
		}
	}
	if again := mergeCodexDefaults(t, home); again != got {
		t.Errorf("a second merge changed the file (startup hooks re-run every boot)\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func TestCodexDefaultsKeepTheOperatorsValueUnlessReseeded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("hide_agent_reasoning = true\nmodel = \"mine\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mergeCodexDefaults(t, home); !strings.Contains(got, "hide_agent_reasoning = true") || strings.Count(got, "hide_agent_reasoning") != 1 {
		t.Errorf("the merge overrode or duplicated the operator's key\n%s", got)
	}
	got := mergeCodexDefaults(t, home, "CODEX_RESEED=1")
	if !strings.Contains(got, "hide_agent_reasoning = false") || !strings.Contains(got, `model = "mine"`) {
		t.Errorf("CODEX_RESEED=1 must reset proveo's keys and keep the rest\n%s", got)
	}
}

func TestCodexDefaultsSeedAMissingConfig(t *testing.T) {
	t.Parallel()
	got := mergeCodexDefaults(t, filepath.Join(t.TempDir(), ".codex"))
	if !strings.Contains(got, "hide_agent_reasoning = false") {
		t.Errorf("no config.toml ⇒ the defaults seed it\n%s", got)
	}
}

func TestTheCodexSeedMergesDefaultsAfterItRestoresConfig(t *testing.T) {
	t.Parallel()
	calls, _, _ := runSeedPhase(t, bashOrSkip(t), nil, "codex", "release")
	restore, merge := strings.Index(calls, "CALL proveo_sync_config restore"), strings.Index(calls, "CALL proveo_merge_codex_defaults")
	if restore < 0 || merge < restore {
		t.Errorf("restore at %d, merge at %d; the merge must land on the restored config.toml\n%s", restore, merge, calls)
	}
}
