// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/gdamore/tcell/v2"
)

// SandboxClose is the operator's answer to a headed Ctrl+C.
type SandboxClose int

const (
	SandboxCloseStay SandboxClose = iota
	SandboxCloseRemove
	SandboxClosePass
)

// SandboxCloseCopy is the close-modal body.
func SandboxCloseCopy() []string {
	return []string{
		"close this sandbox?",
		"",
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
	}
}

func sandboxCloseChoices() []modalChoice {
	return []modalChoice{
		{Key: "y/N", Text: "remove the sandbox"},
		{Key: "esc", Text: "go back"},
		{Key: "ctrl+c", Text: "interrupt the agent and keep the sandbox"},
	}
}

// NewSandboxClose is the close dialog as a Bubble Tea model.
func NewSandboxClose() tea.Model {
	return typedModal{lines: SandboxCloseCopy(), title: "close this sandbox?", choices: sandboxCloseChoices()}
}

// SandboxCloseAnswer reads the operator's answer off a model NewSandboxClose made, once its Program ended.
func SandboxCloseAnswer(m tea.Model) SandboxClose {
	d, ok := m.(typedModal)
	if !ok {
		return SandboxCloseStay
	}
	return sandboxCloseOf(d.input, d.signal)
}

// ConfirmSandboxClose draws the close modal and waits for one answer.
func ConfirmSandboxClose(s tcell.Screen) SandboxClose {
	return sandboxCloseOf(readModal(s, SandboxCloseCopy(), "close this sandbox?", sandboxCloseChoices()))
}

// sandboxCloseOf maps what was typed and the key that ended the dialog to an answer.
func sandboxCloseOf(input string, sig modalSignal) SandboxClose {
	switch sig {
	case modalCtrlC:
		return SandboxClosePass
	case modalEnter:
		if modalYes(input) {
			return SandboxCloseRemove
		}
		return SandboxCloseStay
	default:
		return SandboxCloseStay
	}
}
