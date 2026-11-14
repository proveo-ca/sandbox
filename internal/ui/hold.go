// SPEC: _spec/internal/ui/output-vocabulary.puml (HOLD)
package ui

import (
	"bytes"
	"sync"
)

// heldLines is what a held Default printed, measured at the width of the terminal it stands in for.
type heldLines struct {
	buf   bytes.Buffer
	width func() int
}

func (h *heldLines) Write(p []byte) (int, error) { return h.buf.Write(p) }

func (h *heldLines) cols() int { return h.width() }

// Hold keeps Default's lines off the terminal until release, which puts Default back and returns
// what it kept; a second call of release returns the same bytes.
func Hold() (release func() []byte) {
	prev := Default
	prev.mu.Lock()
	pending, shown := prev.pending, prev.shown
	prev.mu.Unlock()
	kept := &heldLines{width: prev.width}
	held := &Printer{W: kept, Plain: prev.Plain, Tier: prev.Tier, pending: pending, shown: shown}
	Default = held
	var once sync.Once
	var out []byte
	return func() []byte {
		once.Do(func() {
			held.mu.Lock()
			pending, shown := held.pending, held.shown
			out = bytes.Clone(kept.buf.Bytes())
			held.mu.Unlock()
			prev.mu.Lock()
			prev.pending, prev.shown = pending, shown
			prev.mu.Unlock()
			Default = prev
		})
		return out
	}
}

// Replay prints what a Hold kept through Default, as Default would have printed it then.
func Replay(held []byte) {
	if len(held) == 0 {
		return
	}
	p := Default
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	_, _ = p.W.Write(held)
}
