// SPEC: _spec/internal/ptyproxy/pty-ownership.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// SandboxClose is the operator's answer to a headed Ctrl+C.
type SandboxClose int

const (
	SandboxCloseStay SandboxClose = iota
	SandboxCloseRemove
	SandboxClosePass
)

const sandboxCloseTitle = "close this sandbox?"

// SandboxCloseCopy is the close-dialog body.
func SandboxCloseCopy() []string {
	return []string{
		sandboxCloseTitle,
		"",
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
	}
}

// NewSandboxClose is the close dialog. When stop is set, accepting Remove runs it
// once and the dialog stays up, saying the stop is in flight, until its Program is ended.
func NewSandboxClose(stop func(), stopWait time.Duration) tea.Model {
	d := newDialog(sandboxCloseTitle, SandboxCloseCopy(), int(SandboxCloseStay),
		dialogButton{Label: "Stay", Key: 's'},
		dialogButton{Label: "Remove", Key: 'r', Danger: true},
		dialogButton{Label: "Interrupt agent", Key: 'i'},
	)
	d.onCtrlC = int(SandboxClosePass)
	if stop != nil {
		d.holdOn = int(SandboxCloseRemove)
		d.hold = func() string {
			stop()
			return fmt.Sprintf("stopping the agent (up to %s)", stopWait)
		}
	}
	return d
}

// SandboxCloseAnswer reads the operator's answer off a model NewSandboxClose made, once its Program ended.
func SandboxCloseAnswer(m tea.Model) SandboxClose {
	d, ok := dialogAnswer(m)
	if !ok {
		return SandboxCloseStay
	}
	return SandboxClose(d.answer)
}
