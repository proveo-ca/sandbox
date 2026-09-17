package sandbox

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// A failed run is kept for diagnosis and the next run over that workspace
// derives the same name, so proveo hands sbx a sandbox it cannot give
// workspaces to. The operator is one command from unblocked; sbx's own error
// does not say which, because it does not know proveo kept it.
// SPEC: _spec/internal/sbx/sandbox-backend.puml
func TestAKeptSandboxBlocksTheNextRunAndSaysHow(t *testing.T) {
	err := staleSandbox(
		sbx.RunConfig{Name: "proveo-cursor-177e7812", Mounts: []sbx.Mount{{Host: "/w"}}},
		func(string) bool { return true })
	if err == nil {
		t.Fatal("a run with workspaces met an existing sandbox and proceeded into sbx's own error")
	}
	if !strings.Contains(err.Error(), "proveo-cursor-177e7812") {
		t.Errorf("error does not name the sandbox: %v", err)
	}
}

func TestReattachingWithoutWorkspacesIsNotBlocked(t *testing.T) {
	if err := staleSandbox(sbx.RunConfig{Name: "proveo-cursor-177e7812"},
		func(string) bool { return true }); err != nil {
		t.Errorf("a run with no workspaces to hand over was refused: %v", err)
	}
}
