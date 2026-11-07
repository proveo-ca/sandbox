//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import "regexp"

var encodedCtrlC = regexp.MustCompile(`^(?:99(?::[0-9:]*)?;5(?::[12])?u|27;5;99~)$`)

func interruptKey(b []byte) (start, end int) {
	for i := 0; i < len(b); {
		if b[i] == 3 {
			return i, i + 1
		}
		n, complete := 1, true
		switch b[i] {
		case 0x1b:
			n, complete = escEnd(b[i:])
		case 0x9b:
			n, complete = csiParamsEnd(b[i+1:])
			n++
		}
		if !complete {
			return -1, -1
		}
		key := b[i : i+n]
		if len(key) > 1 && key[0] == 0x9b {
			key = key[1:]
		} else if len(key) > 2 && key[0] == 0x1b && key[1] == '[' {
			key = key[2:]
		} else {
			key = nil
		}
		if encodedCtrlC.Match(key) {
			return i, i + n
		}
		i += n
	}
	return -1, -1
}

func holdKeySequence(b []byte) (forward, held []byte) {
	for i := 0; i < len(b); {
		n, complete := 1, true
		switch b[i] {
		case 0x1b:
			n, complete = escEnd(b[i:])
		case 0x9b:
			n, complete = csiParamsEnd(b[i+1:])
			n++
		}
		if !complete {
			if len(b)-i <= maxHeld {
				return b[:i], b[i:]
			}
			break
		}
		i += n
	}
	return b, nil
}
