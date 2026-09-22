package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"
)

func TestExecuteFleetPrintGroupsConcurrentAfterBases(t *testing.T) {
	t.Parallel()
	ts := []maintain.Target{
		{Name: "base", Kind: maintain.KindBase},
		{Name: "base-node", Kind: maintain.KindBase},
		{Name: "cecli", Kind: maintain.KindHarness},
		{Name: "opencode", Kind: maintain.KindHarness},
		{Name: "claudecode", Kind: maintain.KindHarness},
		{Name: "claudecode-solidity", Kind: maintain.KindHarness},
		{Name: "egress-proxy", Kind: maintain.KindSidecar},
	}
	var buf bytes.Buffer
	err := executeFleet("build", ts, true, &buf,
		func(maintain.Target) {},
		func(tgt maintain.Target, pio planIO) error {
			fmt.Fprintf(pio.out, "build %s\n", tgt.Name)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	idxConc := strings.Index(got, "# concurrent ")
	if idxConc < 0 {
		t.Fatalf("print lacks a concurrent group:\n%s", got)
	}
	for _, base := range []string{"# serial base\n", "# serial base-node\n"} {
		idx := strings.Index(got, base)
		if idx < 0 || idx > idxConc {
			t.Errorf("%q is not before the concurrent group:\n%s", strings.TrimSpace(base), got)
		}
	}
	head := got[idxConc:]
	line := head
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		line = head[:i]
	}
	if strings.Contains(line, "claudecode-solidity") {
		t.Errorf("solidity shares a wave with claudecode:\n%s", line)
	}
	if strings.Contains(got[:idxConc], "# concurrent ") {
		t.Fatalf("concurrent group before bases:\n%s", got)
	}
}
