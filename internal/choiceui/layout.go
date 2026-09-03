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
	stripBlock           // the full five, in the foot
	stripPane            // beside the rows, for no rows at all
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
	// pane is the column the figure starts at, meaningful only for stripPane.
	pane int
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

// layout runs the height ladder, and first asks the one question the ladder
// cannot: whether the figure can be had for nothing.
//
// The pane is NOT a rung. A rung that spends no height cannot be refused by a
// budget it never draws on, and putting it on the ladder would let it evict the
// banner it never charged for. So it short-circuits to the shipped ladder
// instead, run WITHOUT the strip rung — because that is the layout a pane
// actually produces: the five rows the block would have cost stay with the body.
func (f *Form) layout(w, h int) layout {
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
	if strip && f.Topology != nil && step(true, digestRows) {
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

// paneGutter is the empty columns between the widest row and the figure.
const paneGutter = 2

// rightEdge is the last column this row's painter will touch.
//
// Computed with the SAME len() drawBodyLine advances by — bytes, not display
// columns. Agreeing with the painter matters more than being right about wide
// runes: a non-ASCII option label already mis-advances the paint, and a budget
// that quietly "fixed" it would disagree with where the options actually land
// and let the pane overwrite one.
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
	// The inline reason IN FULL, not a floor.
	//
	// A floor was worse than no budget at all. The locked sbx egress row carries
	// changeBaselineHint — the one runnable command for changing the host
	// baseline — and it has no Help or OffWhy, and a locked row can never hold
	// the cursor, so the help block never shows it either. Clipping it to a floor
	// destroyed the only copy of that text to make room for a picture, which is
	// precisely the trade the pane is not allowed to make.
	if r.Reason != "" && (r.Locked || r.anyOff()) {
		x += len("— ") + len(r.Reason)
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
			// The heading's real right edge, not the 72 it is centred ON: the
			// painter writes 6 rules, the padded label and 6 more from a pad of
			// (72-L)/2, so a label past ~48 reaches beyond 72 — and the heading
			// branch of the painter ignores the limit entirely.
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
// rows, or -1 when it cannot.
//
// This is a PLACEMENT decision and never a clipping one: the origin is chosen
// after the widest row this form has, so no option is ever cut to make room for
// a picture. That keeps the shipped invariant — what a short terminal loses is
// decoration — true on the width axis too.
//
// Derived rather than a constant. A two-row prompt earns the pane far sooner
// than the full claudecode form does, and any fixed breakpoint would either
// clip somebody's options or withhold the figure from a terminal with room for
// it. There is deliberately no magic number here.
func (f *Form) paneOrigin(w int) int {
	if f.Topology == nil {
		return -1 // nothing to place, so nothing may narrow the rows
	}
	if f.bodyRight()+paneGutter+paneCols.width > w {
		return -1
	}
	// Anchored RIGHT, not flush against the rows. Both satisfy the budget, but
	// hugging the rows leaves the margin empty on a wide terminal while still
	// pressing on the row limit; against the right edge the rows keep every
	// column the figure does not need.
	return w - paneCols.width
}
