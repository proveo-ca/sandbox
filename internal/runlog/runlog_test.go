package runlog

import (
	"os"
	"strings"
	"testing"
)

func TestArtifactsNameTheSandboxEvidence(t *testing.T) {
	t.Setenv("PROVEO_HOME", t.TempDir())
	dir := t.TempDir()
	l, err := Open("t-artifacts")
	if err != nil {
		t.Skipf("runlog unavailable: %v", err)
	}
	defer l.Close()

	l.Artifacts(dir)
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"spec.yaml", PolicyLogFile, MemoryEvidenceFile} {
		if !strings.Contains(got, want) {
			t.Errorf("artifacts must name %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "squid") || strings.Contains(got, "flows.ndjson") {
		t.Errorf("artifacts name a retired sidecar log:\n%s", got)
	}
}
