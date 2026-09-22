// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"
)

func TestParseDebugArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      []string
		want    debugArgs
		wantErr string
	}{
		{nil, debugArgs{Tag: "latest"}, ""},
		{[]string{"claudecode"}, debugArgs{Target: "claudecode", Tag: "latest"}, ""},
		{[]string{"--tag", "dev", "cursor"}, debugArgs{Target: "cursor", Tag: "dev"}, ""},
		{[]string{"cursor", "--print", "--", "--foo", "bar"}, debugArgs{Target: "cursor", Tag: "latest", Print: true, Extra: []string{"--foo", "bar"}}, ""},
		{[]string{"--tag", ""}, debugArgs{Tag: ""}, ""},
		{[]string{"--tag"}, debugArgs{}, "--tag requires a value"},
		{[]string{"--nope"}, debugArgs{}, "unknown debug option: --nope"},
		{[]string{"a", "b"}, debugArgs{}, "multiple debug targets specified: a and b"},
		{[]string{"-h"}, debugArgs{Tag: "latest", Help: true}, ""},
	}
	for _, c := range cases {
		got, err := parseDebugArgs(c.in)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("parseDebugArgs(%q) err = %v, want %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDebugArgs(%q) err = %v", c.in, err)
			continue
		}
		if got.Target != c.want.Target || got.Tag != c.want.Tag || got.Print != c.want.Print ||
			got.Help != c.want.Help || !slices.Equal(got.Extra, c.want.Extra) {
			t.Errorf("parseDebugArgs(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestDebugArgvPinsTheImageOnlyForANonLatestTag(t *testing.T) {
	t.Parallel()
	tgt := maintain.Target{Name: "claudecode", Image: "proveo/claudecode"}
	if got, want := debugArgv(tgt, "latest", nil), []string{"run", "claudecode", "--shell"}; !slices.Equal(got, want) {
		t.Errorf("latest: %q, want %q", got, want)
	}
	if got, want := debugArgv(tgt, "dev", []string{"-x"}), []string{"run", "claudecode", "--shell", "--image", "proveo/claudecode:dev", "-x"}; !slices.Equal(got, want) {
		t.Errorf("dev: %q, want %q", got, want)
	}
}

func TestFindProveoPrefersTheFirstDir(t *testing.T) {
	t.Parallel()
	have := map[string]bool{"/gopath/bin/proveo": true, "/usr/local/bin/proveo": true}
	isExec := func(p string) bool { return have[p] }
	if got := findProveo([]string{"/gopath/bin", "/usr/local/bin"}, isExec); got != "/gopath/bin/proveo" {
		t.Errorf("got %q", got)
	}
	if got := findProveo([]string{"", "/nope", "/usr/local/bin"}, isExec); got != "/usr/local/bin/proveo" {
		t.Errorf("got %q", got)
	}
	if got := findProveo([]string{"/nope"}, isExec); got != "" {
		t.Errorf("got %q, want go run fallback", got)
	}
}

func TestDevRegistryListsTheHarnessDefs(t *testing.T) {
	t.Parallel()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := loadDevRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claudecode", "cursor", "base", "egress-proxy"} {
		if _, ok := lookupTarget(reg, name); !ok {
			t.Errorf("registry has no %s", name)
		}
	}
}
