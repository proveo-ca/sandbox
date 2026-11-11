// SPEC: _spec/_devops/unit-baseline.puml
package main

import (
	"slices"
	"strings"
	"testing"
)

const baselineStream = `{"Action":"pass","Package":"github.com/proveo-ca/proveo/internal/ui","Test":"TestA"}
{"Action":"fail","Package":"github.com/proveo-ca/proveo/internal/envreg","Test":"TestVarsSortByScopeThenName"}
{"Action":"fail","Package":"github.com/proveo-ca/proveo/internal/envreg","Test":"TestVarsSortByScopeThenName/sub"}
{"Action":"fail","Package":"github.com/proveo-ca/proveo/internal/envreg"}
{"Action":"fail","Package":"github.com/proveo-ca/proveo/internal/broken"}
FAIL	github.com/proveo-ca/proveo/internal/broken [build failed]
{"Action":"output","Package":"github.com/proveo-ca/proveo/internal/ui","Output":"fail"}
`

func TestFailedTestsKeepsTopLevelFailuresAndTestlessPackages(t *testing.T) {
	got := failedTests(strings.NewReader(baselineStream))
	want := []string{"internal/broken " + packageFailure, "internal/envreg TestVarsSortByScopeThenName"}
	if !slices.Equal(got, want) {
		t.Errorf("failedTests = %v, want %v", got, want)
	}
}

func TestNewFailuresSplitsAddedFromFixed(t *testing.T) {
	added, fixed := newFailures([]string{"a T1", "b T2"}, []string{"a T1", "c T3"})
	if !slices.Equal(added, []string{"b T2"}) || !slices.Equal(fixed, []string{"c T3"}) {
		t.Errorf("added = %v, fixed = %v", added, fixed)
	}
}

func TestTheBaselineListIsSortedUniqueAndWellFormed(t *testing.T) {
	list := baselineList(unitBaseline)
	if len(list) == 0 {
		t.Fatal("unit-baseline.txt is empty; delete the gate instead of emptying the list")
	}
	if !slices.Equal(list, slices.Compact(slices.Clone(list))) {
		t.Errorf("duplicate rows in unit-baseline.txt")
	}
	for _, l := range list {
		pkg, test, ok := strings.Cut(l, " ")
		if !ok || strings.HasPrefix(pkg, modulePrefix) || !strings.HasPrefix(test, "Test") {
			t.Errorf("row %q: want `<package under the module> <TestName>`", l)
		}
	}
}

func TestBaselineArgsDefaultToTheWholeModule(t *testing.T) {
	if got := baselineArgs(true, nil); !slices.Equal(got, []string{"test", "-race", "-count=1", "-json", "./..."}) {
		t.Errorf("baselineArgs = %v", got)
	}
	if got := baselineArgs(false, []string{"./internal/ui/"}); !slices.Equal(got, []string{"test", "-count=1", "-json", "./internal/ui/"}) {
		t.Errorf("baselineArgs = %v", got)
	}
}
