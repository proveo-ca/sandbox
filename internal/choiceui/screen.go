// SPEC: _spec/internal/choiceui/tui-stack.puml
package choiceui

import (
	"charm.land/lipgloss/v2"
)

// screen is one frame being composed: styled runs of text, each placed by column
// and row. A later run covers an earlier one where they overlap.
type screen struct {
	w, h int
	runs []run
}

// run is one styled piece of text and where it starts.
type run struct {
	x, y int
	st   lipgloss.Style
	text string
}

func newScreen(w, h int) *screen { return &screen{w: w, h: h} }

// Size is the frame's width and height in cells.
func (s *screen) Size() (int, int) { return s.w, s.h }

// put places text at column x of row y; rows outside the frame are dropped.
func (s *screen) put(x, y int, st lipgloss.Style, text string) {
	if text == "" || y < 0 || y >= s.h || x >= s.w {
		return
	}
	s.runs = append(s.runs, run{x, y, st, text})
}

// dim redraws every run put so far in the faint style.
func (s *screen) dim() {
	faint := lipgloss.NewStyle().Faint(true)
	for i := range s.runs {
		s.runs[i].st = faint
	}
}

// paste puts every run of src with its top-left corner at column x0 of row y0.
func (s *screen) paste(src *screen, x0, y0 int) {
	for _, r := range src.runs {
		s.put(r.x+x0, r.y+y0, r.st, r.text)
	}
}

// view composes the runs, in the order they were put, into the frame's string.
func (s *screen) view() string {
	layers := make([]*lipgloss.Layer, len(s.runs))
	for i, r := range s.runs {
		layers[i] = lipgloss.NewLayer(r.st.Render(r.text)).X(r.x).Y(r.y).Z(i + 1)
	}
	return lipgloss.NewCanvas(s.w, s.h).Compose(lipgloss.NewCompositor(layers...)).Render()
}
