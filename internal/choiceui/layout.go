// SPEC: _spec/internal/choiceui/figure-fidelities.puml, _spec/internal/choiceui/topology-strip.puml
package choiceui

const (
	stripRows  = figureRows + 1             // the figure, plus a blank separating it from the help
	stripCols  = 72 + hostPad + (boxW - 30) // the narrowest terminal the figure is drawn on
	digestRows = 2                          // one blank, the caption alone
)

type stripFit int

const (
	stripNone   stripFit = iota
	stripDigest          // the caption alone — the same facts, one row
	stripBlock           // the full five, in the foot
	stripPane            // beside the rows, for no rows at all
)

type layout struct {
	banner   bool
	header   bool
	strip    stripFit
	helpSlot int // rows held for help, at its maximum over every row and option
	pane     int
	tooSmall bool
	body     int
}

func (f *Form) rowsHeight() int { return len(f.bodyLines()) }

func (f *Form) maxHelpLines(width int) int {
	most := 0
	for i := range f.Rows {
		r := f.Rows[i]
		for j := range r.Options {
			for _, on := range []bool{false, true} {
				probe := r
				probe.Selected = j
				probe.Hover = j
				probe.On = append(append([]bool(nil), r.On...), make([]bool, len(r.Options))...)[:len(r.Options)]
				probe.On[j] = on
				if n := len(probe.helpLines(width)); n > most {
					most = n
				}
			}
			if m := r.cellMore(j); m != nil {
				for at := range m.Options {
					probe, list := r, *m
					list.At = at
					probe.More, probe.Lists, probe.Selected, probe.Hover = &list, nil, j, j
					if n := len(probe.helpLines(width)); n > most {
						most = n
					}
				}
			}
		}
	}
	return most
}

func (f *Form) layout(w, h int) layout {
	if lay := f.ladder(w, h, true); lay.strip == stripBlock {
		return lay
	}
	if col := f.paneOrigin(w); col >= 0 {
		if lay := f.ladder(w, h, false); !lay.tooSmall && lay.body >= paneRows {
			lay.strip, lay.pane = stripPane, col
			return lay
		}
	}
	return f.ladder(w, h, true)
}

func (f *Form) ladder(w, h int, strip bool) layout {
	help := f.maxHelpLines(helpWidth(w))
	lines := f.rowsHeight()
	lay := layout{helpSlot: help}

	need := cardRows + lines + help
	if f.Skip != "" {
		need += skipRows
	}

	afford := func(cost int) bool {
		if need+cost > h {
			return false
		}
		need += cost
		return true
	}
	room := true
	step := func(want bool, cost int) bool {
		if !room {
			return false
		}
		if !want {
			return false // absent, not refused: the ladder is not broken by it
		}
		room = afford(cost)
		return room
	}
	lay.header = step(len(f.Header) > 0, len(f.facts(w)))

	if strip && f.Topology != nil && step(true, digestRows) {
		lay.strip = stripDigest
		if w >= stripCols && afford(stripRows-digestRows) {
			lay.strip = stripBlock
		} else {
			room = false // the figure did not fit, so nothing below it may
		}
	}
	lay.banner = step(len(f.Banner) > 0, len(f.Banner)+2)

	lay.body = lines
	if deficit := need - h; deficit > 0 {
		lay.body = clampInt(lines-deficit, 0, lines)
	}
	lay.tooSmall = lines == 0 || lay.body < min(minBodyLines, lines)
	return lay
}

const paneGutter = 2

// paneOrigin is the pane's column beside the card, or -1 when the terminal cannot hold both.
func (f *Form) paneOrigin(w int) int {
	if f.Topology == nil {
		return -1 // nothing to place
	}
	if cardWidth(w)+paneGutter+paneCols.width > w {
		return -1
	}
	return w - paneCols.width
}
