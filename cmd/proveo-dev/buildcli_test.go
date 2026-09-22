// SPEC: _spec/cmd/proveo/usage.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"testing"
)

func TestDevVersion(t *testing.T) {
	if got := devVersion("abc1234"); got != "dev@abc1234" {
		t.Errorf("%q", got)
	}
	if got := devVersion(""); got != "dev@unknown" {
		t.Errorf("%q", got)
	}
}

func TestInstallTargetsStampOnlyProveo(t *testing.T) {
	ts := installTargets("dev@x")
	if len(ts) != 3 || ts[0] != [2]string{"./cmd/proveo", "-s -w -X main.version=dev@x"} {
		t.Fatalf("%q", ts)
	}
	for _, tt := range ts[1:] {
		if tt[1] != "-s -w" {
			t.Errorf("%s stamped: %q", tt[0], tt[1])
		}
	}
	for _, tt := range ts {
		if tt[0] == "./cmd/proveo-dev" {
			t.Error("proveo-dev must not be installed as a shipped CLI")
		}
	}
}

func TestGoreleaserArgs(t *testing.T) {
	if got := goreleaserArgs("v1.0.0"); !slices.Equal(got, []string{"release", "--clean", "--skip=publish"}) {
		t.Errorf("tagged: %q", got)
	}
	if got := goreleaserArgs(""); !slices.Equal(got, []string{"release", "--snapshot", "--clean"}) {
		t.Errorf("untagged: %q", got)
	}
}

func TestWranglerArgs(t *testing.T) {
	if got := wranglerArgs("deploy"); !slices.Equal(got, []string{"exec", "wrangler", "deploy", "--cwd", "apps/cli"}) {
		t.Errorf("%q", got)
	}
}
