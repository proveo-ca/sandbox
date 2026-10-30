// SPEC: _spec/_devops/release-gate.puml
package contract_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envreg"
)

// retirement is a deprecated surface that ships in at most one release; marker is a string only it has.
type retirement struct {
	what, file, marker string
}

var retirements = []retirement{
	{what: "proveo_retire_lsp_wiring, the strip of LSP wiring an earlier release persisted",
		file: "packages/lib/entrypoint-lib.sh", marker: "proveo_retire_lsp_wiring"},
}

func TestEveryRetirementMarkerIsStillInTheTree(t *testing.T) {
	t.Parallel()
	for _, r := range retirements {
		if !strings.Contains(readRepoFile(t, r.file), r.marker) {
			t.Errorf("%s no longer holds %q: delete the %s entry, or update its marker", r.file, r.marker, r.what)
		}
	}
}

func TestEveryDeprecatedAliasHasARetirement(t *testing.T) {
	t.Parallel()
	for _, v := range envreg.Vars {
		if v.AliasOf == "" {
			continue
		}
		if !slices.ContainsFunc(retirements, func(r retirement) bool { return strings.HasPrefix(r.what, v.Name+",") }) {
			t.Errorf("internal/envreg registers %s as a deprecated alias of %s, but no retirement entry bounds it", v.Name, v.AliasOf)
		}
	}
}

func TestNoRetirementOutlivesOneRelease(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	prev := previousRelease(t, root)
	for _, r := range retirements {
		err := exec.Command("git", "-C", root, "grep", "-q", "-F", "-e", r.marker, prev, "--").Run()
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 {
			continue
		}
		if err != nil {
			t.Fatalf("git grep %s in %s: %v", r.marker, prev, err)
		}
		t.Errorf("%s shipped deprecated in %s; delete it (and its entry here) before the next release", r.what, prev)
	}
}

// previousRelease is the newest v* tag merged into HEAD that does not point at HEAD.
func previousRelease(t *testing.T, root string) string {
	t.Helper()
	if out, _ := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository").Output(); strings.TrimSpace(string(out)) == "true" {
		t.Skip("shallow clone: run `git fetch --unshallow --tags` so the retirement ledger can read the last release")
	}
	merged, err := exec.Command("git", "-C", root, "tag", "--merged", "HEAD", "--sort=-v:refname", "--list", "v*").Output()
	if err != nil {
		t.Skipf("git tags unavailable: %v", err)
	}
	head, _ := exec.Command("git", "-C", root, "tag", "--points-at", "HEAD").Output()
	at := strings.Fields(string(head))
	for _, tag := range strings.Fields(string(merged)) {
		if !slices.Contains(at, tag) {
			return tag
		}
	}
	t.Skip("no release tag before HEAD")
	return ""
}
