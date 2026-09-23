// SPEC: _spec/defs/agent-definition-sharing.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"strings"
	"testing"
)

func TestRenderArgsMountTheWorkingTreeAtItsHostPath(t *testing.T) {
	got := renderArgs("/r", "proveo/codex:local", "codex", "/d", 501, 20)
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"--user 501:20",
		"--entrypoint bash",
		"-v /r/defs/subagents:/r/defs/subagents:ro",
		"-v /r/packages/lib/entrypoint-lib.sh:/entrypoint-lib.sh:ro",
		"-v /d:/d",
		"-e PROVEO_SUBAGENTS_DIR=/r/defs/subagents",
		"--network none",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q:\n%s", want, joined)
		}
	}
	i := slices.Index(got, "proveo/codex:local")
	if i < 0 || !slices.Equal(got[i+1:], []string{"-c", renderScript, "render-subagents", "codex", "/d"}) {
		t.Errorf("command tail = %v", got[i:])
	}
	if !strings.Contains(renderScript, `render_subagents "$1" "$2" 1`) {
		t.Error("the preview must reseed (third arg 1), as the bash did")
	}
}
