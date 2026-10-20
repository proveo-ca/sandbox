package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// rendered is the simulation screen's text, one row per line.
func rendered(t *testing.T, sim tcell.SimulationScreen) string {
	t.Helper()
	cells, w, h := sim.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
