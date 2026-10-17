// SPEC: _spec/_paradigms/credential-boundary.puml
package run

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

func TestARenamedVariableReadsTheNewNameThenWarnsOnTheRetiredOne(t *testing.T) {
	var sb strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&sb)
	t.Cleanup(func() { ui.Default = prev })

	t.Setenv("PROVEO_TEST_NEW_NAME", "new")
	t.Setenv("PROVEO_TEST_OLD_NAME", "old")
	if got := renamedEnv("PROVEO_TEST_NEW_NAME", "PROVEO_TEST_OLD_NAME"); got != "new" {
		t.Errorf("both set: got %q, want the new name's value", got)
	}
	if sb.Len() != 0 {
		t.Errorf("the new name is set, yet a warning printed: %q", sb.String())
	}

	t.Setenv("PROVEO_TEST_NEW_NAME", "")
	for range 2 {
		if got := renamedEnv("PROVEO_TEST_NEW_NAME", "PROVEO_TEST_OLD_NAME"); got != "old" {
			t.Errorf("only the retired name set: got %q, want its value", got)
		}
	}
	if n := strings.Count(sb.String(), "PROVEO_TEST_OLD_NAME"); n != 1 {
		t.Errorf("want one warning naming the retired variable, got %d: %q", n, sb.String())
	}
	if !strings.Contains(sb.String(), "PROVEO_TEST_NEW_NAME") {
		t.Errorf("the warning must name the replacement: %q", sb.String())
	}
}

func TestStateRootTakesItsNewName(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_ROOT", "")
	t.Setenv("PROVEO_STATE_ROOT", "/state")
	if got := StateDir(); got != "/state" {
		t.Errorf("StateDir() = %q, want PROVEO_STATE_ROOT", got)
	}
}
