//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml

package e2e

import (
	"strings"
	"testing"
)

func TestShellExecLineNeverSpellsTheMarker(t *testing.T) {
	const marker = "PROVEO-SHELLEXEC-1789456759122517000"
	line := shellExecLine("echo hi", marker)

	if strings.Contains(line, marker) {
		t.Errorf("the typed line spells the marker whole, so the pane matches it on echo:\n%s", line)
	}
	if !strings.Contains(line, "=$?") {
		t.Errorf("the line no longer reports an exit status:\n%s", line)
	}
	// The halves must still be there, in order: the shell joins them on output.
	half := len(marker) / 2
	head, tail := marker[:half], marker[half:]
	hi, ti := strings.Index(line, head), strings.Index(line, tail)
	if hi < 0 || ti < hi {
		t.Errorf("the marker halves are missing or out of order:\n%s", line)
	}
}

func TestMarkerStatus(t *testing.T) {
	const marker = "PROVEO-SHELLEXEC-17894567591"
	typed := "$ bash -c 'git commit -q -m x'; echo '" + marker[:13] + "''" + marker[13:] + "'=$?\n"

	for _, c := range []struct {
		name   string
		screen string
		want   int
		ok     bool
	}{
		{"answer after the echo", typed + marker + "=0\n", 0, true},
		{"non-zero status", typed + marker + "=1\n", 1, true},
		{"multi-digit status", typed + marker + "=127\n", 127, true},
		{"echo only, no answer yet", typed, 0, false},
		{"the marker never appeared", "$ bash -c 'true'\n", 0, false},
		{"the last answer wins", typed + marker + "=0\n" + marker + "=3\n", 3, true},
		{"a marker with nothing after it is not a status", typed + marker + "=\n", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := markerStatus(c.screen, marker)
			if ok != c.ok {
				t.Fatalf("markerStatus ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Errorf("markerStatus = %d, want %d", got, c.want)
			}
		})
	}
}
