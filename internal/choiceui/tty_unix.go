// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
//go:build !windows

package choiceui

import "os"

// openTerminal opens the operator's terminal itself, so a redirected stdout still gets the prompt.
func openTerminal() (in, out *os.File, done func(), err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	return tty, tty, func() { _ = tty.Close() }, nil
}
