// SPEC: _spec/internal/agentio/agent-terminal-io.puml
package agentio

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTailKeepsTheExplanation(t *testing.T) {
	t.Parallel()
	w := NewTail(3)
	fmt.Fprint(w, "\x1b[2J\x1b[H🚀 Launching Claude Code…\n")
	fmt.Fprint(w, "\x1b[Kthinking\r\x1b[Kthinking\r")
	fmt.Fprint(w, "Ignoring 10 permissions.allow entries\n")
	fmt.Fprint(w, "\x1b[31mCredit balance is too low\x1b[0m")

	got := w.Lines()
	if len(got) != 3 {
		t.Fatalf("want the last 3 lines, got %d: %q", len(got), got)
	}
	if last := got[len(got)-1]; last != "Credit balance is too low" {
		t.Errorf("the unterminated final line must survive and be de-escaped, got %q", last)
	}
	for _, l := range got {
		if strings.Contains(l, "\x1b") {
			t.Errorf("escape sequences must be stripped, got %q", l)
		}
	}
	var thinking int
	for _, l := range got {
		if l == "thinking" {
			thinking++
		}
	}
	if thinking > 1 {
		t.Errorf("consecutive duplicates must collapse, got %q", got)
	}
}

func TestTailIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	w := NewTail(8)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				fmt.Fprintf(w, "g%d line %d\n\x1b[Kpartial %d", g, i, i)
			}
		}(g)
	}
	wg.Wait()

	got := w.Lines()
	if len(got) == 0 || len(got) > 8 {
		t.Fatalf("want 1..8 retained lines, got %d", len(got))
	}
	for _, l := range got {
		if strings.Contains(l, "\x1b") {
			t.Errorf("escapes must be stripped even under concurrency, got %q", l)
		}
	}
}

func TestStdioHandsTheTerminalOverUnwrapped(t *testing.T) {
	out, errw := os.Stdout, os.Stderr
	gotOut, gotErr, tail := Stdio(out, errw, true)
	if gotOut != io.Writer(out) || gotErr != io.Writer(errw) {
		t.Fatalf("interactive run wrapped the terminal: stdout=%T stderr=%T", gotOut, gotErr)
	}
	if tail == nil {
		t.Fatal("an interactive run needs somewhere for the pty proxy to put the agent's last words")
	}
	if lines := tail.Lines(); len(lines) != 0 {
		t.Fatalf("nothing has been written yet, so the tail must replay as empty, got %v", lines)
	}
	fmt.Fprint(tail, "Invalid API key · Please run /login\n")
	if lines := tail.Lines(); len(lines) != 1 || lines[0] != "Invalid API key · Please run /login" {
		t.Fatalf("the interactive tail must retain what the proxy feeds it, got %v", lines)
	}
}

func TestStdioTeesWhenStdoutIsRedirected(t *testing.T) {
	var out, errw bytes.Buffer
	gotOut, gotErr, tail := Stdio(&out, &errw, false)
	if tail == nil {
		t.Fatal("a redirected run must keep the agent's last output")
	}
	fmt.Fprintln(gotOut, "credit balance is too low")
	fmt.Fprintln(gotErr, "agent exited with code 137")
	if !strings.Contains(out.String(), "credit balance") {
		t.Fatalf("the tee stopped reaching stdout: %q", out.String())
	}
	if got := tail.Lines(); len(got) != 2 || got[0] != "credit balance is too low" {
		t.Fatalf("tail did not retain both streams: %v", got)
	}
}

func TestTracerRecordsControlBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "stdin.trace")
	tap, stop := Tracer(path)
	if tap == nil {
		t.Fatal("a named trace file must produce a tap")
	}
	tap([]byte("sh\r"), true)
	tap([]byte{0x1b, '[', '?', '6', 'c'}, false)
	stop()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"sh<CR>", "<ESC>[?6c", "1b 5b 3f 36 63", "n=3", "n=5", "sent", "DROPPED"} {
		if !strings.Contains(got, want) {
			t.Errorf("trace is missing %q:\n%s", want, got)
		}
	}
}

func TestTracerIsOffByDefault(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"", "   "} {
		if tap, stop := Tracer(v); tap != nil {
			stop()
			t.Errorf("PROVEO_TRACE_STDIN=%q must not install a tap", v)
		} else {
			stop()
		}
	}
}

func TestFilterEnabledIsOnUnlessTurnedOff(t *testing.T) {
	for _, off := range []string{"off", "0", "no", "false", "disable", "disabled", "OFF", "  off  "} {
		t.Setenv("PROVEO_STDIN_FILTER", off)
		if FilterEnabled() {
			t.Errorf("PROVEO_STDIN_FILTER=%q must turn the filter off", off)
		}
	}
	for _, on := range []string{"", "on", "1", "anything"} {
		t.Setenv("PROVEO_STDIN_FILTER", on)
		if !FilterEnabled() {
			t.Errorf("PROVEO_STDIN_FILTER=%q must leave the filter on", on)
		}
	}
}

func TestTTYPredicatesRejectNonFiles(t *testing.T) {
	t.Parallel()
	if IsWriterTTY(&bytes.Buffer{}) {
		t.Error("a buffer is not a terminal, and wrapping one is what costs the agent its tty")
	}
	if IsReaderTTY(strings.NewReader("x")) {
		t.Error("a reader that is not an *os.File cannot be a terminal")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsWriterTTY(f) || IsReaderTTY(f) {
		t.Error("/dev/null is an *os.File and still not a terminal")
	}
}
