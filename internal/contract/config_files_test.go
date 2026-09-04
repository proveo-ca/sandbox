// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
)

// docker binds the WHOLE proveo home, so a file at its root persists without
// anyone naming it. sbx copies a named, DIRECTORY-shaped set instead — so
// claudecode's ~/.claude.json (accepted workspace trust, Chrome onboarding, the
// operator's own MCP servers) and cecli's ~/.cecli.conf.yml were rebuilt from
// scratch on every sandbox open, silently.
func TestHarnessesDeclareTheirHomeRootConfigFiles(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	want := map[string]string{"claudecode": ".claude.json", "cecli": ".cecli.conf.yml"}
	for _, m := range ms {
		file, ok := want[m.Name]
		if !ok {
			continue
		}
		if got := proveohome.ConfigFiles(m.Home); !strings.Contains(got, file) {
			t.Errorf("%s: ConfigFiles = %q, want it to carry %q — the harness reads that "+
				"path and nothing else persists it on sbx", m.Name, got, file)
		}
	}
}

// A path would resolve against two different roots and land where nothing reads.
func TestHomeFilesMustBeBareNames(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"nested/file.json", "../escape", ".", "..", "a|b", ""} {
		m := manifest.Manifest{
			Name: "x", Images: map[string]string{"x": "x:latest"},
			Home: manifest.Home{Enabled: true, Files: []string{bad}},
		}
		if err := m.Validate(); err == nil {
			t.Errorf("Validate() accepted home file %q", bad)
		}
	}
}

// The round trip, end to end: a home-root file reaches the operator and comes
// back before the next run's agent reads it.
func TestConfigSyncCarriesHomeRootFiles(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	agent, state := t.TempDir(), t.TempDir()

	run := func(mode string) {
		t.Helper()
		script := `source "$1/packages/lib/entrypoint-lib.sh"
proveo_sync_config ` + mode + `
echo "rc=$?"`
		cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
		cmd.Env = append(os.Environ(), "PROVEO_CONFIG_SYNC=", "PROVEO_CONFIG_DIRS=",
			"HOME="+agent, "PROVEO_HOME=", "PROVEO_STATE_HOME="+state,
			"PROVEO_CONFIG_FILES=.claude.json;.cecli.conf.yml")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "rc=0") {
			t.Fatalf("proveo_sync_config %s: %v\n%s", mode, err, out)
		}
	}

	// A previous run left accepted workspace trust behind.
	if err := os.WriteFile(filepath.Join(state, ".claude.json"),
		[]byte(`{"projects":{"/app":{"hasTrustDialogAccepted":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run("restore")
	if b, err := os.ReadFile(filepath.Join(agent, ".claude.json")); err != nil ||
		!strings.Contains(string(b), "hasTrustDialogAccepted") {
		t.Fatalf("restore did not bring ~/.claude.json back: %v", err)
	}

	// This run wires Serena; teardown must carry the declaration out.
	if err := os.WriteFile(filepath.Join(agent, ".cecli.conf.yml"),
		[]byte("mcp-servers:\n  mcpServers:\n    serena: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("save")
	if b, err := os.ReadFile(filepath.Join(state, ".cecli.conf.yml")); err != nil ||
		!strings.Contains(string(b), "serena") {
		t.Fatalf("save did not carry ~/.cecli.conf.yml out: %v", err)
	}
}
