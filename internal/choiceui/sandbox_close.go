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

// SandboxCloseCopy is the modal body. y removes the sandbox; n and esc return
// to the agent; ctrl+c interrupts the agent and leaves the sandbox up.
func SandboxCloseCopy() []string {
	return []string{
		"close this sandbox?",
		"",
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"",
		"y  remove the sandbox",
		"n / esc  go back",
		"ctrl+c  interrupt the agent and keep the sandbox",
	}
}

// ConfirmSandboxClose draws the close modal and waits for one answer.
func ConfirmSandboxClose(s tcell.Screen) SandboxClose {
	for {
		drawSandboxClose(s)
		switch ev := s.PollEvent().(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyCtrlC:
				return SandboxClosePass
			case tcell.KeyEscape, tcell.KeyEnter:
				return SandboxCloseStay
			case tcell.KeyRune:
				switch ev.Rune() {
				case 'y', 'Y':
					return SandboxCloseRemove
				case 'n', 'N':
					return SandboxCloseStay
				}
			}
		}
	}
}

func drawSandboxClose(s tcell.Screen) {
	p := styles()
	s.Clear()
	w, h := s.Size()
	lines := SandboxCloseCopy()
	styled := make([]struct {
		text  string
		style tcell.Style
	}, len(lines))
	for i, text := range lines {
		style := p.body
		switch text {
		case "close this sandbox?":
			style = p.title
		case "sbx rm --force will run.", "That deletes the session.", "Uncommitted changes can be permanently lost.":
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
