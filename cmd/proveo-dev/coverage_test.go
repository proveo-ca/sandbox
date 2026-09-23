// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCovPathsDefaultAndOverride(t *testing.T) {
	l := covPaths("/repo", "")
	if l.unit != "/repo/cov/unit" || l.merged != "/repo/cov/merged" || l.profile != "/repo/cov/coverage.out" {
		t.Fatalf("default layout: %+v", l)
	}
	if o := covPaths("/repo", "/tmp/c"); o.unit != "/tmp/c/unit" || o.integration != "/tmp/c/integration" {
		t.Fatalf("override layout: %+v", o)
	}
}

func TestUnitArgs(t *testing.T) {
	want := []string{"test", "-race", "-cover", "-covermode=atomic", "./...", "-args", "-test.gocoverdir=/d"}
	if got := unitArgs(true, "/d"); !slices.Equal(got, want) {
		t.Errorf("race: %q", got)
	}
	if got := unitArgs(false, "/d"); slices.Contains(got, "-race") {
		t.Errorf("no-race run still passes -race: %q", got)
	}
}

func TestE2EArgsTimeoutAndForwarding(t *testing.T) {
	got := e2eArgs("", []string{"-run", "TestX"})
	want := []string{"test", "-tags=e2e", "./e2e/", "-count=1", "-timeout", "45m", "-run", "TestX"}
	if !slices.Equal(got, want) {
		t.Errorf("default: %q", got)
	}
	if got := e2eArgs("150m", nil); got[5] != "150m" {
		t.Errorf("PROVEO_TEST_GO_TIMEOUT ignored: %q", got)
	}
}

func TestIntegrationArgsForwarding(t *testing.T) {
	got := integrationArgs([]string{"-v"})
	if got[len(got)-1] != "-v" || !slices.Contains(got, "-tags=integration") || !slices.Contains(got, "./internal/egress/") {
		t.Errorf("integration argv: %q", got)
	}
}

func TestMergeInputsAddsNonEmptyIntegration(t *testing.T) {
	l := covPaths("", t.TempDir())
	if got := mergeInputs(l); got != l.unit {
		t.Errorf("absent integration dir merged: %q", got)
	}
	if err := os.MkdirAll(l.integration, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mergeInputs(l); got != l.unit {
		t.Errorf("empty integration dir merged: %q", got)
	}
	if err := os.WriteFile(filepath.Join(l.integration, "covmeta.x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mergeInputs(l); got != l.unit+","+l.integration {
		t.Errorf("non-empty integration dir not merged: %q", got)
	}
}

func TestCoverageExitCodes(t *testing.T) {
	l := covPaths("", t.TempDir())
	cases := []struct {
		name string
		args []string
		env  string
		code int
	}{
		{"unknown mode", []string{"bogus"}, "", 2},
		{"merge without unit data", []string{"merge"}, "", 1},
		{"integration without gate", []string{"integration"}, "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PROVEO_EGRESS_INTEGRATION", c.env)
			err := coverage(t.TempDir(), l, c.args)
			ee, ok := errors.AsType[*exitError](err)
			if !ok || ee.code != c.code {
				t.Fatalf("got %v, want exit %d", err, c.code)
			}
		})
	}
}
