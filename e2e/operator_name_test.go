//go:build e2e

// SPEC: _spec/cmd/proveo/operator-name.puml

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/operator"
)

// TestOperatorNameReachesTheHouseRules stores a name the way `proveo init` does
// and reads, inside a real sandbox, the instructions each harness's agent gets.
func TestOperatorNameReachesTheHouseRules(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	rules := map[string]string{
		"claudecode": "/etc/claude-code/CLAUDE.md",
		"opencode":   "$HOME/.config/opencode/AGENTS.md",
	}
	proveoBin := buildProveo(t)
	for _, target := range []string{"claudecode", "opencode"} {
		t.Run(target, func(t *testing.T) {
			harnessImage(t, target)
			sweepSandboxesAfter(t)
			home := t.TempDir()
			if _, err := operator.Save(home, "Roberto von Schoettler"); err != nil {
				t.Fatal(err)
			}
			sess := launchShellEnv(t, proveoBin, target, t.TempDir(), []string{"PROVEO_HOME=" + home})
			out, status := shellExec(t, sess, `grep -h 'Address me as' `+rules[target], 60*time.Second)
			if status != 0 || !strings.Contains(out, `Address me as "Roberto von Scho"`) {
				t.Fatalf("%s house rules do not address the stored name (status %d):\n%s", target, status, out)
			}
			if strings.Contains(out, `Address me as "Executor"`) {
				t.Errorf("%s still carries the default next to the stored name:\n%s", target, out)
			}
		})
	}
}
