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
	// node is a point in the topology and wall encloses the container. Both are
	// the BANNER's vocabulary rather than ASCII punctuation: the mark is built
	// from dots interrupting rules inside a bracketed frame, and the figure is
	// the same idea applied to a run. It also ends a collision — "()" is the
	// form's radio glyph three rows above, and it meant something else there.
	node, wallL, wallR string
}

func glyphsFor(t GlyphTier) glyphSet {
	if t == GlyphsASCII {
		return glyphSet{
			cloud: "(~)", key: "o-", quiet: "\"", speaking: "\"!",
			spine: "=", screened: "-", watched: ".",
			refused: "x", asking: "?",
			east: ">", west: "<", north: "^", up: "^", pulse: "*",
			node: "o", wallL: "|", wallR: "|",
		}
	}
	return glyphSet{
		cloud: "", key: "", quiet: "", speaking: "",
		spine: "═", screened: "─", watched: "╌",
		refused: "×", asking: "?",
		east: "▸", west: "◂", north: "▴", up: "↑", pulse: "•",
		node: "●", wallL: "│", wallR: "│",
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

// topoCols is the figure's column set, relative to its origin.
//
// A struct rather than four parameters because the columns must move TOGETHER:
// a hop column moved without the lanes column behind it draws the hop under the
// lanes, which is the one failure the fit budget exists to prevent.
type topoCols struct {
	host, square, hop, lanes int
	width                    int  // columns the whole figure occupies
	lead                     int  // blank rows before the figure: 1 block, 0 pane
	runLen                   int  // rule characters in a connector run
	asksLabel                bool // whether review spells out "asks you"
	headCaption              bool // caption's leading clause only
}

var blockCols = topoCols{
	host: 2, square: 14, hop: 42, lanes: 62, width: stripCols,
	lead: 1, runLen: 3, asksLabel: true,
}

// paneCols is the same figure in the margin beside the rows. It drops the three
// things that cost columns and carry least: the connector RUNS shrink to a
// single rule (the arrowhead carries the direction, the length carries
// nothing), review's "asks you" prose goes while its '?' stays, and the caption
// keeps only its leading clause. Everything that says what the run DOES — the
// key's home, the labels, the lane and refusal counts — survives intact.
// The lane column is sized to the WORST case rather than the typical one: the
// longest hop label ("mitm + squid", 12) beside the widest key (the ASCII "o- ",
// one column wider than a devicon). A hop clipped to "sbx pro" still reads, but
// the hop's identity is one of the four facts the figure exists to carry, and a
// few columns on a terminal this wide is a cheap price for keeping it whole.
var paneCols = topoCols{
	host: 0, square: 11, hop: 34, lanes: 59, width: 66,
	lead: 0, runLen: 1, headCaption: true,
}

const paneRows = 4 // three figure rows and a caption, with no leading blank

// drawTopology paints the block figure at y0. It always occupies exactly
// stripRows, because the caller has already reserved them: a figure whose
// height moved with its content would shove whatever came after it.
func drawTopology(s tcell.Screen, y0 int, fr Frame, tier GlyphTier, p palette, tick int) {
	drawFigure(s, 0, y0, blockCols, fr, tier, p, tick)
}

// captionHead is the caption's leading clause — "allow-all · broker" — which is
// the pane's whole caption. The block prints the sentence; the pane has room
// only for the two facts it opens with. A caption with no clause to cut on is
// left to the caller's clip, so a caption of any shape still says something.
func captionHead(caption string) string {
	if i := strings.Index(caption, " — "); i > 0 {
		return caption[:i]
	}
	return caption
}

// drawFigure paints the figure with its origin at (x0, y0), in one of the two
// column sets. One painter and one assembly, so the twenty-four states in
// _spec/internal/choiceui/topology-states.puml are verified once rather than
// twice.
func drawFigure(s tcell.Screen, x0, y0 int, cs topoCols, fr Frame, tier GlyphTier, p palette, tick int) {
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

	colHost, colSquare := x0+cs.host, x0+cs.square
	colHop, colLanes := x0+cs.hop, x0+cs.lanes
	run := func(k LaneKind) string { return strings.Repeat(g.lane(k), cs.runLen) }
	spine := strings.Repeat(g.spine, cs.runLen)

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

	top := y0 + cs.lead

	// Row 1 — the question walking back to the operator, above the traffic. Only
	// review has one: it is the tier that has somebody to ask.
	if fr.Lane == LaneAsked {
		pn := newPen(s, colHost, top)
		pn.write(on(FocusHop), g.asking+" "+g.north)
		gap(pn, colSquare).write(dim, g.west+strings.Repeat(g.screened, colHop-colSquare-2))
		gap(pn, colHop).write(on(FocusHop), g.asking)
		if cs.asksLabel {
			pn.write(on(FocusHop), " asks you")
		}
	}

	// Row 2 — the spine: host, the square, the hop, the lanes.
	pn := newPen(s, colHost, top+1)
	pn.write(keyStyle(fr, KeyAtHost, on), keyIf(g, fr, KeyAtHost))
	pn.write(on(FocusNone), g.node+" host")
	gap(pn, colSquare-cs.runLen-1).write(dim, spine+g.east)

	gap(pn, colSquare).write(on(FocusSquare), g.wallL+" ")
	pn.write(keyStyle(fr, KeyInSquare, on), keyIf(g, fr, KeyInSquare))
	say := g.quiet
	if fr.Speaking {
		say = g.speaking
	}
	pn.write(on(FocusSay), say+" ")
	// Measured from where the pen actually is, and clipped against the LANES
	// rather than the hop column: the columns are a floor, so a long harness
	// name pushes the hop right rather than being cut to fit a slot. The only
	// thing that must never happen is running under the lanes, because that
	// corrupts the figure instead of shortening a label.
	pn.write(on(FocusSquare), fit(fr.Square, pn.col(), colLanes-14)+" "+g.wallR)

	if fr.Hop == "" {
		// The one shape with nothing in the path. The columns are held open
		// rather than closed up, so the figure does not shift sideways between
		// one tier and the next — the spine simply runs through where a hop
		// would have been.
		start := pn.col() + 1
		gap(pn, colSquare).write(dim, strings.Repeat(g.spine, maxInt(cs.runLen, colLanes-start-2))+g.east)
	} else {
		gap(pn, colHop-cs.runLen-1).write(dim, run(fr.Lane)+g.east)
		// The key is budgeted BEFORE the label is clipped, not written after it.
		// Writing it afterwards left it unaccounted for, and the ASCII key — one
		// column wider than a devicon — ran into the lanes.
		k := keyIf(g, fr, KeyAtHop)
		room := colLanes - textWidth(k)
		if k != "" {
			room-- // the space between the label and the key
		}
		gap(pn, colHop).write(on(FocusHop), g.node+" "+fit(fr.Hop, pn.col()+3, room))
		if k != "" {
			pn.write(tcell.StyleDefault, " ").write(keyStyle(fr, KeyAtHop, on), k)
		}
	}

	// Row 3 — the interface hanging off the square and returning to the host.
	rt := newPen(s, colHost, top+2)
	rt.write(on(FocusReturn), g.north).padTo(colHost+3).
		write(on(FocusReturn), g.west+strings.Repeat(g.screened, colSquare-colHost-4))
	gap(rt, colSquare).
		write(on(FocusReturn), g.node+" "+fit(fr.Interface, rt.col()+3, colLanes-2)+" "+g.up)

	// The lanes, stacked so the spine's own lane is the middle one.
	drawLanes(s, top-1, colLanes, fr, g, on(FocusHop), p.warn, tick, cs.runLen)

	// The caption, last, clipped rather than wrapped: it is one sentence by
	// construction and a second line would cost a row nobody reserved.
	// Both branches yield a WIDTH, never an absolute column: mixing the two
	// silently clipped the block's caption two columns short of what it drew
	// before the pane existed.
	w, _ := s.Size()
	caption, room := fr.Caption, w-colHost-1
	if cs.headCaption {
		caption, room = captionHead(caption), cs.width
	}
	newPen(s, colHost, top+3).write(p.aside, clip(caption, room))
}

// drawLanes stacks the surviving lanes and the refused ones around the spine.
// A pulse rides whichever lane the tick has reached, so motion says which way
// the traffic is going without a caption having to.
func drawLanes(s tcell.Screen, y0, col int, fr Frame, g glyphSet, lit, dim tcell.Style, tick, runLen int) {
	rule := g.lane(fr.Lane)
	rows := []int{y0 + 1, y0 + 2, y0 + 3}
	total := fr.Open + fr.Refused
	for i := 0; i < total && i < len(rows); i++ {
		pn := newPen(s, col, rows[i])
		run := strings.Repeat(rule, runLen)
		if tick > 0 && (tick/2)%3 == i && g.pulse != "" {
			// The mote replaces a rule rather than joining the run, so a lane is
			// the same width moving as at rest. On the pane, where a run is one
			// rule wide, the mote IS the lane for that frame — which is the most a
			// single column can say.
			r := []rune(run)
			r[len(r)/2] = []rune(g.pulse)[0]
			run = string(r)
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
