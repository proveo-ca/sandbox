// SPEC: _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sbx keeps a template's ENTRYPOINT and passes its keepalive as the command, so
// proveo's entrypoint is PID 1 there; the dispatcher's seed is the only seeder.
func TestEntrypointPassesThroughSbxKeepalive(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	// sbx sets SANDBOX_VM_ID before PID 1 starts; /etc/durable-startup.d is
	// written about two seconds later, so it cannot be the marker.
	run := func(vmID string, args ...string) string {
		t.Helper()
		script := `source "$1"; shift; proveo_sbx_passthrough "$@"; echo SEEDED`
		cmd := exec.Command(bash, append([]string{"-c", script, "bash", lib}, args...)...)
		cmd.Env = append(cmd.Environ(), "SANDBOX_VM_ID="+vmID)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	keepalive := []string{"sh", "-c", "echo KEEPALIVE # trap 'kill -TERM -- -1; wait' TERM; sleep infinity & wait"}

	if out := run("proveo-opencode-0cae7b86", keepalive...); !strings.Contains(out, "KEEPALIVE") || strings.Contains(out, "SEEDED") {
		t.Errorf("under sbx the keepalive must be exec'd, not seeded:\n%s", out)
	}
	if out := run("", keepalive...); !strings.Contains(out, "SEEDED") {
		t.Errorf("without sbx's SANDBOX_VM_ID the entrypoint must run normally:\n%s", out)
	}
	for _, argv := range [][]string{{"opencode", "--version"}, {"sh", "-c", "echo agent"}, {}} {
		if out := run("proveo-opencode-0cae7b86", argv...); !strings.Contains(out, "SEEDED") {
			t.Errorf("argv %q is not sbx's keepalive and must run the entrypoint:\n%s", argv, out)
		}
	}
}

var libSourced = regexp.MustCompile(`(?s)source /entrypoint-lib\.sh\nfi\n(.*)`)

func TestHarnessEntrypointsPassThroughBeforeSeeding(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"defs/cecli/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
		"defs/codex/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
	} {
		m := libSourced.FindStringSubmatch(readRepoFile(t, rel))
		if m == nil {
			t.Errorf("%s no longer sources /entrypoint-lib.sh in the shape this guard reads", rel)
			continue
		}
		first, _, _ := strings.Cut(strings.TrimLeft(m[1], "\n"), "\n")
		if first != `proveo_sbx_passthrough "$@"` {
			t.Errorf("%s must call proveo_sbx_passthrough \"$@\" right after sourcing the lib, got %q", rel, first)
		}
	}
}
