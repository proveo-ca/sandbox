// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// Frame is one still picture of the run the form currently describes: where the
// credential rests, which hop is in the path, how many lanes survive it.
//
// It is a PROJECTION and never a second state — the caller builds it at paint
// time from the same values Selection returns — so a frame cannot disagree with
// the checkboxes above it. Everything the design calls a "re-label" is a string
// here rather than a shape, which is what holds the picture to one geometry.
type Frame struct {
	Square    string // inside the box: "sbx · claudecode", "dind · opencode"
	Hop       string // "sbx proxy", "mitm + squid"; empty means there is no hop
	Interface string // the interface dot's label
	Caption   string // one sentence, the same facts as the figure; clipped, never wrapped
	Key       KeyHome
	Lane      LaneKind
	Open      int // lanes that reach a cloud
	Refused   int // lanes drawn as refused
	Speaking  bool
	Focus     Focus
}

// KeyHome is where the credential actually rests for this run. It starts beside
// the host because that is where the secret is; the answer to the credentials
// row is drawn as the place it travels TO.
type KeyHome int

const (
	KeyAtHost KeyHome = iota
	KeyInSquare
	KeyAtHop
)

// LaneKind is what the hop does to the traffic, which is the one thing the tier
// names do not say out loud.
type LaneKind int

// The ZERO value is the screened hop, deliberately. A zero Frame — one built by
// a caller that forgot a field, or a future bug — then draws the tighter picture
// with no lanes rather than an open one: a security figure must not fail to the
// most permissive reading of itself.
const (
	LaneScreened LaneKind = iota // the hop terminates and re-originates: allowlist, balanced, deny-all
	LaneWatched                  // the hop sees it and passes it: open, allow-all
	LaneAsked                    // the hop asks the operator first: review
)

// Focus is the element the cursor's row owns. Every element is drawn on every
// frame; the focused one is raised and the rest are dimmed, so nothing appears
// or vanishes as the operator walks down the form.
type Focus int

const (
	FocusNone Focus = iota
	FocusHop
	FocusKey
	FocusSquare
	FocusReturn
	FocusSay
)

// GlyphTier is how much decoration the terminal is trusted with.
//
// Emoji are deliberately absent. They read well in a diagram and badly in a
// grid: the speaking head is two runes that go-runewidth measures as one column
// and terminals draw as two, and the cloud is an ambiguous-width character
// whose column count depends on the font. A Nerd Font devicon is one rune of
// one column with no ambiguity, so nerd is the decorated tier and ASCII is the
// tier that is correct everywhere.
type GlyphTier int

const (
	GlyphsNerd GlyphTier = iota
	GlyphsASCII
)

// glyphSet is one tier's runes. EVERY rune the figure draws lives here — the
// arrowheads and the corner marks included — because a tier that is "ASCII
// except for the arrows" is not a tier a plain terminal can render.
type glyphSet struct {
	cloud, key, quiet, speaking string
	spine, screened, watched    string
	refused, asking             string
	east, west, north, up       string // the arrowheads and the two corner marks
	pulse                       string // the mote that rides a lane while animating
}

func glyphsFor(t GlyphTier) glyphSet {
	if t == GlyphsASCII {
		return glyphSet{
			cloud: "(~)", key: "o-", quiet: "\"", speaking: "\"!",
			spine: "=", screened: "-", watched: ".",
			refused: "x", asking: "?",
			east: ">", west: "<", north: "^", up: "^", pulse: "*",
		}
	}
	return glyphSet{
		cloud: "", key: "", quiet: "", speaking: "",
		spine: "═", screened: "─", watched: "╌",
		refused: "×", asking: "?",
		east: "▸", west: "◂", north: "▴", up: "↑", pulse: "•",
	}
}

// lane is the run of rule characters a single lane is drawn with.
func (g glyphSet) lane(k LaneKind) string {
	switch k {
	case LaneWatched:
		return g.watched
	case LaneScreened:
		return g.screened
	}
	return g.spine
}

// drawTopology paints the figure at y0. It always occupies exactly stripRows,
// because the caller has already reserved them: a figure whose height moved
// with its content would shove whatever came after it.
func drawTopology(s tcell.Screen, y0 int, fr Frame, tier GlyphTier, p palette, tick int) {
	g := glyphsFor(tier)
	dim, lit := p.body, p.brand
	on := func(f Focus) tcell.Style {
		if fr.Focus == FocusNone {
			return p.accent
		}
		if fr.Focus == f {
			return lit
		}
		return dim
	}

	const (
		colHost   = 2
		colSquare = 14
		colHop    = 42
		colLanes  = 62
	)
	// gap pads to a column but never butts two runs together: the labels inside
	// the figure are of unknown length, so a fixed column is a floor rather than
	// a promise, and one space is what keeps a long harness name legible.
	// Named `pn` rather than `p`: the palette is also `p` in this scope, and a
	// shadow one edit from being silently wrong is not worth the shorter name.
	gap := func(pn *pen, col int) *pen {
		if pn.col() >= col {
			return pn.write(tcell.StyleDefault, " ")
		}
		return pn.padTo(col)
	}
	// The columns are a floor, not a ceiling: `gap` only ever moves right, so a
	// long harness name or hop label would run under the lanes and overwrite
	// them. Clipping keeps the figure legible where corruption would not, and
	// the caption carries the same facts for whatever gets cut.
	//
	// It is measured in DISPLAY columns rather than runes because a devicon is
	// ambiguous-width: the same glyph is one column in a Latin locale and two in
	// a CJK one, so the budget has to be asked, never assumed.
	fit := func(text string, from, until int) string {
		room := until - from
		if room <= 0 {
			return ""
		}
		for textWidth(text) > room {
			r := []rune(text)
			if len(r) <= 1 {
				return ""
			}
			text = string(r[:len(r)-1])
		}
		return text
	}

	// Row 1 — the question walking back to the operator, above the traffic. Only
	// review has one: it is the tier that has somebody to ask.
	if fr.Lane == LaneAsked {
		pn := newPen(s, colHost, y0+1)
		pn.write(on(FocusHop), g.asking+" "+g.north)
		gap(pn, colSquare).write(dim, g.west+strings.Repeat(g.screened, colHop-colSquare-2))
		gap(pn, colHop).write(on(FocusHop), g.asking+" asks you")
	}

	// Row 2 — the spine: host, the square, the hop, the lanes.
	pn := newPen(s, colHost, y0+2)
	pn.write(keyStyle(fr, KeyAtHost, on), keyIf(g, fr, KeyAtHost))
	pn.write(on(FocusNone), "() host")
	gap(pn, colSquare-4).write(dim, strings.Repeat(g.spine, 3)+g.east)

	gap(pn, colSquare).write(on(FocusSquare), "[ ")
	pn.write(keyStyle(fr, KeyInSquare, on), keyIf(g, fr, KeyInSquare))
	say := g.quiet
	if fr.Speaking {
		say = g.speaking
	}
	pn.write(on(FocusSay), say+" ")
	// Measured from where the pen actually is, and with the closing " ]" and the
	// arrow that follows it already subtracted — a budget taken before the key
	// and the bubble were written is a budget that clips a name which fits.
	// Clipped against the LANES, not against the hop column: the columns are a
	// floor, so a long harness name pushes the hop right rather than being cut
	// to fit a slot. The only thing that must never happen is running under the
	// lanes, because that corrupts the figure instead of shortening a label.
	pn.write(on(FocusSquare), fit(fr.Square, pn.col(), colLanes-14)+" ]")

	if fr.Hop == "" {
		// The one shape with nothing in the path. The columns are held open
		// rather than closed up, so the figure does not shift sideways between
		// one tier and the next — the spine simply runs through where a hop
		// would have been.
		start := pn.col() + 1
		gap(pn, colSquare).write(dim, strings.Repeat(g.spine, maxInt(3, colLanes-start-2))+g.east)
	} else {
		gap(pn, colHop-4).write(dim, strings.Repeat(g.lane(fr.Lane), 3)+g.east)
		gap(pn, colHop).write(on(FocusHop), "() "+fit(fr.Hop, pn.col()+3, colLanes-1))
		if k := keyIf(g, fr, KeyAtHop); k != "" {
			pn.write(tcell.StyleDefault, " ").write(keyStyle(fr, KeyAtHop, on), k)
		}
	}

	// Row 3 — the interface hanging off the square and returning to the host.
	rt := newPen(s, colHost, y0+3)
	rt.write(on(FocusReturn), g.north).padTo(colHost+3).
		write(on(FocusReturn), g.west+strings.Repeat(g.screened, colSquare-colHost-4))
	gap(rt, colSquare).
		write(on(FocusReturn), "() "+fit(fr.Interface, rt.col()+3, colLanes-2)+" "+g.up)

	// The lanes, stacked so the spine's own lane is the middle one.
	drawLanes(s, y0, colLanes, fr, g, on(FocusHop), p.warn, tick)

	// The caption, last, clipped rather than wrapped: it is one sentence by
	// construction and a second line would cost a row nobody reserved.
	w, _ := s.Size()
	newPen(s, colHost, y0+4).write(p.aside, clip(fr.Caption, w-colHost-1))
}

// drawLanes stacks the surviving lanes and the refused ones around the spine.
// A pulse rides whichever lane the tick has reached, so motion says which way
// the traffic is going without a caption having to.
func drawLanes(s tcell.Screen, y0, col int, fr Frame, g glyphSet, lit, dim tcell.Style, tick int) {
	rule := g.lane(fr.Lane)
	rows := []int{y0 + 1, y0 + 2, y0 + 3}
	total := fr.Open + fr.Refused
	for i := 0; i < total && i < len(rows); i++ {
		pn := newPen(s, col, rows[i])
		run := rule + rule + rule
		if tick > 0 && (tick/2)%3 == i {
			run = rule + g.pulse + rule
		}
		if i < fr.Open {
			pn.write(lit, run+g.east+" ").write(lit, g.cloud)
			continue
		}
		pn.write(dim, run+g.refused)
	}
	if fr.Lane == LaneAsked {
		// Nothing has been consented yet, so no lane is drawn: the lone cloud is
		// out of reach until the operator answers the question on row one.
		newPen(s, col+4, y0+2).write(dim, g.cloud)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// keyIf returns the key glyph when this is where the credential rests.
func keyIf(g glyphSet, fr Frame, at KeyHome) string {
	if fr.Key != at {
		return ""
	}
	return g.key + " "
}

func keyStyle(fr Frame, at KeyHome, on func(Focus) tcell.Style) tcell.Style {
	if fr.Key == at {
		return on(FocusKey)
	}
	return on(FocusNone)
}
