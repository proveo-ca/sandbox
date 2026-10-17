//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// RUN MUST NOT RETURN BEFORE THE CHILD'S LAST OUTPUT HAS BEEN DRAINED.
func TestRunDrainsTheChildsOutputBeforeReturning(t *testing.T) {
	const want = "LAST_WORDS"
	for i := 0; i < 500; i++ {
		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		inR, _, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		var mu sync.Mutex
		var tapped []byte
		p := New(inR, outW)
		p.OutTap = func(b []byte) {
			mu.Lock()
			defer mu.Unlock()
			tapped = append(tapped, b...)
		}

		// Exits as fast as a process can, which is what opens the window.
		if err := p.Run(exec.Command("sh", "-c", "echo "+want)); err != nil {
			t.Fatalf("attempt %d: Run: %v", i, err)
		}
		_ = outW.Close()

		buf := make([]byte, 4096)
		n, _ := outR.Read(buf)
		_ = outR.Close()
		_ = inR.Close()

		if got := string(buf[:n]); !strings.Contains(got, want) {
			t.Fatalf("attempt %d: the operator's terminal lost the child's last output: %q\n"+
				"Run returned before the out pump had drained the master, and closing the "+
				"master took the bytes with it", i, got)
		}
		mu.Lock()
		gotTap := string(tapped)
		mu.Unlock()
		if !strings.Contains(gotTap, want) {
			t.Fatalf("attempt %d: the transcript tap saw nothing: %q\n"+
				"a child that dies on its last line is exactly the one whose output matters",
				i, gotTap)
		}
	}
}
