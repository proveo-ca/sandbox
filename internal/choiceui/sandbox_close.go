// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import "github.com/gdamore/tcell/v2"

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

// ConfirmSandboxClose draws the close modal and waits for one answer.
func ConfirmSandboxClose(s tcell.Screen) SandboxClose {
	input, sig := readModal(s, SandboxCloseCopy(), "close this sandbox?", sandboxCloseChoices())
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
