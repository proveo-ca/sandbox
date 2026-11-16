// SPEC: _spec/internal/sbx/state-sync.puml
package sbx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runSave runs args' guest script against a lib that defines each function in rc with that
// exit status; it returns the script's status and the functions it called, in order.
func runSave(t *testing.T, args []string, rc map[string]int) (int, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	var lib strings.Builder
	for fn, code := range rc {
		lib.WriteString(fn + "() { echo " + fn + "; return " + strconv.Itoa(code) + "; }\n")
	}
	path := filepath.Join(t.TempDir(), "entrypoint-lib.sh")
	if err := os.WriteFile(path, []byte(lib.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	script := args[len(args)-1]
	if !strings.HasPrefix(script, ". /entrypoint-lib.sh") {
		t.Fatalf("the save no longer sources the image's lib first: %q", script)
	}
	out, err := exec.Command("bash", "-c", ". "+path+strings.TrimPrefix(script, ". /entrypoint-lib.sh")).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), strings.Join(strings.Fields(string(out)), " ")
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, strings.Join(strings.Fields(string(out)), " ")
}

func TestTheSaveFailsWhenConfigOrStateDidNotReachTheHost(t *testing.T) {
	t.Parallel()
	all := "proveo_sync_config proveo_sync_tools proveo_sync_state"
	for _, c := range []struct {
		name                 string
		config, tools, state int
		want                 int
	}{
		{"every sync saved", 0, 0, 0, 0},
		{"the config save failed", 1, 0, 0, 1},
		{"the toolchain save failed", 0, 1, 0, 0},
		{"the state save failed", 0, 0, 1, 1},
	} {
		code, ran := runSave(t, SaveStateArgs("sb"), map[string]int{
			"proveo_sync_config": c.config, "proveo_sync_tools": c.tools, "proveo_sync_state": c.state})
		if code != c.want || ran != all {
			t.Errorf("%s: exit %d after %q, want exit %d after %q", c.name, code, ran, c.want, all)
		}
	}
}

func TestTheGuardedSaveSkipsAnImageOlderThanTheRestoreWait(t *testing.T) {
	t.Parallel()
	old := map[string]int{"proveo_sync_config": 0, "proveo_sync_tools": 0, "proveo_sync_state": 0}
	if code, ran := runSave(t, GuardedSaveStateArgs("sb"), old); code != SaveStateUnguarded || ran != "" {
		t.Errorf("an older lib: exit %d after %q, want exit %d and no sync", code, ran, SaveStateUnguarded)
	}
	if code, ran := runSave(t, SaveStateArgs("sb"), old); code != 0 || ran == "" {
		t.Errorf("the unguarded save on an older lib: exit %d after %q, want every sync run", code, ran)
	}
	current := map[string]int{"_proveo_await_restore": 0, "proveo_sync_config": 0, "proveo_sync_tools": 0, "proveo_sync_state": 1}
	if code, ran := runSave(t, GuardedSaveStateArgs("sb"), current); code != 1 || !strings.HasSuffix(ran, "proveo_sync_state") {
		t.Errorf("a current lib: exit %d after %q, want the save run and its failure", code, ran)
	}
}
