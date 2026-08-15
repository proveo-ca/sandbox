//go:build e2e

// SPEC: _spec/internal/workspace/subdir-scope-mounts.puml

package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// A subdir scope must still carry the repo's shared directories. _spec is the
// one that matters most here: agents are asked to read AND maintain the specs,
// and plantuml is on the floor precisely so they can verify what they edited —
// none of which works if _spec never reaches the container. The mount plan is
// unit-tested; this asserts the container can actually reach it.
func TestScopeCarriesRootSpecDir(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)

	for _, tc := range []struct {
		mode  string
		scope string
	}{
		{"root", ""},
		{"subproject", "apps"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			repo := newTempMonorepo(t)
			args := []string{"run", "--rm"}
			args = append(args, scopedMountArgs(t, target, repo, tc.scope)...)
			args = append(args, "-w", "/app", "--user", hostUIDGID(t),
				"--entrypoint", "bash", img, "-c", `
[ -d /app/_spec ] || { echo "MISSING_SPEC_DIR"; exit 1; }
grep -q x /app/_spec/c.puml || { echo "SPEC_UNREADABLE"; exit 1; }
printf 'edited by the agent\n' > /app/_spec/written.puml || { echo "SPEC_READONLY"; exit 1; }
rm -f /app/_spec/written.puml
[ -d /app/apps/web ] || { echo "MISSING_SCOPE"; exit 1; }
echo "SPEC_OK"`)

			out, err := exec.Command("docker", args...).CombinedOutput()
			s := string(out)
			if err != nil || !strings.Contains(s, "SPEC_OK") {
				t.Fatalf("%s scope cannot reach a writable _spec: %v\n%s", tc.mode, err, s)
			}
		})
	}
}

// The scope must still NARROW the tree — otherwise rootDirs would just be a
// roundabout way of mounting everything, and the git scoping in
// scope_git_worktree would have nothing to hide.
func TestSubdirScopeOmitsUnmountedPaths(t *testing.T) {
	const target = "opencode"
	img := harnessImage(t, target)
	repo := newTempMonorepo(t)

	args := []string{"run", "--rm"}
	args = append(args, scopedMountArgs(t, target, repo, "apps")...)
	args = append(args, "-w", "/app", "--user", hostUIDGID(t),
		"--entrypoint", "bash", img, "-c", `
[ -e /app/libs ] && { echo "LIBS_PRESENT"; exit 1; }
[ -e /app/unmounted ] && { echo "UNMOUNTED_PRESENT"; exit 1; }
echo "NARROWED_OK"`)

	out, err := exec.Command("docker", args...).CombinedOutput()
	s := string(out)
	if err != nil || !strings.Contains(s, "NARROWED_OK") {
		t.Fatalf("a subdir scope must not carry unrelated repo paths: %v\n%s", err, s)
	}
}
