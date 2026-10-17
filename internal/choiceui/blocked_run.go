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
	lines = append(lines,
		"",
		"y  replace the sandbox",
		"n / esc  keep it — the next run reattaches",
	)
	return lines
}

// ConfirmSandboxUpgrade waits for permission to replace a kept sandbox.
func ConfirmSandboxUpgrade(s tcell.Screen, name string, changed []string, live, clone, history bool) bool {
	return confirmPrompt(s, SandboxUpgradeCopy(name, changed, live, clone, history), "this sandbox was created with different settings")
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
		case strings.HasPrefix(strings.TrimSpace(text), "sbx rm --force ") || strings.Contains(text, "are lost") || strings.Contains(text, "does not persist") || strings.Contains(text, "lost unless fetched") || strings.Contains(text, "not imported") || strings.Contains(text, "does not survive"):
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
