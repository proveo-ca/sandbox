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
		{Name: "hermes", Kind: maintain.KindHarness},
		{Name: "opencode", Kind: maintain.KindHarness},
		{Name: "claudecode", Kind: maintain.KindHarness},
		{Name: "claudecode-solidity", Kind: maintain.KindHarness},
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

func TestNumberedTargetPickerUsesTheStatusVocabulary(t *testing.T) {
	var out strings.Builder
	got, err := pickTargetsNumbered([]maintain.Target{{Name: "hermes"}, {Name: "opencode"}}, "build", strings.NewReader("1\n"), &out)
	if err != nil || len(got) != 1 || got[0].Name != "hermes" {
		t.Fatalf("pick = %v, %v", got, err)
	}
	for _, want := range []string{"build", "select a target to build:", "   0) all", "   1) hermes", "target [0]: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("target picker missing %q:\n%s", want, out.String())
		}
	}
}

func TestTheAllRowAloneOrAmongOthersIsEveryTarget(t *testing.T) {
	t.Parallel()
	reg := []maintain.Target{{Name: "base"}, {Name: "hermes"}, {Name: "opencode"}}
	names := func(ts []maintain.Target) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name    string
		idxs    []int
		want    string
		wantErr bool
	}{
		{"one target", []int{2}, "hermes", false},
		{"two targets, in list order", []int{1, 3}, "base opencode", false},
		{"all alone", []int{0}, "base hermes opencode", false},
		{"all among others", []int{0, 2}, "base hermes opencode", false},
		{"nothing taken", nil, "", true},
		{"a row past the list", []int{9}, "", true},
	} {
		got, err := targetsAt(reg, c.idxs)
		if names(got) != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: targetsAt = %q, %v; want %q, error %v", c.name, names(got), err, c.want, c.wantErr)
		}
	}
}
