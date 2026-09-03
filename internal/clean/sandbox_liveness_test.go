// SPEC: _spec/internal/clean/clean-lifecycle.puml, _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package clean

import (
	"strings"
	"testing"
)

// An sbx run has no egress sidecar and no dind, so before sandboxes were
// counted this gate read "nothing is running" on that backend every time. The
// toolchain tree it prunes now lives on the host and is mounted into the live
// sandbox over virtiofs, where replacing a directory's inode unlinks the guest's
// dentry permanently — the binaries vanish under a running agent.
func TestToolsPruneHeldBackByALiveSandbox(t *testing.T) {
	t.Parallel()
	inv := Inventory{
		ToolDirs:  []ToolDir{{Path: "/h/.proveo/toolchains/linux-arm64/.local/share/mise"}},
		Sandboxes: []string{"proveo-1787956302-22788"},
	}
	p := BuildPlan(inv, Options{Tools: true})
	if len(p.ToolDirs) != 0 {
		t.Errorf("pruned %v while a sandbox is running — that tree is mounted into it", p.ToolDirs)
	}
	if len(p.SkippedLive) != 1 || !strings.HasPrefix(p.SkippedLive[0], "tools ") {
		t.Errorf("SkippedLive = %v, want the tool dir reported as live", p.SkippedLive)
	}
}

// An unreadable listing is not evidence of quiet. For a destructive prune the
// safe direction is to hold back and say so.
func TestToolsPruneHeldBackWhenSandboxLivenessIsUnknown(t *testing.T) {
	t.Parallel()
	inv := Inventory{
		ToolDirs:         []ToolDir{{Path: "/h/.proveo/toolchains/linux-arm64/.local/bin"}},
		SandboxesUnknown: true,
	}
	if p := BuildPlan(inv, Options{Tools: true}); len(p.ToolDirs) != 0 {
		t.Errorf("pruned %v with sbx liveness undecidable", p.ToolDirs)
	}
}

// --force stays the documented escape hatch, and a quiet host still prunes.
func TestToolsPrunePolicy(t *testing.T) {
	t.Parallel()
	dir := ToolDir{Path: "/h/.proveo/toolchains/linux-arm64/.go"}

	live := Inventory{ToolDirs: []ToolDir{dir}, Sandboxes: []string{"proveo-1-2"}}
	if p := BuildPlan(live, Options{Tools: true, Force: true}); len(p.ToolDirs) != 1 {
		t.Errorf("--force did not override a live sandbox: %v", p.ToolDirs)
	}

	quiet := Inventory{ToolDirs: []ToolDir{dir}}
	if p := BuildPlan(quiet, Options{Tools: true}); len(p.ToolDirs) != 1 {
		t.Errorf("a host with nothing running must still prune: %v", p.ToolDirs)
	}
}
