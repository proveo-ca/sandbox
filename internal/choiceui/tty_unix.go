// SPEC: _spec/internal/choiceui/confirm-dialogs.puml, _spec/internal/choiceui/session.puml
//go:build !windows

package choiceui

import (
	"os"

	"github.com/proveo-ca/proveo/internal/ui"
)

// openTerminal opens the operator's terminal itself, so a redirected stdout still gets the prompt.
// It refuses with ErrTerminalHeld while a Session holds the terminal.
func openTerminal() (in, out *os.File, done func(), err error) {
	if terminalHeld() {
		return nil, nil, nil, ErrTerminalHeld
	}
	return ttyFiles()
}

// ttyFiles is /dev/tty for reading and writing, with the live line off it until done.
func ttyFiles() (in, out *os.File, done func(), err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	resume := ui.SuspendLive()
	return tty, tty, func() { _ = tty.Close(); resume() }, nil
}
