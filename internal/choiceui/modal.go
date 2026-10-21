// SPEC: _spec/internal/sbx/clone-workspace.puml, _spec/internal/ptyproxy/pty-ownership.puml, _spec/internal/sbx/kit-lifecycle.puml
package choiceui

import (
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

// modalChoice is one answer row, drawn as [Key] then Text.
type modalChoice struct {
	Key  string
	Text string
}

type modalSignal int

const (
	modalEnter modalSignal = iota
	modalEsc
	modalCtrlC
	modalInterrupt
)

const (
	modalPad       = 2
	modalFieldRoom = 23
	modalInputMax  = 64
)

type modalSeg struct {
	text  string
	style tcell.Style
}

// drawModal paints a fullscreen confirm. Body lines are left-aligned, each
// choice is its own row, and the last rows are the input and "enter submits".
func drawModal(s tcell.Screen, lines []string, title string, choices []modalChoice, input string) {
	p := styles()
	s.Clear()
	rows := modalRows(p, lines, title, choices, input)
	boxW := 0
	for _, row := range rows {
		if n := modalSegsWidth(row); n > boxW {
			boxW = n
		}
	}
	boxW += modalPad*2 + 2
	boxH := len(rows) + 2
	w, h := s.Size()
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
	for i, row := range rows {
		putModalSegs(s, x0+1+modalPad, y0+1+i, row)
	}
	s.Show()
}

func modalRows(p palette, lines []string, title string, choices []modalChoice, input string) [][]modalSeg {
	var rows [][]modalSeg
	for _, text := range lines {
		rows = append(rows, []modalSeg{{text, modalLineStyle(p, text, title)}})
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		rows = append(rows, nil)
	}
	keyW := 0
	for _, c := range choices {
		if n := textWidth("[" + c.Key + "]"); n > keyW {
			keyW = n
		}
	}
	for _, c := range choices {
		key := "[" + c.Key + "]"
		gap := strings.Repeat(" ", keyW-textWidth(key)) + "  "
		rows = append(rows, []modalSeg{
			{key, p.accent},
			{gap + c.Text, p.body},
		})
	}
	rows = append(rows, nil)
	marker := modalAskMarker()
	shown := []rune(input)
	if len(shown) > modalFieldRoom {
		shown = shown[len(shown)-modalFieldRoom:]
	}
	pad := strings.Repeat(" ", modalFieldRoom-len(shown))
	rows = append(rows, []modalSeg{
		{marker, p.brand},
		{"[", p.idle},
		{string(shown), p.body.Bold(true)},
		{"_", p.brand},
		{pad, p.idle},
		{"]", p.idle},
	})
	rows = append(rows, []modalSeg{{strings.Repeat(" ", textWidth(marker)) + "enter submits", p.aside}})
	return rows
}

func modalLineStyle(p palette, text, title string) tcell.Style {
	switch {
	case text == title:
		return p.title
	case strings.HasPrefix(strings.TrimSpace(text), "sbx rm --force"),
		strings.Contains(text, "are lost"),
		strings.Contains(text, "permanently lost"),
		strings.Contains(text, "deletes the session"),
		strings.Contains(text, "does not persist"),
		strings.Contains(text, "lost unless fetched"),
		strings.Contains(text, "not imported"),
		strings.Contains(text, "does not survive"):
		return p.warn
	default:
		return p.body
	}
}

func modalAskMarker() string {
	switch ui.GlyphTierFrom(os.Getenv) {
	case ui.GlyphsASCII:
		return "> "
	case ui.GlyphsOff:
		return "  "
	default:
		return "› "
	}
}

func modalSegsWidth(segs []modalSeg) int {
	n := 0
	for _, seg := range segs {
		n += textWidth(seg.text)
	}
	return n
}

func putModalSegs(s tcell.Screen, x, y int, segs []modalSeg) {
	w, h := s.Size()
	if y < 0 || y >= h {
		return
	}
	for _, seg := range segs {
		for _, r := range seg.text {
			rw := runeCols(r)
			if rw < 1 {
				rw = 1
			}
			if x >= 0 && x < w {
				s.SetContent(x, y, r, nil, seg.style)
			}
			x += rw
		}
	}
}

// readModal draws until enter, esc, ctrl+c, or an interrupt.
func readModal(s tcell.Screen, lines []string, title string, choices []modalChoice) (string, modalSignal) {
	var input string
	for {
		drawModal(s, lines, title, choices, input)
		switch ev := s.PollEvent().(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventInterrupt:
			return input, modalInterrupt
		case *tcell.EventKey:
			next, sig, done := applyModalKey(input, ev)
			input = next
			if done {
				return input, sig
			}
		}
	}
}

func applyModalKey(input string, ev *tcell.EventKey) (string, modalSignal, bool) {
	switch ev.Key() {
	case tcell.KeyEscape:
		return input, modalEsc, true
	case tcell.KeyCtrlC:
		return input, modalCtrlC, true
	case tcell.KeyEnter:
		return input, modalEnter, true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if r := []rune(input); len(r) > 0 {
			input = string(r[:len(r)-1])
		}
		return input, 0, false
	case tcell.KeyCtrlU:
		return "", 0, false
	case tcell.KeyRune:
		if ev.Rune() < 0x20 {
			return input, 0, false
		}
		if len([]rune(input)) >= modalInputMax {
			return input, 0, false
		}
		return input + string(ev.Rune()), 0, false
	default:
		return input, 0, false
	}
}

// modalYes is the [y/N] submit: y and yes accept; anything else declines.
func modalYes(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
