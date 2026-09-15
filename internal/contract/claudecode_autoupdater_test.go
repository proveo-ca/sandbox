// SPEC: _spec/defs/claudecode/claudecode-paradigm.puml
package contract_test

import (
	"os/exec"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
)

// The runtime user has no write access to the npm prefix, deliberately — a
// SUCCESSFUL update would silently replace the version the image was pinned
// at. Disabling the attempt, not the symptom, is the fix (see the plan).
func TestClaudecodeManifestDisablesTheAutoUpdater(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	for _, m := range ms {
		if m.Name != "claudecode" {
			continue
		}
		if got := m.AgentEnv["DISABLE_AUTOUPDATER"]; got != "1" {
			t.Errorf("claudecode agentEnv DISABLE_AUTOUPDATER = %q, want %q — without it every "+
				"run prints \"Auto-update failed\" and points an unattended session at an "+
				"interactive `claude doctor`", got, "1")
		}
		return
	}
	t.Fatal("no claudecode manifest embedded")
}

// The entrypoint repeats the default for a bare `docker run` of the image,
// before it launches the CLI, exactly as it already does for the classic
// renderer switches (TestClaudecodeEntrypointDefaultsTheClassicRenderer) —
// and leaves an operator's own value alone.
func TestClaudecodeEntrypointDisablesTheAutoUpdater(t *testing.T) {
	t.Parallel()
	ep := readRepoFile(t, "defs/claudecode/mcp/entrypoint.sh")
	launch := strings.Index(ep, "proveo_exec_agent claude")
	if launch < 0 {
		t.Fatal("claudecode entrypoint no longer launches through proveo_exec_agent claude")
	}
	const export = `export DISABLE_AUTOUPDATER="${DISABLE_AUTOUPDATER:-1}"`
	at := strings.Index(ep, export)
	if at < 0 {
		t.Fatalf("claudecode entrypoint lacks %q", export)
	}
	if at > launch {
		t.Errorf("%q is exported after the CLI is launched, so it never reaches it", export)
	}

	// Behaviour, not just text: the default is a default, and an operator's own
	// value survives it — same shape as the renderer switches' behavioural check.
	bash := bashOrSkip(t)
	script := export + "\nprintf '%s' \"$DISABLE_AUTOUPDATER\""
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{nil, "1"},
		{[]string{"DISABLE_AUTOUPDATER=0"}, "0"},
	} {
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, tc.env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		if got := string(out); got != tc.want {
			t.Errorf("env %v: DISABLE_AUTOUPDATER = %q, want %q", tc.env, got, tc.want)
		}
	}
}
