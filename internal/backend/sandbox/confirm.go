package sandbox

import (
	"os"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/choiceui"
)

// headedConfirm reports whether this run may ask the operator a question on its terminal.
var headedConfirm = func() bool {
	if os.Getenv("PROVEO_SCHEDULE") != "" || !agentio.WizardEnabled() {
		return false
	}
	return agentTerminal()
}

// agentTerminal reports whether this run can hand the agent a terminal.
var agentTerminal = func() bool {
	return agentio.IsReaderTTY(os.Stdin) && agentio.IsWriterTTY(os.Stdout)
}

// The dialogs this package asks; tests replace them.
var (
	askFetchPushed       = choiceui.AskFetchPushed
	askSandboxUpgrade    = choiceui.AskSandboxUpgrade
	askUnansweredRemoval = choiceui.AskUnansweredRemoval
)
