// SPEC: _spec/cmd/proveo/operator-name.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nameHermesOperator(t *testing.T, home, target string, env ...string) {
	t.Helper()
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_name_operator_hermes "$2"`, "bash", repoRoot(t), target)
	cmd.Env = append(os.Environ(), append([]string{"HERMES_HOME=" + home, "PROVEO_OPERATOR_NAME="}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proveo_name_operator_hermes: %v\n%s", err, out)
	}
}

func TestHermesLearnsTheOperatorsNameAndKeepsWhatItLearned(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	user := filepath.Join(home, "memories", "USER.md")
	if err := os.MkdirAll(filepath.Dir(user), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := "Likes terse answers.\n§\nPreferred name (set in proveo): Old — address the user as \"Old\", not by any account, host or path name.\n§\nWorks on proveo.\n"
	if err := os.WriteFile(user, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	nameHermesOperator(t, home, "hermes", "PROVEO_OPERATOR_NAME=Executor")
	b, _ := os.ReadFile(user)
	got := string(b)
	if !strings.HasPrefix(got, `Preferred name (set in proveo): Executor — address the user as "Executor"`) {
		t.Errorf("USER.md does not lead with the stored name:\n%s", got)
	}
	for _, kept := range []string{"Likes terse answers.", "Works on proveo."} {
		if !strings.Contains(got, kept) {
			t.Errorf("Hermes's own entry %q was lost:\n%s", kept, got)
		}
	}
	if strings.Contains(got, `"Old"`) || strings.Count(got, "Preferred name (set in proveo):") != 1 {
		t.Errorf("the previous proveo entry survived beside the new one:\n%s", got)
	}
	if strings.Count(got, "\n§\n") != 2 {
		t.Errorf("entries are not §-delimited as Hermes's MemoryStore reads them:\n%q", got)
	}
	nameHermesOperator(t, home, "hermes", "PROVEO_OPERATOR_NAME=Executor")
	if again, _ := os.ReadFile(user); string(again) != got {
		t.Errorf("a second seed changed USER.md (startup hooks re-run every boot)\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func TestHermesNamingSeedsAFreshProfileAndSkipsWithoutAName(t *testing.T) {
	t.Parallel()
	fresh := t.TempDir()
	nameHermesOperator(t, fresh, "hermes", "PROVEO_OPERATOR_NAME=Ada")
	if b, err := os.ReadFile(filepath.Join(fresh, "memories", "USER.md")); err != nil || !strings.HasPrefix(string(b), "Preferred name (set in proveo): Ada") {
		t.Errorf("no USER.md yet ⇒ one entry naming Ada, got %q (%v)", b, err)
	}
	for _, c := range []struct {
		target string
		env    []string
	}{{"hermes", nil}, {"claudecode", []string{"PROVEO_OPERATOR_NAME=Ada"}}} {
		home := t.TempDir()
		nameHermesOperator(t, home, c.target, c.env...)
		if _, err := os.Stat(filepath.Join(home, "memories")); err == nil {
			t.Errorf("%s %v wrote a Hermes profile", c.target, c.env)
		}
	}
}

func TestTheHermesSeedNamesTheOperatorAfterItRestoresConfig(t *testing.T) {
	t.Parallel()
	calls, _, _ := runSeedPhase(t, bashOrSkip(t), nil, "hermes", "release")
	restore, name := strings.Index(calls, "CALL proveo_sync_config restore"), strings.Index(calls, "CALL proveo_name_operator_hermes")
	if restore < 0 || name < restore {
		t.Errorf("restore at %d, naming at %d; the entry must land on the restored profile\n%s", restore, name, calls)
	}
}
