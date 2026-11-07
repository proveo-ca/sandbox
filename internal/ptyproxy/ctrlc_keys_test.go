//go:build !windows

package ptyproxy

import "testing"

func TestInterruptKeyLeavesOtherKeysAndControlStrings(t *testing.T) {
	for _, input := range []string{"c", "\x1b[99u", "\x1b[99;3u", "\x1b[99;5:3u", "\x1b]title\x03\a", "\x1bP\x1b[99;5u\x1b\\"} {
		if start, _ := interruptKey([]byte(input)); start != -1 {
			t.Errorf("intercepted %q", input)
		}
	}
}
