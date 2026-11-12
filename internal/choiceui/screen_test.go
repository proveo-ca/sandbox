// SPEC: _spec/internal/choiceui/tui-stack.puml
package choiceui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// simScreen is an empty frame of the given size for a test to draw on.
func simScreen(_ *testing.T, w, h int) *screen { return newScreen(w, h) }

// The frame needs none of a terminal's lifecycle; these keep the tests' shape.
func (s *screen) Fini()            {}
func (s *screen) Show()            {}
func (s *screen) Clear()           { s.runs = nil }
func (s *screen) SetSize(w, h int) { s.w, s.h = w, h }

// screenLines is the frame as the terminal shows it, as plain text: one string per row.
func screenLines(s *screen) []string {
	lines := strings.Split(plain(s.view()), "\n")
	for len(lines) < s.h {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines[:s.h]
}

// rendered is the frame's text, one row per line, each padded to the frame's width.
func rendered(_ *testing.T, s *screen) string {
	var b strings.Builder
	for _, l := range screenLines(s) {
		b.WriteString(l + strings.Repeat(" ", max(0, s.w-textWidth(l))) + "\n")
	}
	return b.String()
}

// renderAt draws f with the cursor on a row and returns the frame's rows.
func renderAt(t *testing.T, f *Form, cursor, w, h int) []string {
	t.Helper()
	return frameLines(t, f, cursor, w, h, 0)
}

// simCell is one cell of a drawn frame: what shows there and the style it was put with.
type simCell struct {
	Runes []rune
	Style lipgloss.Style
}

// GetContents is the frame as a grid of cells, row by row, with its width and height.
func (s *screen) GetContents() ([]simCell, int, int) {
	cells := make([]simCell, s.w*s.h)
	for i := range cells {
		cells[i].Runes = []rune{' '}
	}
	for _, r := range s.runs {
		x := r.x
		for _, c := range r.text {
			if zeroWidth(c) {
				continue
			}
			if x >= 0 && x < s.w {
				cells[r.y*s.w+x] = simCell{Runes: []rune{c}, Style: r.st}
			}
			x += runeCols(c)
		}
	}
	return cells, s.w, s.h
}

// GetContent is the rune and the style at one cell; a blank cell reads as a space.
func (s *screen) GetContent(x, y int) (rune, []rune, lipgloss.Style, int) {
	cells, w, h := s.GetContents()
	if x < 0 || y < 0 || x >= w || y >= h {
		return ' ', nil, lipgloss.NewStyle(), 1
	}
	c := cells[y*w+x]
	return c.Runes[0], nil, c.Style, 1
}

// fgHex is a style's foreground as #RRGGBB, or "" when it sets none.
func fgHex(st lipgloss.Style) string {
	c := st.GetForeground()
	if c == nil {
		return ""
	}
	if _, none := c.(lipgloss.NoColor); none {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return strings.ToUpper(fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8))
}

// hexName is a palette colour as fgHex spells it.
func hexName(c int) string { return fmt.Sprintf("#%06X", c) }

// sameStyle reports whether two styles draw text the same way.
func sameStyle(a, b lipgloss.Style) bool { return a.Render("x") == b.Render("x") }

// Keys, as the form reads them.
func key(r rune) press     { return press{text: string(r)} }
func named(k string) press { return press{key: k} }
