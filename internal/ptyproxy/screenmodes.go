// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
//go:build !windows

package ptyproxy

import (
	"bytes"
	"sync"
)

// screenModes follows two modes the child sets on the operator's terminal and an
// overlay undoes: the alternate screen and a hidden cursor.
type screenModes struct {
	mu      sync.Mutex
	scan    csiScanner
	alt     bool
	entered bool // the child switched to the alternate screen at least once
	hidden  bool
}

// observe scans one chunk of child output for CSI ? Ps [;Ps…] h / l.
func (s *screenModes) observe(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scan.scan(b, func(params []byte, final byte) {
		if (final != 'h' && final != 'l') || len(params) == 0 || params[0] != '?' {
			return
		}
		for _, mode := range bytes.Split(params[1:], []byte{';'}) {
			switch string(mode) {
			case "1049", "1047", "47":
				s.alt = final == 'h'
				s.entered = s.entered || s.alt
			case "25":
				s.hidden = final == 'l'
			}
		}
	})
}

// enteredAlt reports whether the child switched to the alternate screen at least once.
func (s *screenModes) enteredAlt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entered
}

// reassert is what puts the child's modes back after an overlay left the alternate screen and showed the cursor.
func (s *screenModes) reassert() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	if s.alt {
		out = append(out, "\x1b[?1049h"...)
	}
	if s.hidden {
		out = append(out, "\x1b[?25l"...)
	}
	return out
}
