// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
//go:build windows

package choiceui

import "os"

// openTerminal is the console behind stdin and stdout.
func openTerminal() (in, out *os.File, done func(), err error) {
	return os.Stdin, os.Stdout, func() {}, nil
}
