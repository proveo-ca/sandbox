//go:build e2e

// SPEC: _spec/internal/run/run-spec.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestEscInTheScopePickerCancelsTheRun closes the monorepo scope picker on a real terminal.
func TestEscInTheScopePickerCancelsTheRun(t *testing.T) {
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "package.json"), []byte(`{"name":"mono","workspaces":["apps/*"]}`))
	mkdirAll(t, filepath.Join(work, "apps", "web"))
	writeFile(t, filepath.Join(work, "apps", "web", "package.json"), []byte(`{"name":"web"}`))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-scope-esc-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	cmd := append([]string{"env"}, childEnvArgsNoCredential(t)...)
	cmd = append(cmd,
		"PROVEO_WIZARD=on",
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"sh", "-c", proveoBin+" run "+target+" --input "+work+"; echo PROVEO_EXIT=$?; sleep 300",
	)
	if err := sess.Start(160, 40, cmd...); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	if screen, err := sess.WaitFor("scope>", durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute)); err != nil {
		t.Fatalf("the scope picker never opened: %v\n%s", err, tail(screen, 40))
	}
	for _, row := range []string{"<repo root>", "apps/web"} {
		if screen, _ := sess.Capture(); !strings.Contains(screen, row) {
			t.Errorf("the picker shows no %q row\n%s", row, tail(screen, 40))
		}
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err := sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("proveo kept running after Esc closed the scope picker: %v\n%s", err, tail(screen, 40))
	}
	if !strings.Contains(screen, "cancelled at the scope prompt") || !strings.Contains(screen, "PROVEO_EXIT=1") {
		t.Errorf("Esc did not cancel the run with status 1\n%s", tail(screen, 40))
	}
	if made := newSandboxes(before); canList && len(made) > 0 {
		t.Errorf("a cancelled run created %v", made)
	}
}
