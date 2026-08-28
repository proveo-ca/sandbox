// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
package ptyproxy

import (
	"bytes"
	"sync"
	"time"
)

// A terminal writes two very different things to an application's input: the
// operator's KEYSTROKES, and REPORTS answering queries the application itself
// sent (device attributes, mode state, cursor position, version). Both arrive on
// the same file descriptor, and telling them apart is the application's job.
//
// That works until something sits in between. A multiplexer that answers one
// query twice — Zellij answers Primary Device Attributes twice, observed as two
// identical "\x1b[?62;4c" reads 42ms apart — leaves a surplus report with no
// query to belong to. Whatever consumes the stream then has to interpret it as
// input, because that is the only thing it can be. Where the far end treats
// input as a PROMPT STREAM rather than as terminal input, the surplus is
// enqueued and answered: a user message nobody typed.
//
// So the filter is not cosmetic. It removes the only bytes on this path that are
// neither a keystroke nor an answer anyone is waiting for.
type inputFilter struct {
	dropFocus bool
	// dropReplies removes EVERY report, not just a surplus copy. It is for a far
	// end that reads its input as a prompt stream rather than as terminal input:
	// there no report is owed to anyone, because nothing on that side ever asked.
	//
	// The dedup rule below cannot cover that case, and the reason is that it was
	// written for a different one. It forwards the FIRST copy on the premise that
	// the application asked and is owed an answer, and drops only a duplicate. A
	// LONE unsolicited report is therefore indistinguishable from a legitimate
	// answer and sails through — which is exactly what killed the sbx runs: a
	// single "\x1b[?6c" arrived five seconds into a run nobody had queried from,
	// was forwarded as the first copy, and was enqueued as a user message nobody
	// typed. The agent answered it and exited.
	dropReplies bool
	window      time.Duration
	now         func() time.Time

	mu     sync.Mutex
	recent []seenReply
}

// seenReply is one report already forwarded, kept only until it ages out of the
// window. A single-slot memory is not enough: the duplicate need not be
// ADJACENT. In the captured trace Zellij's surplus Device Attributes reply
// arrived with a mode report in between, which a "same as last time" check
// waves straight through.
type seenReply struct {
	b  []byte
	at time.Time
}

// DefaultReplyWindow is how close together two IDENTICAL reports must arrive to
// be read as one terminal answering twice rather than the application asking
// twice. Generous enough for a multiplexer's relay hop, far below any interval
// at which an application would re-query.
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

// keep reports whether b should reach the child.
//
// It only ever judges a read that is EXACTLY one report. A read carrying a
// report alongside anything else is forwarded untouched: dropping a keystroke to
// win a filtering argument is a far worse outcome than passing a stray report
// through, and terminals emit their reports as one atomic write anyway — every
// report in the captured traces arrived as its own read.
func (f *inputFilter) keep(b []byte) bool {
	switch classifyTerminalReport(b) {
	case reportFocus:
		// Focus in/out is a notification, not an answer and not a keystroke.
		// Nothing downstream of here consumes it, so forwarding it can only
		// produce noise in whatever reads the stream.
		return !f.dropFocus
	case reportMouse:
		// Solicited, so never de-duplicated; dropped only where nothing consumes
		// terminal input. See _spec/internal/ptyproxy/terminal-report-filter.puml.
		return !f.dropReplies
	case reportReply:
		// Nothing downstream asked, so nothing downstream is owed. Treated exactly
		// like focus: a report is the one thing on this path that is neither a
		// keystroke nor an answer anyone is waiting for.
		if f.dropReplies {
			return false
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		now := f.now()
		// Drop anything that has aged out, so the window is the only thing this
		// grows with — a long session cannot accumulate reports here.
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
		// The FIRST copy is forwarded: the application asked, and it is owed an
		// answer. Only the surplus is dropped.
		f.recent = append(f.recent, seenReply{b: append([]byte(nil), b...), at: now})
		return true
	}
	return true
}

// classifyTerminalReport recognises a read that is exactly one terminal report.
//
// Reports are self-delimiting and all begin with ESC, which is what makes this
// safe: a keystroke sequence that happens to look like one would have to be
// byte-identical to a report AND arrive alone in its own read.
func classifyTerminalReport(b []byte) reportKind {
	if len(b) < 3 || b[0] != 0x1b {
		return reportNone
	}
	switch b[1] {
	case '[':
		body := b[2:]
		// CSI M Cb Cx Cy — raw coordinate bytes, so no final-byte match is possible.
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
