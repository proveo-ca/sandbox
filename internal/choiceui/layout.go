// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

// The strip's fixed geometry. Five rows because a figure that is clipped
// mid-body is worse than one that is absent, and 72 columns because that is
// already the width the add-on divider centres on — so carrying the strip gives
// the form no new minimum width.
const (
	stripRows  = 5  // one blank, three figure rows, one caption
	stripCols  = 72 // the narrowest terminal the figure is drawn on
	digestRows = 2  // one blank, the caption alone
)

// stripFit is how much of the figure this terminal can afford.
type stripFit int

const (
	stripNone   stripFit = iota
	stripDigest          // the caption alone — the same facts, one row
	stripBlock           // the full five
)

// layout is which optional regions this terminal can afford, decided in ONE
// pass before anything is painted. A region is dropped whole rather than
// clipped, and the rows, the hint and the help slot are never dropped: what a
// short terminal loses is decoration, never the thing being filled in.
//
// The promise here is deliberately narrow. draw() has always painted straight
// down and tcell has always discarded the overflow, so a terminal shorter than
// the form still loses its last rows. What the budget guarantees is only that
// THE STRIP is never what pushed them off.
type layout struct {
	banner   bool
	header   bool
	axis     bool
	strip    stripFit
	helpSlot int // rows held for help, at its maximum over every row and option
	// tooSmall means the body cannot even show its floor, so the form is not
	// navigable and the prompt says so rather than painting a broken one.
	tooSmall bool
	// body is how many lines the scrolling region may paint. Fewer than the form
	// has means it scrolls; the foot then follows the body rather than being
	// pinned to the screen's bottom edge, so a form that fits renders exactly as
	// it did before the viewport existed.
	body int
}

// rowsHeight is what the rows cost, read off the enumeration the painter walks
// rather than recomputed. The formula and the paint loop used to say it
// separately, and a viewport would have made that three places to disagree.
func (f *Form) rowsHeight() int { return len(f.bodyLines()) }

// maxHelpLines is the tallest the help block can EVER be for this form, taken
// over every row and every option rather than over the current selection.
// Reserving the current height would still jump the moment the cursor reached a
// wordier option, which is the whole failure the reservation exists to prevent.
func (f *Form) maxHelpLines(width int) int {
	most := 0
	for i := range f.Rows {
		r := f.Rows[i]
		for j := range r.Options {
			// Over the TICK as well as the option: helpLines writes "off: " for a
			// greyed box and "always on: " for a greyed-and-ticked one, six
			// columns apart, so a space press could still wrap one line further
			// and move the figure — the exact jump the reservation exists to stop.
			for _, on := range []bool{false, true} {
				probe := r
				probe.Selected = j
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

// layout spends the height in a fixed order: everything mandatory first, then
// the optional regions in the order they are worth keeping.
func (f *Form) layout(w, h int) layout {
	help := f.maxHelpLines(w - 4)
	lines := f.rowsHeight()
	lay := layout{helpSlot: help}

	// Mandatory: the title pair, the whole body, the blank-and-hint pair, and the
	// help slot with the blank it opens with.
	//
	// The body is charged at its FULL height, so a decoration can only ever be
	// bought with rows that are genuinely spare. Charging its floor instead was a
	// real bug: the ladder then spent rows the body was already using, and
	// GROWING the terminal by one row could buy the header and start the rows
	// scrolling. "What a short terminal loses is decoration" has to hold in that
	// direction too.
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
	// ONE strict ladder, afforded in order, stopping at the first thing that does
	// not fit. A cheaper region may never sneak past a dearer one that was
	// refused — that is what stops the figure DISAPPEARING as a terminal grows.
	//
	// The figure outranks the banner. This is a confirmation prompt about network
	// and credential posture, and the figure is the only thing on it that draws
	// that posture; the banner is a logo. Eight rows of wordmark ahead of five
	// rows of "where does the key rest and what does the hop stop" is the wrong
	// way round, so the ladder spends on the picture first.
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
	if f.Topology != nil && step(true, digestRows) {
		lay.strip = stripDigest
		if w >= stripCols && afford(stripRows-digestRows) {
			lay.strip = stripBlock
		} else {
			room = false // the figure did not fit, so nothing below it may
		}
	}
	lay.banner = step(len(f.Banner) > 0, len(f.Banner)+1)

	// Only when even the mandatory regions do not fit does the body give ground —
	// which is exactly what scrolling is for. Decorations were already refused
	// above, because `afford` measured them against a `need` holding the WHOLE
	// body, so nothing here can take a row a decoration is using. That ordering
	// is also what makes the foot FOLLOW the body rather than sit at the screen's
	// bottom edge, so a form that fits renders where it always did.
	lay.body = lines
	if deficit := need - h; deficit > 0 {
		lay.body = clampInt(lines-deficit, 0, lines)
	}
	// The floor is capped by what the form HAS: a two-row form on a tall terminal
	// wants two lines, and demanding three would call a usable prompt unusable.
	lay.tooSmall = lines == 0 || lay.body < min(minBodyLines, lines)
	return lay
}
