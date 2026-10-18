// SPEC: _spec/internal/envreg/env-registry.puml, _spec/cmd/proveo/usage.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envreg"
)

func TestHelpEnvListsEveryOperatorAndMaintainerVariable(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	renderEnvHelp(&b)
	out := b.String()
	for _, want := range []string{"------ operator ------", "------ maintainer ------"} {
		if !strings.Contains(out, want) {
			t.Errorf("help env lacks %q:\n%s", want, out)
		}
	}
	for _, v := range envreg.Vars {
		shown := strings.Contains(out, "\n  "+v.Name+" ")
		switch {
		case v.Scope == envreg.Internal && shown:
			t.Errorf("help env lists %s, which proveo sets itself", v.Name)
		case v.Scope != envreg.Internal && !shown:
			t.Errorf("help env omits %s (%s)", v.Name, v.Scope)
		}
	}
}

func TestProveoHelpEnvRendersTheRegistry(t *testing.T) {
	t.Parallel()
	root := rootCmd()
	var b strings.Builder
	root.SetOut(&b)
	root.SetArgs([]string{"help", "env"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "------ operator ------") {
		t.Errorf("`proveo help env` did not render the registry:\n%s", b.String())
	}
	var top strings.Builder
	renderHelp(&top, rootCmd())
	if !strings.Contains(top.String(), "\n  help env ") {
		t.Errorf("root help does not point at `proveo help env`:\n%s", top.String())
	}
}
