// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

// The strip's fixed geometry.
const (
	stripRows  = figureRows + 1 // the figure, plus a blank separating it from the help
	stripCols  = 72             // the narrowest terminal the figure is drawn on
	digestRows = 2              // one blank, the caption alone
)

// stripFit is how much of the figure this terminal can afford.
type stripFit int

const (
	stripNone   stripFit = iota
	stripDigest          // the caption alone — the same facts, one row
	stripBlock           // the full five, in the foot
	stripPane            // beside the rows, for no rows at all
)

// layout is which optional regions this terminal can afford, decided in ONE
// pass before anything is painted. A region is dropped whole rather than
// clipped, and the rows, the hint and the help slot are never dropped.
type layout struct {
	banner   bool
	header   bool
	axis     bool
	strip    stripFit
	helpSlot int // rows held for help, at its maximum over every row and option
	// pane is the column the figure starts at, meaningful only for stripPane.
	pane int
	// tooSmall means the body cannot even show its floor, so the form is not
	// navigable and the prompt says so rather than painting a broken one.
	tooSmall bool
	// body is how many lines the scrolling region may paint; fewer than the form
	// has means it scrolls, and the foot then follows the body.
	body int
}

// rowsHeight is what the rows cost, read off the enumeration the painter walks
// rather than recomputed. The formula and the paint loop used to say it
// separately, and a viewport would have made that three places to disagree.
func (f *Form) rowsHeight() int { return len(f.bodyLines()) }

// maxHelpLines is the tallest the help block can EVER be for this form — over
// every row and every option, not the current selection.
func (f *Form) maxHelpLines(width int) int {
	most := 0
	for i := range f.Rows {
		r := f.Rows[i]
		for j := range r.Options {
			// Over the TICK as well as the option: the two greyed prefixes differ
			// in width, so one can wrap a line further than the other.
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
		}
	}
	return most
}

// layout runs the height ladder. The pane is not a rung and not a preference:
// it is the FALLBACK for a terminal the block figure will not fit in.
func (f *Form) layout(w, h int) layout {
	// The full figure first, priced against the height budget.
	if lay := f.ladder(w, h, true); lay.strip == stripBlock {
		return lay
	}
	// It did not fit; the pane costs no height, so it can still carry one.
	if col := f.paneOrigin(w); col >= 0 {
		if lay := f.ladder(w, h, false); !lay.tooSmall && lay.body >= paneRows {
			lay.strip, lay.pane = stripPane, col
			return lay
		}
	}
	return f.ladder(w, h, true)
}

// ladder is the height budget: one pass, one strict order, with the strip rung
// optional so the pane's placement can price a layout that does not use it.
func (f *Form) ladder(w, h int, strip bool) layout {
	help := f.maxHelpLines(w - 4)
	lines := f.rowsHeight()
	lay := layout{helpSlot: help}

	// Mandatory: the title pair, the whole body at its FULL height, the
	// blank-and-hint pair, and the help slot with its opening blank.
	need := 2 + lines + 2 + help
	if help > 0 {
		need++ // the blank the help block opens with
	}

	afford := func(cost int) bool {
		if need+cost > h {
			return false
		}
		need += cost
		return true
	}
	// ONE strict ladder, afforded in order, stopping at the first refusal. The
	// figure outranks the banner.
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
	lay.axis = step(f.axisLabel(), 2)
	lay.header = step(len(f.Header) > 0, len(f.Header)+1)

	// The digest is the fidelity below the figure: a sentence carrying where the
	// key rests and what the hop does is the cheapest real information here, so a
	// terminal that cannot spare five rows loses the picture rather than the facts.
	if strip && f.Topology != nil && step(true, digestRows) {
		lay.strip = stripDigest
		if w >= stripCols && afford(stripRows-digestRows) {
			lay.strip = stripBlock
		} else {
			room = false // the figure did not fit, so nothing below it may
		}
	}
	lay.banner = step(len(f.Banner) > 0, len(f.Banner)+1)

	// Only when even the mandatory regions do not fit does the body give ground.
	lay.body = lines
	if deficit := need - h; deficit > 0 {
		lay.body = clampInt(lines-deficit, 0, lines)
	}
	// The floor is capped by what the form HAS: a two-row form on a tall terminal
	// wants two lines, and demanding three would call a usable prompt unusable.
	lay.tooSmall = lines == 0 || lay.body < min(minBodyLines, lines)
	return lay
}

// paneGutter is the empty columns between the widest row and the figure.
const paneGutter = 2

// rightEdge is the last column this row's painter will touch, advanced by the
// SAME len() drawBodyLine uses — bytes, not display columns.
func (r *Row) rightEdge() int {
	// The label, which a row with few options can outrun.
	x := bodyIndent + len("  "+r.Label)
	opts := bodyIndent + 22
	for _, opt := range r.Options {
		opts += 4 + len(opt) + 3
	}
	if opts > x {
		x = opts
	}
	return x
}

// bodyRight is the widest column any body line reaches, over every row and
// every divider heading.
func (f *Form) bodyRight() int {
	most := 0
	for i := range f.Rows {
		if e := f.Rows[i].rightEdge(); e > most {
			most = e
		}
		if f.Rows[i].Divider {
			// The heading's real right edge, not the 72 it is centred ON.
			label := len(" " + f.Rows[i].Label + " ")
			pad := (72 - label) / 2
			if pad < 0 {
				pad = 0
			}
			if e := bodyIndent + pad + 12 + label; e > most {
				most = e
			}
		}
	}
	return most
}

// paneOrigin is the column the figure starts at when it can sit beside the
// rows, or -1 when it cannot. Placement, never clipping: derived from the
// widest row this form has, so no option is ever cut for a picture.
func (f *Form) paneOrigin(w int) int {
	if f.Topology == nil {
		return -1 // nothing to place, so nothing may narrow the rows
	}
	if f.bodyRight()+paneGutter+paneCols.width > w {
		return -1
	}
	// Anchored RIGHT, not flush against the rows.
	return w - paneCols.width
}
