// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// Frame is one still picture of the run the form currently describes: where the
// credential rests, which hop is in the path, how many lanes survive it. A
// projection of Selection, built at paint time, never a second state.
type Frame struct {
	// Host and HostOS name the machine the operator is on, one under the other:
	// the account, then the platform. "host" said only that a host existed.
	Host      string
	HostOS    string
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

// The ZERO value is the screened hop, deliberately: a security figure must not
// fail to the most permissive reading of itself.
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

// GlyphTier is how much decoration the terminal is trusted with. Emoji are
// deliberately absent — their column counts are ambiguous.
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
	// node is a point in the topology and wall encloses the container — both the
	// BANNER's vocabulary, not ASCII punctuation.
	node, wallL, wallR string
	// The container's frame, in the banner's own set: the mark is a bracketed
	// frame with dots interrupting its rules, and the figure encloses the agent
	// the same way.
	cornerTL, cornerTR, cornerBL, cornerBR, tee, cross, rule string
}

func glyphsFor(t GlyphTier) glyphSet {
	if t == GlyphsASCII {
		return glyphSet{
			// "K", not "o-": the node glyph is "o", so "o-" is indistinguishable
			// from a dot sitting on a rule — which is most of this figure.
			cloud: "(~)", key: "K", quiet: "\"", speaking: "\"!",
			spine: "=", screened: "-", watched: ".",
			refused: "x", asking: "?",
			east: ">", west: "<", north: "^", up: "^", pulse: "*",
			node: "o", wallL: "|", wallR: "|",
			cornerTL: "+", cornerTR: "+", cornerBL: "+", cornerBR: "+", tee: "+", cross: "+", rule: "-",
		}
	}
	return glyphSet{
		cloud: "", key: "", quiet: "", speaking: "",
		spine: "═", screened: "─", watched: "╌",
		refused: "×", asking: "?",
		east: "▸", west: "◂", north: "▴", up: "↑", pulse: "•",
		node: "●", wallL: "│", wallR: "│",
		cornerTL: "┌", cornerTR: "┐", cornerBL: "└", cornerBR: "┘", tee: "┬", cross: "┼", rule: "─",
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

// topoCols is the figure's column set, relative to its origin. A struct so the
// columns move TOGETHER.
type topoCols struct {
	host, box, hop, lanes int
	boxW                  int  // the container's inner width, walls excluded
	width                 int  // columns the whole figure occupies
	runLen                int  // rule characters in a connector run
	asksLabel             bool // whether review spells out "asks you"
	headCaption           bool // caption's leading clause only
}

var blockCols = topoCols{
	host: 2, box: 13, boxW: 26, hop: 46, lanes: 57,
	width: stripCols, runLen: 3, asksLabel: true,
}

// paneCols is the same figure in the margin beside the rows, dropping the two
// things that cost columns and carry least.
var paneCols = topoCols{
	host: 1, box: 11, boxW: 26, hop: 44, lanes: 55,
	width: 62, runLen: 1, headCaption: true,
}

// The figure is seven rows: the question, the container's four, the return, and
// the caption. Every node outside the container names itself on the row beneath
// its dot, so a node costs two rows and the tallest column sets the height.
const (
	figureRows = 7
	paneRows   = figureRows
)

// drawTopology paints the block figure at y0.
func drawTopology(s tcell.Screen, y0 int, fr Frame, tier GlyphTier, p palette, tick int) {
	drawFigure(s, 0, y0, blockCols, fr, tier, p, tick)
}

// captionHead is the caption's leading clause — "allow-all · broker" — which is
// the pane's whole caption. The block prints the sentence; the pane has room
// only for the two facts it opens with.
func captionHead(caption string) string {
	if i := strings.Index(caption, " — "); i > 0 {
		return caption[:i]
	}
	return caption
}

// drawFigure paints the figure with its origin at (x0, y0), in one of the two
// column sets. What runs INSIDE the agent's container is drawn inside the frame.
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

	colHost, boxL := x0+cs.host, x0+cs.box
	colHop, colLanes := x0+cs.hop, x0+cs.lanes
	boxR := boxL + cs.boxW + 1
	boxMid := boxL + (boxR-boxL)/2

	// at centres text on a column, clamped to the figure's origin.
	at := func(row, col, floor int, style tcell.Style, text string) *pen {
		x := col - textWidth(text)/2
		if x < floor {
			x = floor
		}
		return newPen(s, x, y0+row).write(style, text)
	}
	fit := func(text string, room int) string {
		for room > 0 && textWidth(text) > room {
			r := []rune(text)
			if len(r) <= 1 {
				return ""
			}
			text = string(r[:len(r)-1])
		}
		if room <= 0 {
			return ""
		}
		return text
	}
	// row centres one line of the container's inside: a node dot, then its name,
	// padded out to the wall so the frame is square whatever the name's length.
	inside := func(row int, dot, text, key string, dotStyle, textStyle tcell.Style) {
		pn := newPen(s, boxL, y0+row).write(on(FocusSquare), g.wallL+" ")
		pn.write(dotStyle, dot).write(textStyle, " ")
		pn.write(keyStyle(fr, KeyInSquare, on), key)
		pn.write(textStyle, fit(text, boxR-pn.col()-1))
		pn.padTo(boxR).write(on(FocusSquare), g.wallR)
	}

	// r0 — the question walking back to the operator. Only review has one: it is
	// the tier with somebody to ask.
	if fr.Lane == LaneAsked {
		pn := newPen(s, colHost, y0).write(on(FocusHop), g.asking+" "+g.north)
		pn.padTo(boxL).write(dim, g.west+strings.Repeat(g.screened, colHop-boxL-2))
		pn.padTo(colHop).write(on(FocusHop), g.asking)
		if cs.asksLabel {
			pn.write(on(FocusHop), " asks you")
		}
	}

	// r1 — the lid.
	newPen(s, boxL, y0+1).write(on(FocusSquare),
		g.cornerTL+strings.Repeat(g.rule, boxR-boxL-1)+g.cornerTR)

	// r2 — the spine: the host dot, the harness inside its frame, the hop dot.
	pn := newPen(s, colHost, y0+2)
	pn.write(keyStyle(fr, KeyAtHost, on), keyIf(g, fr, KeyAtHost))
	pn.write(on(FocusNone), g.node).write(dim, " ")
	pn.write(dim, strings.Repeat(g.spine, boxL-pn.col()))
	say := g.quiet
	if fr.Speaking {
		say = g.speaking
	}
	inside(2, say, fr.Square, keyIf(g, fr, KeyInSquare), on(FocusSay), on(FocusSquare))

	spine := newPen(s, boxR+1, y0+2)
	if fr.Hop == "" {
		// The one shape with nothing in the path. The columns are held open
		// rather than closed up, so the figure does not shift between tiers.
		spine.write(dim, strings.Repeat(g.spine, colLanes-boxR-2))
	} else {
		spine.write(dim, strings.Repeat(g.lane(fr.Lane), colHop-boxR-1))
		spine.padTo(colHop).write(on(FocusHop), g.node)
		// ...and on to the fan, but only when there ARE lanes: a rule to an empty
		// margin would draw the route the tier is withholding.
		if fr.Open+fr.Refused > 0 {
			spine.write(dim, strings.Repeat(g.lane(fr.Lane), colLanes-spine.col()))
		}
	}

	// r3 — what the agent drives, inside the frame with it; and the two outside
	// nodes naming themselves under their dots.
	inside(3, g.node, fr.Interface, "", on(FocusReturn), on(FocusReturn))
	at(3, colHost, x0, on(FocusNone), fit(fr.Host, boxL-x0-1))
	if fr.Hop != "" {
		// Floored past the frame's right wall: a hop label long enough to centre
		// on top of the wall would erase it, and the wall is the thing saying
		// where the container ends.
		label := fit(fr.Hop, colLanes-boxR-3)
		p3 := at(3, colHop, boxR+2, on(FocusHop), label)
		if k := keyIf(g, fr, KeyAtHop); k != "" {
			p3.write(dim, " ").write(keyStyle(fr, KeyAtHop, on), k)
		}
	}

	// r4 — the floor, with the tee the return leaves by; and under the host's
	// name, the platform it is. Two rows for one node, because "host" alone said
	// only that a host existed.
	at(4, colHost, x0, p.aside, fit(fr.HostOS, boxL-x0-1))
	floor := newPen(s, boxL, y0+4).write(on(FocusSquare), g.cornerBL)
	floor.write(on(FocusSquare), strings.Repeat(g.rule, boxMid-boxL-1)+g.tee)
	floor.write(on(FocusSquare), strings.Repeat(g.rule, boxR-boxMid-1)+g.cornerBR)

	// r5 — the return: an elbow up to the host and up into the frame's tee.
	newPen(s, colHost, y0+5).write(on(FocusReturn),
		g.cornerBL+strings.Repeat(g.rule, boxMid-colHost-1)+g.cornerBR)

	// The lanes branch off the hop rather than floating beside it.
	drawLanes(s, y0, colLanes, fr, g, on(FocusHop), p.warn, tick, cs.runLen)

	w, _ := s.Size()
	caption, room := fr.Caption, w-colHost-1
	if cs.headCaption {
		caption, room = captionHead(caption), cs.width
	}
	newPen(s, colHost, y0+6).write(p.aside, clip(caption, room))
}

// drawLanes stacks the surviving lanes and the refused ones around the spine.
// A pulse rides whichever lane the tick has reached, so motion says which way
// the traffic is going without a caption having to.
func drawLanes(s tcell.Screen, y0, col int, fr Frame, g glyphSet, lit, dim tcell.Style, tick, runLen int) {
	rule := g.lane(fr.Lane)
	rows := []int{y0 + 1, y0 + 2, y0 + 3}
	total := fr.Open + fr.Refused
	// The fan is ATTACHED to the hop it leaves — a tee on the dot's own row and
	// corners above and below it — rather than three runs floating beside it.
	branch := []string{g.cornerTL, g.cross, g.cornerBL}
	if total == 1 {
		branch = []string{g.rule, g.rule, g.rule}
	}
	for i := 0; i < total && i < len(rows); i++ {
		pn := newPen(s, col, rows[i])
		pn.write(dim, branch[i])
		run := strings.Repeat(rule, runLen)
		if tick > 0 && (tick/2)%3 == i && g.pulse != "" {
			// The mote replaces a rule rather than joining the run, so a lane is
			// the same width moving as at rest.
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
