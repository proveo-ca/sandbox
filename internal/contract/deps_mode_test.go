// SPEC: _spec/packages/lib/dependency-trees.puml
package contract_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestHostAndImageReadPROVEODepsAlike(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	values := append([]string{"", " Shared ", "REINSTALL", "Auto", "0", "No", "DISABLED", "false",
		"on", "1", "sharde", "re install"}, workspace.DepsModes()...)
	for _, v := range values {
		want, wantKnown := workspace.DepsMode(v)
		cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; PROVEO_DEPS="$2" _proveo_deps_mode`,
			"bash", repoRoot(t), v)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("_proveo_deps_mode with PROVEO_DEPS=%q: %v\n%s", v, err, stderr.String())
		}
		got, gotKnown := strings.TrimSpace(stdout.String()), !strings.Contains(stderr.String(), "PROVEO_DEPS=")
		if got != want || gotKnown != wantKnown {
			t.Errorf("PROVEO_DEPS=%q: image reads %q (known %v), host reads %q (known %v)", v, got, gotKnown, want, wantKnown)
		}
	}
}
