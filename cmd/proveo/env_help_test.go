// SPEC: _spec/internal/envreg/env-registry.puml, _spec/cmd/proveo/usage.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envreg"
)

// envHelpRows maps each variable row of `help env` to its text, wrapped lines joined.
func envHelpRows(out string) map[string]string {
	rows := map[string]string{}
	name := ""
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "  PROVEO_"):
			f := strings.Fields(l)
			name = f[0]
			rows[name] = strings.Join(f[1:], " ")
		case name != "" && strings.HasPrefix(l, "    ") && strings.TrimSpace(l) != "" && !strings.Contains(l, "------"):
			rows[name] += " " + strings.TrimSpace(l)
		default:
			name = ""
		}
	}
	return rows
}

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

func TestHelpEnvNamesReadersOtherThanTheBinary(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	renderEnvHelp(&b)
	rows := envHelpRows(b.String())
	for _, v := range envreg.Vars {
		if v.Scope == envreg.Internal {
			continue
		}
		row, ok := rows[v.Name]
		if !ok {
			t.Errorf("help env has no row for %s", v.Name)
			continue
		}
		sandbox := strings.Contains(row, " in the sandbox")
		installer := strings.Contains(row, " by the install scripts")
		also := strings.Contains(row, "· also read ")
		if want := v.Where&envreg.Image != 0; sandbox != want {
			t.Errorf("%s (%s): row says read in the sandbox = %v, want %v: %q", v.Name, v.Where, sandbox, want, row)
		}
		if want := v.Where&envreg.Installer != 0; installer != want {
			t.Errorf("%s (%s): row says read by the install scripts = %v, want %v: %q", v.Name, v.Where, installer, want, row)
		}
		if want := v.Where != envreg.Binary && v.Where&envreg.Binary != 0; also != want {
			t.Errorf("%s (%s): row says also read = %v, want %v: %q", v.Name, v.Where, also, want, row)
		}
	}
	for _, c := range []struct {
		w    envreg.Where
		want string
	}{
		{envreg.Binary, ""},
		{envreg.Image, " · read in the sandbox"},
		{envreg.Installer, " · read by the install scripts"},
		{envreg.Binary | envreg.Image, " · also read in the sandbox"},
		{envreg.Binary | envreg.Installer, " · also read by the install scripts"},
	} {
		if got := readerNote(c.w); got != c.want {
			t.Errorf("readerNote(%s) = %q, want %q", c.w, got, c.want)
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
