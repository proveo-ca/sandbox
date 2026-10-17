package sandbox

import (
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/agentio"
)

// headedConfirm reports whether this run may ask the operator a question on its terminal.
var headedConfirm = func() bool {
	if os.Getenv("PROVEO_SCHEDULE") != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROVEO_WIZARD"))) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return agentTerminal()
}

// agentTerminal reports whether this run can hand the agent a terminal.
var agentTerminal = func() bool {
	return agentio.IsReaderTTY(os.Stdin) && agentio.IsWriterTTY(os.Stdout)
}

func openScreen() (tcell.Screen, error) {
	screen, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	if err := screen.Init(); err != nil {
		return nil, err
	}
	return screen, nil
}
