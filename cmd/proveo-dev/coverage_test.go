// SPEC: _spec/tests/testing-strategy.puml
package main

import (
	"errors"
	"slices"
	"testing"
)

func TestCovPathsDefaultAndOverride(t *testing.T) {
	l := covPaths("/repo", "")
	if l.unit != "/repo/cov/unit" || l.merged != "/repo/cov/merged" || l.profile != "/repo/cov/coverage.out" {
		t.Fatalf("default layout: %+v", l)
	}
	if o := covPaths("/repo", "/tmp/c"); o.unit != "/tmp/c/unit" {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := coverage(t.TempDir(), l, c.args)
			ee, ok := errors.AsType[*exitError](err)
			if !ok || ee.code != c.code {
				t.Fatalf("got %v, want exit %d", err, c.code)
			}
		})
	}
}
