// SPEC: _spec/internal/agentio/agent-terminal-io.puml
// Package termtext reads terminal output as plain text.
package termtext

import (
	"regexp"
	"strings"
)

var controlSeq = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]" +
	"|\x1b[]PX^_][^\x07\x1b]*(?:\x07|\x1b\\\\)" +
	"|\x1b[ -/]*[0-~]?")

// StripANSI removes CSI sequences, OSC/DCS/SOS/PM/APC strings and every other escape sequence.
func StripANSI(s string) string { return controlSeq.ReplaceAllString(s, "") }

// Words is s without control sequences, each sequence and each whitespace run read as one space.
func Words(s string) string {
	return strings.Join(strings.Fields(controlSeq.ReplaceAllString(s, " ")), " ")
}
