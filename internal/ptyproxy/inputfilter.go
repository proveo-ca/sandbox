// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
package ptyproxy

import (
	"bytes"
	"sync"
	"time"
)

type inputFilter struct {
	dropFocus   bool
	dropReplies bool
	window      time.Duration
	now         func() time.Time

	mu     sync.Mutex
	recent []seenReply
}

type seenReply struct {
	b  []byte
	at time.Time
}

// DefaultReplyWindow is how close together two IDENTICAL reports must arrive
// to be read as one terminal answering twice rather than the application
// asking twice.
const DefaultReplyWindow = 2 * time.Second

func newInputFilter() *inputFilter {
	return &inputFilter{dropFocus: true, window: DefaultReplyWindow, now: time.Now}
}

type reportKind int

const (
	reportNone  reportKind = iota // keystrokes, pastes, anything not a report
	reportFocus                   // DEC mode 1004 focus in/out
	reportReply                   // an answer to a query the application sent
	reportMouse                   // DEC 1000/1006/1015/1016 mouse press, release or motion
)

func (f *inputFilter) keep(b []byte) bool {
	switch classifyTerminalReport(b) {
	case reportFocus:
		return !f.dropFocus
	case reportMouse:
		return !f.dropReplies
	case reportReply:
		if f.dropReplies {
			return false
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		now := f.now()
		kept := f.recent[:0]
		dup := false
		for _, r := range f.recent {
			if now.Sub(r.at) > f.window {
				continue
			}
			if bytes.Equal(r.b, b) {
				dup = true
			}
			kept = append(kept, r)
		}
		f.recent = kept
		if dup {
			return false // the surplus copy: no query is waiting for it
		}
		f.recent = append(f.recent, seenReply{b: append([]byte(nil), b...), at: now})
		return true
	}
	return true
}

func classifyTerminalReport(b []byte) reportKind {
	if len(b) < 3 || b[0] != 0x1b {
		return reportNone
	}
	switch b[1] {
	case '[':
		body := b[2:]
		if len(body) == 4 && body[0] == 'M' {
			return reportMouse
		}
		switch body[len(body)-1] {
		case 'I', 'O': // CSI I / CSI O — focus in / focus out
			if len(body) == 1 {
				return reportFocus
			}
		case 'c': // CSI ? … c — Primary/Secondary Device Attributes
			if body[0] == '?' || body[0] == '>' {
				return reportReply
			}
		case 'y': // CSI ? … $ y — DECRPM, the reply to a mode query
			if body[0] == '?' && len(body) >= 2 && body[len(body)-2] == '$' {
				return reportReply
			}
		case 'R': // CSI … R — cursor position report
			if isNumericParams(body[:len(body)-1]) {
				return reportReply
			}
		case 'u': // CSI ? flags u — the kitty keyboard protocol's flag report
			// opencode's TUI pushes its own flags with CSI > 5 u and then asks
			// what stuck. Unclassified, the ANSWER arrived as keystrokes.
			if body[0] == '?' && isNumericParams(body[1:len(body)-1]) {
				return reportReply
			}
		case 'M', 'm': // CSI < b;x;y M|m (SGR) · CSI b;x;y M (urxvt)
			if body[0] == '<' && isNumericParams(body[1:len(body)-1]) {
				return reportMouse
			}
			if isNumericParams(body[:len(body)-1]) {
				return reportMouse
			}
		}
	case 'P': // DCS … ST — XTVERSION and friends
		if bytes.HasSuffix(b, []byte{0x1b, '\\'}) {
			return reportReply
		}
	case '_': // APC … ST — the kitty GRAPHICS protocol's answer
		if bytes.HasSuffix(b, []byte{0x1b, '\\'}) {
			return reportReply
		}
	case ']': // OSC … ST/BEL — colour and palette queries
		if bytes.HasSuffix(b, []byte{0x1b, '\\'}) || b[len(b)-1] == 0x07 {
			return reportReply
		}
	}
	return reportNone
}

func isNumericParams(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

const maxHeld = 128

// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func (f *inputFilter) split(b []byte) (forward, held []byte) {
	for i := 0; i < len(b); {
		if b[i] != 0x1b {
			j := i
			for j < len(b) && b[j] != 0x1b {
				j++
			}
			forward = append(forward, b[i:j]...)
			i = j
			continue
		}
		end, complete := escEnd(b[i:])
		if !complete {
			// Unfinished: hold it for the next read, unless it is implausibly
			// long. The pump releases a held prefix after DefaultEscIdle, so a
			// lone ESC keypress is not swallowed waiting for a continuation.
			if len(b)-i <= maxHeld {
				return forward, append(held, b[i:]...)
			}
			forward = append(forward, b[i:]...)
			return forward, nil
		}
		seq := b[i : i+end]
		if f.keep(seq) {
			forward = append(forward, seq...)
		}
		i += end
	}
	return forward, nil
}

func escEnd(b []byte) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	switch b[1] {
	case '[': // CSI: params then a final byte in @..~
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1, true
			}
		}
		return 0, false
	case 'P', ']', '_', '^': // DCS, OSC, APC, PM: terminated by ST or BEL
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1, true
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2, true
			}
		}
		return 0, false
	}
	return 2, true // ESC + one byte: Alt-key, or an introducer we do not parse
}
