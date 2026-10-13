// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package choiceui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// SandboxResetCopy explains sbx rm --force before an unrecoverable engine is deleted.
func SandboxResetCopy(name string) []string {
	return []string{
		"this sandbox cannot start",
		"",
		"sbx rm --force " + name + " will run.",
		"That deletes this sandbox session.",
		"The next proveo run creates a new sandbox with this same name.",
		"This session does not persist.",
		"Host files under the proveo home stay.",
		"Uncommitted clone edits are lost unless fetched.",
		"",
		"y  remove and start a new sandbox",
		"n / esc  keep it — the next run reattaches",
	}
}

// EmptyPrepareCopy explains an empty V2 prepare when older history is left on disk.
func EmptyPrepareCopy(stores []string) []string {
	lines := []string{
		"OpenCode V2 history is not prepared",
		"",
		"1. initialize an empty V2 database",
		"2. publish a credential-free checkpoint",
		"3. start the agent on that cache",
		"",
		"The older store is not imported.",
		"It stays on disk.",
	}
	for _, store := range stores {
		lines = append(lines, store)
	}
	lines = append(lines,
		"This sandbox stays up either way.",
		"The prepared cache does not survive sbx rm.",
		"",
		"y  prepare empty V2",
		"n / esc  stop — the next run asks again",
	)
	return lines
}

// SandboxUpgradeCopy explains replacing a kept sandbox so this run's settings apply.
// Every harness reuses one sandbox name, so the question is the same for each.
func SandboxUpgradeCopy(name string, changed []string, live, clone bool) []string {
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
	if live {
		lines = append(lines, "It is running. Yes replaces that live session.")
	}
	lines = append(lines,
		"",
		"y  replace the sandbox",
		"n / esc  keep it — the next run reattaches",
	)
	return lines
}

// ConfirmSandboxUpgrade waits for permission to replace a kept sandbox.
func ConfirmSandboxUpgrade(s tcell.Screen, name string, changed []string, live, clone bool) bool {
	return confirmPrompt(s, SandboxUpgradeCopy(name, changed, live, clone), "this sandbox was created with different settings")
}

// ConfirmSandboxReset waits for permission to delete an unrecoverable sandbox.
func ConfirmSandboxReset(s tcell.Screen, name string) bool {
	return confirmPrompt(s, SandboxResetCopy(name), "this sandbox cannot start")
}

// ConfirmEmptyPrepare waits for permission to start V2 without importing older history.
func ConfirmEmptyPrepare(s tcell.Screen, stores []string) bool {
	return confirmPrompt(s, EmptyPrepareCopy(stores), "OpenCode V2 history is not prepared")
}

func confirmPrompt(s tcell.Screen, lines []string, title string) bool {
	for {
		drawPrompt(s, lines, title)
		switch ev := s.PollEvent().(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyEnter, tcell.KeyCtrlC:
				return false
			case tcell.KeyRune:
				switch ev.Rune() {
				case 'y', 'Y':
					return true
				case 'n', 'N':
					return false
				}
			}
		}
	}
}

func drawPrompt(s tcell.Screen, lines []string, title string) {
	p := styles()
	s.Clear()
	w, h := s.Size()
	styled := make([]struct {
		text  string
		style tcell.Style
	}, len(lines))
	for i, text := range lines {
		style := p.body
		switch {
		case text == title:
			style = p.title
		case strings.HasPrefix(text, "sbx rm --force ") || strings.Contains(text, "does not persist") || strings.Contains(text, "lost unless fetched") || strings.Contains(text, "not imported") || strings.Contains(text, "does not survive"):
			style = p.warn
		}
		styled[i].text = text
		styled[i].style = style
	}
	boxW := 0
	for _, l := range styled {
		if n := len([]rune(l.text)); n > boxW {
			boxW = n
		}
	}
	boxW += 6
	boxH := len(styled) + 2
	x0, y0 := (w-boxW)/2, (h-boxH)/2
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	for y := y0; y < y0+boxH && y < h; y++ {
		for x := x0; x < x0+boxW && x < w; x++ {
			s.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	drawBorder(s, x0, y0, boxW, boxH, p.warn)
	for i, l := range styled {
		y := y0 + 1 + i
		if y >= h {
			break
		}
		start := x0 + (boxW-len([]rune(l.text)))/2
		for j, r := range []rune(l.text) {
			if x := start + j; x >= 0 && x < w {
				s.SetContent(x, y, r, nil, l.style)
			}
		}
	}
	s.Show()
}
