// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package sbx

import (
	"strings"
	"testing"
)

func TestClonePreservationTargetsRefsThatOutliveTheRemote(t *testing.T) {
	t.Parallel()
	const name = "proveo-1788208363-50815"
	if got := CloneRemote(name); got != "sandbox-"+name {
		t.Errorf("remote = %q; sbx names the clone's remote sandbox-<name>", got)
	}
	refs := CloneRefs(name)
	if strings.HasPrefix(refs, "refs/remotes/") {
		t.Errorf("%s: refs/remotes/<remote>/* is deleted with the remote, which `sbx rm` removes — the fetch has to land somewhere that survives", refs)
	}
	if !strings.HasPrefix(refs, "refs/proveo/") || !strings.HasSuffix(refs, name) {
		t.Errorf("refs = %q, want refs/proveo/<name>", refs)
	}

	fetch := strings.Join(CloneFetchArgs("/home/op/repo", name), " ")
	for _, want := range []string{"-C /home/op/repo", "fetch", "--no-tags", CloneRemote(name), "+refs/heads/*:" + refs + "/*"} {
		if !strings.Contains(fetch, want) {
			t.Errorf("fetch argv %q lacks %q", fetch, want)
		}
	}
}

func TestCloneSnapshotCommitsOnlyWhenSomethingIsStaged(t *testing.T) {
	t.Parallel()
	args := CloneSnapshotArgs("sb", "/Users/op/repo")
	joined := strings.Join(args, " ")
	if args[0] != "exec" || !strings.Contains(joined, "-w /Users/op/repo") {
		t.Errorf("snapshot must exec inside the clone's workdir: %q", joined)
	}
	if !strings.Contains(joined, "git add -A") || !strings.Contains(joined, "git diff --cached --quiet ||") {
		t.Errorf("snapshot must stage everything and commit only when the index is not empty: %q", joined)
	}
	if !strings.Contains(joined, "user.name=proveo") {
		t.Errorf("a teardown commit must say who made it: %q", joined)
	}
}
