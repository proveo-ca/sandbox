// SPEC: _spec/defs/claudecode/claudecode-paradigm.puml
package contract_test

import (
	"os/exec"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
)

// classicRenderer is the pair of switches that say "classic" across Claude Code
// versions: CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1 is the documented opt-out from
// 2.1.132, CLAUDE_CODE_NO_FLICKER=0 the older one.
var classicRenderer = map[string]string{
	"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "1",
	"CLAUDE_CODE_NO_FLICKER":               "0",
}

// The classic renderer is part of agent evidence: fullscreen draws on the
// alternate screen and restores it on exit, so a finished or failed run leaves
// nothing in scrollback, nothing for the transcript's tee, and an empty pane for a
// probe. The manifest is the declaration both backends read — sbx launches the
// agent through its own kit and never runs the image entrypoint, which is how an
// entrypoint-only default left sandboxed sessions in whatever renderer the saved
// `tui` setting named.
func TestClaudecodeManifestDefaultsTheClassicRenderer(t *testing.T) {
	t.Parallel()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	for _, m := range ms {
		if m.Name != "claudecode" {
			continue
		}
		for k, v := range classicRenderer {
			if got := m.AgentEnv[k]; got != v {
				t.Errorf("claudecode agentEnv %s = %q, want %q — without it the sbx backend never sees the switch", k, got, v)
			}
		}
		return
	}
	t.Fatal("no claudecode manifest embedded")
}

// The entrypoint repeats the same defaults for a bare `docker run` of the image,
// before it launches the CLI, and leaves an operator's own value alone.
func TestClaudecodeEntrypointDefaultsTheClassicRenderer(t *testing.T) {
	t.Parallel()
	ep := readRepoFile(t, "defs/claudecode/mcp/entrypoint.sh")
	launch := strings.Index(ep, "proveo_exec_agent claude")
	if launch < 0 {
		t.Fatal("claudecode entrypoint no longer launches through proveo_exec_agent claude")
	}
	exports := []string{
		`export CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN="${CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN:-1}"`,
		`export CLAUDE_CODE_NO_FLICKER="${CLAUDE_CODE_NO_FLICKER:-0}"`,
	}
	for _, want := range exports {
		at := strings.Index(ep, want)
		if at < 0 {
			t.Errorf("claudecode entrypoint lacks %q", want)
			continue
		}
		if at > launch {
			t.Errorf("%q is exported after the CLI is launched, so it never reaches it", want)
		}
	}

	// Behaviour, not just text: the default is a default. Run the same export
	// lines under bash with the operator's own value set and unset.
	bash := bashOrSkip(t)
	script := strings.Join(exports, "\n") + "\nprintf '%s %s' \"$CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN\" \"$CLAUDE_CODE_NO_FLICKER\""
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{nil, "1 0"},
		{[]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=0", "CLAUDE_CODE_NO_FLICKER=1"}, "0 1"},
	} {
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, tc.env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		if got := string(out); got != tc.want {
			t.Errorf("env %v: renderer switches = %q, want %q", tc.env, got, tc.want)
		}
	}
}
