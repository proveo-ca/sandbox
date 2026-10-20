// SPEC: _spec/internal/sbx/kit-lifecycle.puml, _spec/defs/opencode/engine-local-history.puml
package choiceui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// SandboxUpgradeCopy explains replacing a kept sandbox so this run's settings apply.
// Every harness reuses one sandbox name, so the question is the same for each.
func SandboxUpgradeCopy(name string, changed []string, live, clone, history bool) []string {
	lines := []string{
		"this sandbox was created with different settings",
		strings.Join(changed, ", "),
		"",
		"1. carry clone commits and agent state home",
		"2. sbx rm --force " + name + " will run.",
		"3. this run creates a new sandbox with this same name",
		"",
		"This session does not persist.",
		"Host files under the proveo home stay.",
	}
	if clone {
		lines = append(lines, "Uncommitted clone edits are lost unless fetched.")
	} else {
		lines = append(lines, "Edits in the host checkout stay on disk.")
	}
	if history {
		lines = append(lines, "Its OpenCode history moves to the new sandbox.")
	}
	if live {
		lines = append(lines, "It is running. Yes replaces that live session.")
	}
	return lines
}

func upgradeChoices() []modalChoice {
	return []modalChoice{
		{Key: "y/N", Text: "replace the sandbox"},
		{Key: "esc", Text: "keep it — the next run reattaches"},
	}
}

// ConfirmSandboxUpgrade waits for permission to replace a kept sandbox.
func ConfirmSandboxUpgrade(s tcell.Screen, name string, changed []string, live, clone, history bool) bool {
	return confirmPrompt(s, SandboxUpgradeCopy(name, changed, live, clone, history), "this sandbox was created with different settings", upgradeChoices())
}

func confirmPrompt(s tcell.Screen, lines []string, title string, choices []modalChoice) bool {
	input, sig := readModal(s, lines, title, choices)
	return sig == modalEnter && modalYes(input)
}

// drawBorder draws a single-line box.
func drawBorder(s tcell.Screen, x0, y0, w, h int, style tcell.Style) {
	set := func(x, y int, r rune) {
		sw, sh := s.Size()
		if x >= 0 && x < sw && y >= 0 && y < sh {
			s.SetContent(x, y, r, nil, style)
		}
	}
	for x := x0 + 1; x < x0+w-1; x++ {
		set(x, y0, '─')
		set(x, y0+h-1, '─')
	}
	for y := y0 + 1; y < y0+h-1; y++ {
		set(x0, y, '│')
		set(x0+w-1, y, '│')
	}
	set(x0, y0, '┌')
	set(x0+w-1, y0, '┐')
	set(x0, y0+h-1, '└')
	set(x0+w-1, y0+h-1, '┘')
}
