// SPEC: _spec/cmd/proveo/usage.puml, _spec/cmd/proveo/provision-and-targets.puml
package main

import (
	"strings"
	"testing"
)

func TestRootHelpShowsBuildAndKeepsReleaseToolingHidden(t *testing.T) {
	t.Parallel()
	root := rootCmd()
	var b strings.Builder
	renderHelp(&b, root)
	out := b.String()
	if !strings.Contains(out, "\n  build ") {
		t.Errorf("root help does not list build, which run and provision messages name:\n%s", out)
	}
	for _, hidden := range []string{"\n  deploy ", "\n  test ", "\n  targets "} {
		if strings.Contains(out, hidden) {
			t.Errorf("root help lists %q:\n%s", strings.TrimSpace(hidden), out)
		}
	}
	for _, c := range root.Commands() {
		if c.Name() == "targets" {
			t.Error("proveo targets is registered; nothing calls it")
		}
	}
}
