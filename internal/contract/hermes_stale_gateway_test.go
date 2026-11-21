// SPEC: _spec/defs/hermes/hermes-persistence.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAStaleHermesGatewaySocketIsClearedBeforeLaunch(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	for _, c := range []struct {
		name, pidns, cmdline string
		cleared              bool
	}{
		{"an earlier boot's gateway", "4026533011", "", true},
		{"no pidns recorded", "", "", true},
		{"this boot's live gateway", "4026530001", "/opt/hermes/hermes_cli/main.py gateway run", false},
		{"this boot, but the pid is something else", "4026530001", "/usr/bin/sleep infinity", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, proc := t.TempDir(), t.TempDir()
			pid := `{"pid": 475, "kind": "hermes-gateway", "pidns": "` + c.pidns + `"}`
			for name, body := range map[string]string{"gateway.sock": "", "gateway.pid": pid, "gateway.lock": pid} {
				if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(proc, "cmdline"), []byte(c.cmdline), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `source "$1/packages/lib/entrypoint-lib.sh"
_proveo_pidns() { echo 4026530001; }
grep() { if [[ "$3" == /proc/475/cmdline ]]; then command grep "$1" "$2" "$TEST_PROC/cmdline"; else command grep "$@"; fi; }
proveo_clear_stale_hermes_gateway "$TEST_HOME"`
			cmd := exec.Command(bashOrSkip(t), "-c", script, "bash", repoRoot(t))
			cmd.Env = append(os.Environ(), "BASH_ENV=", "TEST_HOME="+home, "TEST_PROC="+proc)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("clear failed: %v\n%s", err, out)
			}
			for _, name := range []string{"gateway.sock", "gateway.pid", "gateway.lock"} {
				_, statErr := os.Stat(filepath.Join(home, name))
				if gone := os.IsNotExist(statErr); gone != c.cleared {
					t.Errorf("%s gone = %v, want %v (output %q)", name, gone, c.cleared, strings.TrimSpace(string(out)))
				}
			}
		})
	}
}
