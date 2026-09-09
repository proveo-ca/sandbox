//go:build !windows

package ptyproxy

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// RUN MUST NOT RETURN BEFORE THE CHILD'S LAST OUTPUT HAS BEEN DRAINED.
//
// The read happens AFTER Run returns, deliberately: that turns a timing window
// into a contract. Nothing here waits or polls, so the only way the bytes are
// in the pipe is that Run refused to return until the out pump had copied them.
//
// Repeated, because the defect was a RACE and one pass only flips a coin.
// `pty.Start` returns with the child already running, and the out pump was
// started two statements later; a child this short exits first, and Run's
// deferred `m.Close()` then tore the master down with the output still in it.
//
// MEASURED against the unfixed code: a few percent of attempts lost the output,
// so a single attempt passed far more often than it caught anything — the
// package's other two PTY tests do one attempt each and failed about three runs
// in five, which read as flake rather than as this bug. 500 caught it 6 times
// out of 6, always inside the first 40, so the loop is a fail-fast ceiling and
// not a half-second of work.
// SPEC: _spec/internal/reviewgate/pty-review-proxy.puml
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
