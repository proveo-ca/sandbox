// SPEC: _spec/cmd/proveo/usage.puml, _spec/cmd/proveo/provision-and-targets.puml
package main

import (
	"strings"
	"testing"
)

func TestRootHelpHidesMaintainerCommands(t *testing.T) {
	t.Parallel()
	root := rootCmd()
	var b strings.Builder
	renderHelp(&b, root)
	out := b.String()
	for _, hidden := range []string{"\n  build ", "\n  deploy ", "\n  test ", "\n  targets "} {
		if strings.Contains(out, hidden) {
			t.Errorf("root help lists %q:\n%s", strings.TrimSpace(hidden), out)
		}
	}
	var buildHidden bool
	var sawCursor bool
	for _, c := range root.Commands() {
		switch c.Name() {
		case "targets":
			t.Error("proveo targets is registered; nothing calls it")
		case "build":
			buildHidden = c.Hidden
		case "cursor":
			sawCursor = isHarnessCmd(c)
		}
	}
	if !buildHidden {
		t.Error("proveo build stays registered for mise, and root help omits it")
	}
	if !sawCursor {
		t.Error("proveo cursor is the alias for proveo run cursor")
	}
	if !strings.Contains(out, "\n  attach ") || !strings.Contains(out, "------ harnesses ------") {
		t.Errorf("root help lists attach and the harness aliases:\n%s", out)
	}
}
