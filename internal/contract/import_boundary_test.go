// SPEC: _spec/_devops/image-lineage-and-publish.puml
package contract_test

import (
	"os/exec"
	"strings"
	"testing"
)

const rootPkg = "github.com/proveo-ca/proveo"

// Sidecar images build Go from a partial context. defs/sidecars/egress-proxy/Dockerfile
// copies only go.mod, cmd/ and internal/ — no root-level .go files and no defs/ tree —
// so any package under internal/ that imports the root embed package makes that image
// fail with "no required module provides package". The root package is for main
// packages to hand down: internal/manifest takes an fs.FS for exactly this reason, and
// internal/provider now does too. This test is the guard that was missing when the
// bridge tables were first embedded.
func TestInternalPackagesDoNotImportRoot(t *testing.T) {
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	cmd := exec.Command(goBin, "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./internal/...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if imp == rootPkg {
				t.Errorf("%s imports %s; internal packages must take an fs.FS instead, "+
					"or sidecar images that copy only cmd/ and internal/ stop building", pkg, rootPkg)
			}
		}
	}
}

// The same context that breaks on a root import must actually be able to build the
// binary it ships. Deps are cheap to enumerate; a full docker build is not.
func TestEgressBinaryBuildsFromPartialContext(t *testing.T) {
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	cmd := exec.Command(goBin, "list", "-deps", "./cmd/proveo-egress")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == rootPkg {
			t.Fatalf("cmd/proveo-egress depends on %s, which its Dockerfile context does not copy", rootPkg)
		}
	}
}
