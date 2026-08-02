package choiceui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// Consent draws a centered modal asking to allow one connection, and returns the
// answer. It takes the whole screen rather than printing a line, because the
// agent owns the alternate screen: plain text written into a running TUI
// interleaves with its rendering and corrupts the display instead of appearing
// above it.
//
// newScreen is injected so the dialog is testable against a SimulationScreen.
func Consent(newScreen func() (tcell.Screen, error), host, port string) (bool, error) {
	s, err := newScreen()
	if err != nil {
		return false, fmt.Errorf("consent prompt: %w", err)
	}
	if err := s.Init(); err != nil {
		return false, fmt.Errorf("consent prompt: %w", err)
	}
	defer s.Fini()

	for {
		drawConsent(s, host, port)
		switch ev := s.PollEvent().(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC, tcell.KeyEnter:
				return false, nil
			case tcell.KeyRune:
				switch ev.Rune() {
				case 'y', 'Y':
					return true, nil
				case 'n', 'N':
					return false, nil
				}
			}
		}
	}
}

// drawConsent renders the modal centred on whatever size the terminal is.
func drawConsent(s tcell.Screen, host, port string) {
	brand, bold, dim := styles()
	s.Clear()
	w, h := s.Size()

	lines := []struct {
		text  string
		style tcell.Style
	}{
		{"connection review", dim},
		{"", dim},
		{fmt.Sprintf("%s:%s", host, port), brand},
		{"", dim},
		{"allow this connection?", bold},
		{"", dim},
		{"y  allow      n / esc  deny", dim},
	}

	boxW := 0
	for _, l := range lines {
		if n := len([]rune(l.text)); n > boxW {
			boxW = n
		}
	}
	boxW += 6
	boxH := len(lines) + 2
	x0, y0 := (w-boxW)/2, (h-boxH)/2
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}

	// Fill the box so the agent's frame does not show through behind the text.
	for y := y0; y < y0+boxH && y < h; y++ {
		for x := x0; x < x0+boxW && x < w; x++ {
			s.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	drawBorder(s, x0, y0, boxW, boxH, brand)

	for i, l := range lines {
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
