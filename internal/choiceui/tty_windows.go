// SPEC: _spec/internal/choiceui/confirm-dialogs.puml, _spec/internal/choiceui/session.puml
//go:build windows

package choiceui

import (
	"os"

	"github.com/proveo-ca/proveo/internal/ui"
)

// openTerminal is the console behind stdin and stdout. It refuses with ErrTerminalHeld while a
// Session holds the terminal.
func openTerminal() (in, out *os.File, done func(), err error) {
	if terminalHeld() {
		return nil, nil, nil, ErrTerminalHeld
	}
	return ttyFiles()
}

// ttyFiles is the console behind stdin and stdout, with the live line off it until done.
func ttyFiles() (in, out *os.File, done func(), err error) {
	return os.Stdin, os.Stdout, ui.SuspendLive(), nil
}
