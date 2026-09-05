// SPEC: _spec/internal/agentio/agent-terminal-io.puml
//
// SPEC: _spec/internal/agentio/agent-terminal-io.puml
package agentio

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	tailLines       = 24
	tailFragmentCap = 8192
)

var ansiSeq = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\\\\-_]")

func Stdio(out, err io.Writer, tty bool) (io.Writer, io.Writer, *Tail) {
	tail := NewTail(tailLines)
	if tty {
		return out, err, tail
	}
	return io.MultiWriter(out, tail), io.MultiWriter(err, tail), tail
}

type Tail struct {
	mu    sync.Mutex
	n     int
	buf   []byte
	lines []string
}

func NewTail(n int) *Tail { return &Tail{n: n} }

func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexAny(t.buf, "\n\r")
		if i < 0 {
			break
		}
		t.push(string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > tailFragmentCap {
		t.push(string(t.buf))
		t.buf = nil
	}
	return len(p), nil
}

func (t *Tail) push(line string) {
	line = strings.TrimSpace(ansiSeq.ReplaceAllString(line, ""))
	line = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, line)
	if line == "" {
		return
	}
	if n := len(t.lines); n > 0 && t.lines[n-1] == line {
		return
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
}

func (t *Tail) Lines() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > 0 {
		t.push(string(t.buf))
		t.buf = nil
	}
	return t.lines
}

func FilterEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_STDIN_FILTER"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

func Tracer(path string) (func([]byte, bool), func()) {
	if strings.TrimSpace(path) == "" {
		return nil, func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		ui.Warnf("stdin trace: cannot open %s (%v); continuing untraced", path, err)
		return nil, func() {}
	}
	ui.Storef("stdin trace → %s (every byte the agent is sent)", path)
	fmt.Fprintf(f, "=== trace opened %s ===\n", time.Now().Format(time.RFC3339Nano))
	var mu sync.Mutex
	tap := func(b []byte, forwarded bool) {
		c := append([]byte(nil), b...)
		mu.Lock()
		defer mu.Unlock()
		verdict := "sent"
		if !forwarded {
			verdict = "DROPPED"
		}
		fmt.Fprintf(f, "%s  n=%-4d %-7s %-40q %s\n",
			time.Now().Format("15:04:05.000"), len(c), verdict, renderControl(c), hexBytes(c))
	}
	return tap, func() {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(f, "=== trace closed %s ===\n", time.Now().Format(time.RFC3339Nano))
		_ = f.Close()
	}
}

func renderControl(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == 0x1b:
			sb.WriteString("<ESC>")
		case c == '\r':
			sb.WriteString("<CR>")
		case c == '\n':
			sb.WriteString("<LF>")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, "<%02X>", c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func hexBytes(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02x", c)
	}
	return sb.String()
}

func IsStdinTTY() bool { return IsReaderTTY(os.Stdin) }

func IsWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func IsReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
