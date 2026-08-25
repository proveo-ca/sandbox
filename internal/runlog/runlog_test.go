package runlog

import (
	"os"
	"strings"
	"testing"
)

// Four of the five docker-path artifacts never exist under sbx. Naming them there
// sent an operator to missing files at exactly the moment they needed a real one.
func TestArtifactsMatchTheBackend(t *testing.T) {
	dir := t.TempDir()
	l, err := Open("t-artifacts")
	if err != nil {
		t.Skipf("runlog unavailable: %v", err)
	}
	defer l.Close()

	l.Artifacts(dir, true)
	l.Artifacts(dir, false)
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	sbxHalf, dockerHalf, _ := strings.Cut(got, "flow record")
	if strings.Contains(sbxHalf, "squid") || strings.Contains(sbxHalf, "flows.ndjson") {
		t.Errorf("sbx artifacts must not name proveo sidecar logs:\n%s", sbxHalf)
	}
	if !strings.Contains(sbxHalf, "spec.yaml") {
		t.Errorf("sbx artifacts must name the Kit:\n%s", sbxHalf)
	}
	if !strings.Contains(dockerHalf, "squid") {
		t.Errorf("docker artifacts must still name squid:\n%s", dockerHalf)
	}
}
