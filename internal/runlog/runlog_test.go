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

func TestTheHeaderRecordsTheHomeEveryTildeResolvesTo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_HOME", t.TempDir())
	l, err := Open("t-home")
	if err != nil {
		t.Skipf("runlog unavailable: %v", err)
	}
	l.Close()
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimLeft(string(b), "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "=== run t-home ===") || lines[1] != "home: "+home {
		t.Errorf("the header is not the run line, then \"home: %s\":\n%s", home, b)
	}
	if n := strings.Count(string(b), "home: "); n != 1 {
		t.Errorf("the home is recorded %d times, want once:\n%s", n, b)
	}
}
