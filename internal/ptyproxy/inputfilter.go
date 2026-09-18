// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
package ptyproxy

import (
	"bytes"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type inputFilter struct {
	dropFocus   bool
	dropReplies bool
	window      time.Duration
	now         func() time.Time

	// mouse is fed from the child's OUTPUT stream by the out pump.
	mouse mouseTracker

	// cpr is the same idea for cursor-position queries: a `CSI …R` arriving on
	// stdin is garbage UNLESS the child asked for it, and the asking is
	// visible on the child's own output stream.
	cpr cprTracker

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
	reportMouse                   // DEC 9/1000/1001/1002/1003 press, release or motion
)

func (f *inputFilter) keep(b []byte) bool {
	switch classifyTerminalReport(b) {
	case reportFocus:
		return !f.dropFocus
	case reportMouse:
		// Wanted exactly while the application has mouse tracking on, which it
		// announces on its own output stream. Off, it is an unasked-for report.
		if f.mouse.enabled() {
			return true
		}
		return !f.dropReplies
	case reportReply:
		// SOLICITED, exactly like mouse. `dropReplies` rests on "on a prompt
		// stream nothing was ever asked, so the first copy is already one too
		// many" — true of the reports proveo's own overlay provokes, false of a
		// child that sent `CSI 6n` and is blocked waiting. cecli's prompt_toolkit
		// does exactly that and reports the withheld answer as
		// "your terminal doesn't support cursor position requests (CPR)".
		if isCursorPositionReport(b) && f.cpr.answered() {
			return true
		}
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
	// 8-bit C1 CSI (0x9b) is the same sequence as ESC [. Some emulators
	// answer Device Attributes that way; without this branch the trailing
	// "c" reached the prompt stream as a keystroke.
	if len(b) >= 2 && b[0] == 0x9b {
		wrapped := make([]byte, 0, 2+len(b)-1)
		wrapped = append(wrapped, 0x1b, '[')
		wrapped = append(wrapped, b[1:]...)
		return classifyTerminalReport(wrapped)
	}
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
		case 'u': // kitty keyboard protocol
			// CSI ? flags u — flag report (the terminal's answer to CSI ? u).
			// CSI > flags u / CSI < n u / CSI = flags ; mode u — push / pop / set.
			// Cursor 2026.09.15 emits CSI > 1 u at startup; on stdin that is a
			// report, never a key. CSI 1;5 u (no private prefix) IS a keypress.
			if kittyKeyboardControl(body) {
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

// kittyKeyboardControl is a CSI u with a private prefix. Those are push / pop
// / set / query-answer. A CSI-u KEYPRESS has no prefix (`CSI 1;5 u`).
func kittyKeyboardControl(body []byte) bool {
	if len(body) < 1 {
		return false
	}
	switch body[0] {
	case '?', '>', '<', '=':
		rest := body[1 : len(body)-1]
		return len(rest) == 0 || isNumericParams(rest)
	}
	return false
}

// mouseTracker follows the DEC private modes that make a terminal SEND mouse
// reports, read off the child's output stream.
type mouseTracker struct {
	// on is read by keep() on the input pump and written by observe() on the
	// output pump: an atomic keeps the input path off the parser's lock.
	on atomic.Bool

	// mu guards the multi-field parser state, which only the output pump
	// touches today but which cannot be made atomic as a unit.
	mu    sync.Mutex
	modes uint16
	scan  csiScanner
}

// maxModeCarry bounds the partial CSI held between two output reads.
const maxModeCarry = 128

func (t *mouseTracker) enabled() bool { return t.on.Load() }

// observe scans one chunk of child output for CSI ? Ps [;Ps…] h / l.
// csiScanner walks a child-output stream and hands each COMPLETE CSI to a
// callback as (params-and-intermediates, final byte). It carries a sequence
// split across two reads, bounded by maxModeCarry so a long run that never
// reaches a final byte is dropped rather than hoarded.
//
// Extracted unchanged from mouseTracker.observe when cursor-position queries
// needed the same walk. Each watcher keeps its OWN scanner, so neither can
// consume the other's carry.
type csiScanner struct{ carry []byte }

func (s *csiScanner) scan(b []byte, fn func(params []byte, final byte)) {
	buf := b
	if len(s.carry) > 0 {
		buf = append(s.carry, b...)
		s.carry = nil
	}
	for i := 0; i < len(buf); {
		if buf[i] != 0x1b {
			i++
			continue
		}
		if i+1 >= len(buf) {
			s.hold(buf[i:])
			return
		}
		if buf[i+1] != '[' {
			i += 2
			continue
		}
		j := i + 2
		for j < len(buf) && buf[j] >= 0x20 && buf[j] < 0x40 { // params and intermediates
			j++
		}
		if j == len(buf) { // the final byte is in the next read
			s.hold(buf[i:])
			return
		}
		fn(buf[i+2:j], buf[j])
		i = j + 1
	}
}

func (s *csiScanner) hold(b []byte) {
	if len(b) > maxModeCarry {
		return // too long to be a mode set: do not hoard the child's output
	}
	s.carry = append([]byte(nil), b...)
}

func (t *mouseTracker) observe(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scan.scan(b, func(params []byte, final byte) {
		if (final == 'h' || final == 'l') && len(params) > 0 && params[0] == '?' {
			t.apply(params[1:], final == 'h')
		}
	})
}

// maxOutstandingCPR bounds the credit a chatty child can build up. A query
// nobody answered must not leave a permanent licence to forward stray reports.
const maxOutstandingCPR = 8

// cprTracker counts cursor-position queries the CHILD sent, so their answers
// can be forwarded even where every unsolicited report is dropped.
type cprTracker struct {
	// outstanding is written by observe() on the output pump and read by
	// keep() on the input pump; atomic keeps the two off one lock.
	outstanding atomic.Int32

	mu   sync.Mutex
	scan csiScanner
}

// observe watches for DSR-CPR: `CSI 6 n`, and DECXCPR's `CSI ? 6 n`. Only 6
// asks for the cursor; `CSI 5 n` asks for device status and is answered with
// something else entirely.
func (t *cprTracker) observe(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scan.scan(b, func(params []byte, final byte) {
		if final != 'n' {
			return
		}
		if len(params) > 0 && params[0] == '?' {
			params = params[1:]
		}
		if string(params) != "6" {
			return
		}
		if t.outstanding.Load() < maxOutstandingCPR {
			t.outstanding.Add(1)
		}
	})
}

// answered spends one outstanding query, reporting whether there was one.
func (t *cprTracker) answered() bool {
	for {
		n := t.outstanding.Load()
		if n <= 0 {
			return false
		}
		if t.outstanding.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// isCursorPositionReport is classifyTerminalReport's `R` case, asked of one
// sequence. reportReply covers several shapes and only this one is solicited.
func isCursorPositionReport(b []byte) bool {
	if len(b) < 4 || b[0] != 0x1b || b[1] != '[' {
		return false
	}
	body := b[2:]
	return body[len(body)-1] == 'R' && isNumericParams(body[:len(body)-1])
}

func (t *mouseTracker) apply(params []byte, set bool) {
	for _, p := range bytes.Split(params, []byte(";")) {
		n, err := strconv.Atoi(string(p))
		if err != nil {
			continue
		}
		bit := mouseModeBit(n)
		if bit == 0 {
			continue
		}
		if set {
			t.modes |= bit
		} else {
			t.modes &^= bit
		}
	}
	t.on.Store(t.modes != 0)
}

// mouseModeBit maps the modes that start and stop reporting. The encodings
// (1005/1006/1015/1016) only change a report's shape, so a terminal sends
// nothing for them alone and they must not hold tracking on by themselves.
func mouseModeBit(mode int) uint16 {
	switch mode {
	case 9: // X10 press-only
		return 1 << 0
	case 1000: // press and release
		return 1 << 1
	case 1001: // highlight tracking
		return 1 << 2
	case 1002: // button-drag
		return 1 << 3
	case 1003: // any-motion
		return 1 << 4
	}
	return 0
}

const maxHeld = 128

func (f *inputFilter) split(b []byte) (forward, held []byte) {
	for i := 0; i < len(b); {
		switch {
		case b[i] == 0x1b:
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
		case b[i] == 0x9b:
			// 8-bit CSI. Same hold/drop rules as ESC [ … .
			n, complete := csiParamsEnd(b[i+1:])
			if !complete {
				if len(b)-i <= maxHeld {
					return forward, append(held, b[i:]...)
				}
				forward = append(forward, b[i:]...)
				return forward, nil
			}
			seq := b[i : i+1+n]
			fake := append([]byte{0x1b, '['}, seq[1:]...)
			if f.keep(fake) {
				forward = append(forward, seq...)
			}
			i += 1 + n
		case f.dropReplies && isOrphanReportStart(b[i:]):
			// A DA1 whose ESC was already released as a keypress arrives as
			// `[?6c`. On a prompt stream that body is still a report.
			n, complete := csiParamsEnd(b[i+1:])
			if !complete {
				if len(b)-i <= maxHeld {
					return forward, append(held, b[i:]...)
				}
				forward = append(forward, b[i:]...)
				return forward, nil
			}
			seq := b[i : i+1+n]
			fake := append([]byte{0x1b}, seq...)
			if f.keep(fake) {
				forward = append(forward, seq...)
			}
			i += 1 + n
		default:
			j := i + 1
			for j < len(b) && b[j] != 0x1b && b[j] != 0x9b && (!f.dropReplies || !isOrphanReportStart(b[j:])) {
				j++
			}
			forward = append(forward, b[i:j]...)
			i = j
		}
	}
	return forward, nil
}

// isOrphanReportStart is a CSI report whose ESC never arrived (or was already
// forwarded as a lone-Esc keypress). `[` alone is a keystroke; the private
// markers `? > < =` are not something a human types as a burst.
func isOrphanReportStart(b []byte) bool {
	if len(b) < 2 || b[0] != '[' {
		return false
	}
	switch b[1] {
	case '?', '>', '<', '=':
		return true
	}
	return false
}

// csiParamsEnd walks CSI parameters and intermediates to the final byte
// (0x40..0x7e). b is the bytes AFTER the introducer (after ESC [ or 0x9b).
func csiParamsEnd(b []byte) (int, bool) {
	for i := 0; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return i + 1, true
		}
	}
	return 0, false
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
