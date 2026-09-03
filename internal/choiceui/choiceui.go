// Package choiceui renders the one-shot harness choice prompt.
// SPEC: _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/agentsettings/choice-cache.puml
package choiceui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

type Row struct {
	Label    string
	Options  []string
	Selected int
	Locked   bool
	// Hover is the option the cursor is on while the row is LOCKED, separate from
	// Selected because there Selected is a fact, not a choice.
	Hover  int
	Reason string
	Multi  bool
	On     []bool
	Off    []bool
	// Help is what an option DOES, keyed by the option itself rather than indexed:
	// callers assemble Options conditionally.
	Help map[string]string
	// OffWhy is why one option is greyed, keyed the same way. Reason states the
	// row's constraints at a glance; this states one option's, in full, next to
	// its description.
	OffWhy map[string]string
	// Radio draws a MULTI row's boxes as radio marks: parentheses say "one of
	// these", brackets say "any number of these".
	Radio bool
	// Divider draws the row's name as a centered heading instead of a left-column
	// label. Declared rather than inferred, so more than one group can be named.
	Divider bool
}

// cursorAt is the option the cursor is on: Selected where the operator can
// change it, Hover where the row is locked.
func (r *Row) cursorAt() int {
	if r.Locked {
		return r.Hover
	}
	return r.Selected
}

// hoverable reports whether the cursor may REST on this row without being able
// to change it.
func (r *Row) hoverable() bool { return r.Locked && len(r.Options) > 0 }

// helpLine is one line under the cursor's row: what the option the cursor is on
// means, and why it cannot be picked.
type helpLine struct {
	text string
	warn bool
}

// helpLines describes the option the cursor is on, wrapped to width and drawn
// BELOW the row rather than appended to it.
func (r *Row) helpLines(width int) []helpLine {
	idx := r.cursorAt()
	if idx < 0 || idx >= len(r.Options) {
		return nil
	}
	opt := r.Options[idx]
	var out []helpLine
	// A locked row's Reason is the whole point of stopping on it, and inline it is
	// CLIPPED to the row's width — which used to destroy the only copy of the one
	// runnable command for changing a host-wide baseline. Here it is unclipped.
	if r.Locked && r.Reason != "" {
		for _, l := range wrap(r.Reason, width, 2) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	// Guarded on the description: an option with none must stay silent, or the
	// block reads "› allowlist —" and explains nothing.
	if h := r.Help[opt]; h != "" {
		for _, l := range wrap("› "+opt+" — "+h, width, 2) {
			out = append(out, helpLine{text: l})
		}
	}
	// Guarded on the reason, not on the label: "off: " alone is still one field,
	// so an available option printed a bare "off:" under itself.
	if why := r.OffWhy[opt]; why != "" {
		// A greyed box that is TICKED is not unavailable, it is compulsory, and
		// labelling it "off:" would say the opposite of what the checkbox shows.
		label := "off: "
		if r.onAt(idx) {
			label = "always on: "
		}
		for _, l := range wrap(label+why, width, len([]rune(label))) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	// The block must never show LESS than the inline copy it replaces. When the
	// hovered option had nothing of its own to say, the row's reason stands in —
	// unclipped, which the inline copy never was.
	if len(out) == 0 && r.Reason != "" {
		for _, l := range wrap(r.Reason, width, 2) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	return out
}

// wrap breaks text on spaces into lines of at most width runes, indenting every
// line after the first so a continuation reads as one. Empty text yields no
// lines, which is what makes an option with no help simply silent.
func wrap(text string, width, indent int) []string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	if width < 20 {
		width = 20 // narrower than any sentence; wrap rather than refuse
	}
	pad := strings.Repeat(" ", indent)
	var out []string
	line := fields[0]
	for _, w := range fields[1:] {
		if len([]rune(line))+1+len([]rune(w)) > width {
			out = append(out, line)
			line = pad + w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}

// clip shortens text to width and marks the cut. tcell drops runes past the last
// column in silence, so an over-long reason simply stopped mid-word with nothing
// saying it had been truncated.
func clip(text string, width int) string {
	runes := []rune(text)
	switch {
	case width <= 0:
		return ""
	case len(runes) <= width:
		return text
	case width == 1:
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func (r *Row) offAt(i int) bool { return i < len(r.Off) && r.Off[i] }

func (r *Row) onAt(i int) bool { return i < len(r.On) && r.On[i] }

type Form struct {
	Banner   []string
	Title    string
	Header   []string
	Rows     []Row
	OnChange func(*Form)
	// Glyphs is how much decoration the terminal is trusted with — a capability
	// of the session, so it is constant for the run.
	Glyphs GlyphTier
	// Topology projects the form onto the picture under the help block. Called at
	// PAINT time on every frame, and must be pure. Nil means no strip.
	Topology func(f *Form, cursor int) *Frame
	// scroll is the body's first visible line. It is a CACHE and never a source
	// of truth: every paint re-clamps it and re-satisfies the cursor margin, so
	// a form drawn twice at different sizes cannot inherit a stale offset.
	scroll int
}

func Banner() []string {
	return append(strings.Split(ui.BrandBanner, "\n"), "  "+ui.BrandTagline)
}

func (f *Form) Selections(label string) []string {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != label || !r.Multi {
			continue
		}
		var out []string
		for j, opt := range r.Options {
			if r.onAt(j) && !r.offAt(j) {
				out = append(out, opt)
			}
		}
		return out
	}
	return nil
}

func (f *Form) Selection(label string) string {
	for _, r := range f.Rows {
		if r.Label == label && r.Selected >= 0 && r.Selected < len(r.Options) {
			return r.Options[r.Selected]
		}
	}
	return ""
}

func (f *Form) Run() (confirmed bool, err error) {
	screen, err := tcell.NewScreen()
	if err != nil {
		return false, fmt.Errorf("choice prompt: %w", err)
	}
	if err := screen.Init(); err != nil {
		return false, fmt.Errorf("choice prompt: %w", err)
	}
	// Registered FIRST, so it runs LAST: the ticker must be stopped before the
	// screen it posts into is torn down, and defers unwind in reverse.
	defer screen.Fini()
	tick := newTicker(screen.PostEvent)
	defer tick.stop()

	cursor := f.firstSelectable()
	for {
		f.draw(screen, cursor, tick.frame())
		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventKey:
			// One unconditional bump, before the key switch: no path can miss it,
			// and a keystroke that changes nothing costs only a second of frames.
			tick.bump()
			// On a screen too small to draw the form, only leaving it is allowed.
			if !f.navigable(screen) {
				switch ev.Key() {
				case tcell.KeyEscape, tcell.KeyCtrlC:
					return false, nil
				case tcell.KeyEnter:
					return true, nil
				case tcell.KeyRune:
					if ev.Rune() == 'q' {
						return false, nil
					}
				}
				continue
			}
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC:
				return false, nil
			case tcell.KeyEnter:
				return true, nil
			case tcell.KeyTab:
				f.toggle(cursor)
			case tcell.KeyUp:
				cursor = f.move(cursor, -1)
			case tcell.KeyDown:
				cursor = f.move(cursor, +1)
			// Paging moves the CURSOR, never the window on its own: the body has
			// nothing to read but rows, so a viewport that could drift away from
			// the cursor would make the next arrow key teleport.
			case tcell.KeyPgUp:
				cursor = f.page(cursor, -1)
			case tcell.KeyPgDn:
				cursor = f.page(cursor, +1)
			case tcell.KeyHome:
				cursor = f.firstSelectable()
			case tcell.KeyEnd:
				cursor = f.lastSelectable()
			case tcell.KeyLeft:
				f.cycle(cursor, -1)
			case tcell.KeyRight:
				f.cycle(cursor, +1)
			case tcell.KeyRune:
				switch ev.Rune() {
				case 'k':
					cursor = f.move(cursor, -1)
				case 'j':
					cursor = f.move(cursor, +1)
				case 'h':
					f.cycle(cursor, -1)
				case 'l':
					f.cycle(cursor, +1)
				case ' ':
					if cursor < len(f.Rows) && f.Rows[cursor].Multi {
						f.toggle(cursor)
					} else {
						f.cycle(cursor, +1)
					}
				case 'q':
					return false, nil
				}
			}
		}
	}
}

func (f *Form) axisLabel() bool {
	for _, r := range f.Rows {
		if !r.Multi && len(r.Options) > 1 {
			return true
		}
	}
	return false
}

func (f *Form) firstSelectable() int {
	for i, r := range f.Rows {
		if !r.Locked {
			return i
		}
	}
	return 0
}

func (f *Form) move(cursor, delta int) int {
	for i := cursor + delta; i >= 0 && i < len(f.Rows); i += delta {
		if !f.Rows[i].Locked || f.Rows[i].hoverable() {
			return i
		}
	}
	return cursor
}

func (f *Form) cycle(cursor, delta int) {
	if cursor < 0 || cursor >= len(f.Rows) {
		return
	}
	r := &f.Rows[cursor]
	if len(r.Options) == 0 {
		return
	}
	n := len(r.Options)
	if r.Locked {
		// Reading, not choosing: every option is off, so nothing is skipped and
		// nothing is reported as changed.
		r.Hover = ((r.Hover+delta)%n + n) % n
		return
	}
	for step := 1; step <= n; step++ {
		next := ((r.Selected+delta*step)%n + n) % n
		// A single-select row's Selected IS the choice, so it may never rest on a
		// gated option. On a MULTI row it is only the cursor, so it may.
		if r.Multi || !r.offAt(next) {
			r.Selected = next
			break
		}
	}
	f.changed()
}

func (f *Form) toggle(cursor int) {
	if cursor < 0 || cursor >= len(f.Rows) {
		return
	}
	r := &f.Rows[cursor]
	if r.Locked || !r.Multi || r.Selected >= len(r.Options) || r.offAt(r.Selected) {
		return
	}
	for len(r.On) < len(r.Options) {
		r.On = append(r.On, false)
	}
	r.On[r.Selected] = !r.On[r.Selected]
	f.changed()
}

func (f *Form) changed() {
	if f.OnChange != nil {
		f.OnChange(f)
	}
}

func hexColor(c int) tcell.Color { return tcell.NewHexColor(int32(c)) }

// palette maps the shared CLI colors onto tcell styles. Background stays the
// terminal default (Clear); contrast comes from light body text, not a custom fill.
type palette struct {
	brand  tcell.Style // cyan — mark, selected / checked
	accent tcell.Style // teal — labels, first-party emphasis
	warn   tcell.Style // lime — attention (reasons, gated)
	body   tcell.Style // light — readable supporting / value text
	title  tcell.Style // bold default fg — section titles
	idle   tcell.Style // unchecked / unfocused options
	aside  tcell.Style // italic — parenthetical descriptions
}

func styles() palette {
	fg := func(c int) tcell.Style {
		return tcell.StyleDefault.Foreground(hexColor(c))
	}
	return palette{
		brand:  fg(ui.ColorBrand).Bold(true),
		accent: fg(ui.ColorAccent).Bold(true),
		warn:   fg(ui.ColorWarn),
		body:   fg(ui.ColorSecondary),
		title:  tcell.StyleDefault.Bold(true),
		idle:   fg(ui.ColorSecondary),
		aside:  fg(ui.ColorSecondary).Italic(true),
	}
}

func (f *Form) draw(s tcell.Screen, cursor, tick int) {
	s.Clear()
	p := styles()
	w, h := s.Size()
	lay := f.layout(w, h)
	if lay.tooSmall {
		// Too small to navigate. The event loop keeps running, so enter and esc
		// still work: a terminal too small to draw the form must never be a
		// terminal the operator cannot leave.
		drawTooSmall(s, w, h, f.minHeight(w))
		s.Show()
		return
	}

	c := newCanvas(s)

	// ── head: droppable, never scrolled ────────────────────────────────────
	if lay.banner {
		for _, b := range f.Banner {
			c.put(0, p.brand, b)
			c.y++
		}
		c.y++
	}
	c.put(0, p.title, f.Title)
	c.y += 2
	if lay.header {
		for _, hl := range f.Header {
			putHeader(c.put, p, hl)
			c.y++
		}
		c.y++
	}
	if lay.axis {
		c.put(bodyIndent+22, p.body, "◀ riskier")
		c.put(bodyIndent+60, p.body, "safer ▶")
		c.y += 2
	}

	// ── body: the only region that scrolls ─────────────────────────────────
	lines := f.bodyLines()
	f.scroll = scrollTo(f.scroll, lay.body, lines, cursor)
	g := newGutter(f.scroll, lay.body, len(lines))
	named := headingsInWindow(lines, f.scroll, lay.body)
	// With the pane on, the rows stop short of it. Nothing is clipped to make room.
	limit := w
	if lay.strip == stripPane {
		limit = lay.pane - paneGutter
	}
	bodyTop := c.y
	c.clipTo(bodyTop, bodyTop+lay.body)
	for i := 0; i < lay.body && f.scroll+i < len(lines); i++ {
		c.y = bodyTop + i
		ln := lines[f.scroll+i]
		if glyph, thumb := g.glyph(i, f.Glyphs == GlyphsASCII); glyph != "" {
			st := p.idle
			if thumb {
				st = p.brand
			}
			c.put(0, st, glyph)
		}
		// A divider row hands its name to the heading above it — but that heading
		// can scroll away, and a group of checkboxes with no name at all is worse
		// than the duplication. When the heading is off screen the label comes back.
		f.drawBodyLine(c, p, ln, cursor, limit, named[ln.row])
	}
	c.unclip()

	// The figure in the margin, anchored to the body's TOP line rather than
	// centred. The guard is the belt to the budget's braces.
	if lay.strip == stripPane && f.Topology != nil && lay.body >= paneRows &&
		bodyTop+paneRows <= h && lay.pane+paneCols.width <= w {
		if fr := f.Topology(f, cursor); fr != nil {
			drawFigure(s, lay.pane, bodyTop, paneCols, *fr, f.Glyphs, p, tick)
		}
	}

	// The blank the rows have always had under them. Dropping it put the hint
	// flush against the last row AND made the budget over-reserve by one, since
	// the arithmetic still charged for it.
	c.y = bodyTop + lay.body + 1

	// ── foot: follows the body, and is never painted over ──────────────────
	hint := "↑↓ row · ←→ choose · enter accept · esc cancel"
	for _, r := range f.Rows {
		if r.Multi {
			hint = "↑↓ row · ←→ move · space toggle · enter accept · esc cancel"
			break
		}
	}
	// The count rides on the hint rather than taking a body line of its own.
	// Clipped BEFORE the count is appended.
	hint = clip(hint, w)
	if n := g.hiddenRows(lines); n > 0 {
		hint = clip(hint+fmt.Sprintf(" · %d more below", n), w)
	}
	c.put(0, p.body, hint)
	c.y++

	// The cursor's option explains itself below the hint, into a slot reserved at
	// its MAXIMUM height. y+2, not y+1: the block opens with a blank line.
	if lay.helpSlot > 0 {
		c.y++ // the blank the help block opens with
		helpTop := c.y
		if cursor >= 0 && cursor < len(f.Rows) {
			for i, line := range f.Rows[cursor].helpLines(w - 4) {
				st := p.body
				if line.warn {
					st = p.warn
				}
				c.y = helpTop + i
				c.put(2, st, line.text)
			}
		}
		c.y = helpTop + lay.helpSlot
	}

	// stripPane was already drawn beside the body and costs the foot nothing.
	if (lay.strip == stripDigest || lay.strip == stripBlock) && f.Topology != nil {
		if fr := f.Topology(f, cursor); fr != nil {
			switch {
			case lay.strip == stripDigest:
				c.y++
				c.put(2, p.aside, clip(fr.Caption, w-3))
			// drawTopology paints through its own pen rather than the canvas, so it has no
			// window to be dropped by.
			case c.y+stripRows <= h:
				drawTopology(s, c.y+1, *fr, f.Glyphs, p, tick)
			}
		}
	}
	s.Show()
}

// drawBodyLine paints one enumerated line of the scrolling body. Splitting it
// out is what lets the body be walked by LINE — the painter no longer decides
// how many lines a row costs, it is told which one to draw.
func (f *Form) drawBodyLine(c *canvas, p palette, ln bodyLine, cursor, limit int, named bool) {
	r := f.Rows[ln.row]
	switch ln.kind {
	case lineBlank:
		return
	case lineHeading:
		label := " " + r.Label + " "
		pad := (72 - len([]rune(label))) / 2
		if pad < 0 {
			pad = 0
		}
		c.put(bodyIndent+pad, p.body, strings.Repeat("─", 6)+label+strings.Repeat("─", 6))
		return
	}

	marker := "  "
	labelStyle := p.accent
	if ln.row == cursor {
		marker = "› "
		labelStyle = p.brand
	}
	rowLabel := r.Label
	if r.Divider && named {
		rowLabel = "" // the heading above it is on screen and already says this
	}
	c.put(bodyIndent, labelStyle, marker+rowLabel)
	x := bodyIndent + 22
	for j, opt := range r.Options {
		var glyph string
		st := p.idle
		switch {
		case r.Multi && r.Radio && r.onAt(j):
			glyph, st = "(•) ", p.brand
		case r.Multi && r.Radio:
			glyph = "( ) "
		case r.Multi && r.onAt(j):
			glyph, st = "[x] ", p.brand
		case r.Multi:
			glyph = "[ ] "
		case j == r.Selected:
			glyph, st = "(•) ", p.brand
		default:
			glyph = "( ) "
		}
		if r.Locked || r.offAt(j) {
			st = p.warn
		}
		if ln.row == cursor && j == r.cursorAt() {
			st = st.Underline(true)
		}
		c.put(x, st, glyph+opt)
		x += len(glyph) + len(opt) + 3
	}
	// The row's Reason is NOT drawn here — the help block is its one home.
}

// headingsInWindow is the set of rows whose divider heading is on screen; only
// those may drop their own label. Built once per frame.
func headingsInWindow(lines []bodyLine, off, window int) map[int]bool {
	named := map[int]bool{}
	for i := off; i < off+window && i < len(lines); i++ {
		if lines[i].kind == lineHeading {
			named[lines[i].row] = true
		}
	}
	return named
}

// navigable reports whether this screen can draw a form the operator can steer.
func (f *Form) navigable(s tcell.Screen) bool {
	w, h := s.Size()
	return !f.layout(w, h).tooSmall
}

// minHeight is the shortest terminal that can still draw a navigable form. It
// takes the WIDTH because the help slot's height depends on it.
func (f *Form) minHeight(w int) int {
	need := 2 + minBodyLines + 2
	if help := f.maxHelpLines(w - 4); help > 0 {
		need += help + 1
	}
	return need
}

// drawTooSmall names the size the prompt needs and draws nothing else. Being
// loud about it is the entire point: the failure this replaces was a form that
// silently lost its last rows and said nothing.
func drawTooSmall(s tcell.Screen, w, h, needH int) {
	p := styles()
	msg := fmt.Sprintf("terminal too small: %dx%d, need %d rows", w, h, needH)
	c := newCanvas(s)
	c.put(0, p.warn, clip(msg, w))
	if h > 1 {
		c.y = 1
		c.put(0, p.body, clip("enter accepts the resolved values · esc cancels", w))
	}
}

// putHeader styles "label: value" and "KEY=value" lines with accent labels. Both
// forms can appear on one line — the models row is "models:   main=… small=…" — so
// the leading label and every KEY= pair after it are accented by the same pass.
func putHeader(put func(int, tcell.Style, string), p palette, line string) {
	const x0 = 2
	runes := []rune(line)
	i := 0
	for i < len(runes) && unicode.IsSpace(runes[i]) {
		i++
	}
	indent := i
	rest := string(runes[i:])

	if colon := strings.IndexByte(rest, ':'); colon > 0 && !strings.ContainsRune(rest[:colon], ' ') {
		put(x0, p.accent, string(runes[:indent])+rest[:colon+1])
		putPairs(put, p, x0+indent+colon+1, rest[colon+1:])
		return
	}
	put(x0, p.body, string(runes[:indent]))
	putPairs(put, p, x0+indent, rest)
}

// putPairs writes s from column col, styling each whitespace-delimited token by what
// it is rather than by which line it came from:
//
//	symbolic token      accent — the lsp: row's devicons and ASCII category markers
//	KEY of "KEY=value"  accent — the llms: row carries several pairs on one line
//	token after "on"    accent — the branch in "git: <repo> on <branch>"
//	a "(…)" run         italic — a description trailing the fact it describes
//
// Accenting only the first pair reads as that value mattering more than the rest
// rather than as one uniform kind of value, which is how the models row lost the
// highlight on every slot after the leading one.
func putPairs(put func(int, tcell.Style, string), p palette, col int, s string) {
	rs := []rune(s)
	aside, afterOn := false, false
	for i := 0; i < len(rs); {
		j := i
		if unicode.IsSpace(rs[i]) {
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
			st := p.body
			if aside {
				st = p.aside
			}
			put(col, st, string(rs[i:j]))
			col += j - i
			i = j
			continue
		}
		for j < len(rs) && !unicode.IsSpace(rs[j]) {
			j++
		}
		tok := rs[i:j]
		word := string(tok)
		if strings.HasPrefix(word, "(") {
			aside = true
		}
		switch {
		case aside:
			put(col, p.aside, word)
		case isGlyph(tok):
			put(col, p.accent, word)
		case afterOn:
			put(col, p.accent, word)
		case keyEnd(tok) > 0:
			k := keyEnd(tok)
			put(col, p.accent, string(tok[:k]))
			put(col+k, p.body, string(tok[k:]))
		default:
			put(col, p.body, word)
		}
		if aside && strings.HasSuffix(word, ")") {
			aside = false
		}
		afterOn = word == "on"
		col += j - i
		i = j
	}
}

// isGlyph reports whether a token is purely symbolic — a Nerd Font devicon lives in
// the private-use area, which is neither letter nor digit, and so do "<>", "$", "#"
// and "{}". Anything carrying a letter or digit is content and stays body-styled.
func isGlyph(tok []rune) bool {
	if len(tok) == 0 {
		return false
	}
	for _, r := range tok {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// keyEnd reports the length of the leading KEY in a "KEY=value" token, or 0 when the
// token is not one. Values may themselves contain '=', so the key must look like an
// identifier for the token to count.
func keyEnd(tok []rune) int {
	for i, r := range tok {
		if r == '=' {
			return i
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return 0
		}
	}
	return 0
}

func EnvHeader(secretNames []string, settings map[string]string) []string {
	var out []string
	if len(secretNames) > 0 {
		out = append(out, "keys:     "+strings.Join(secretNames, "  "))
	}
	for _, k := range sortedKeys(settings) {
		out = append(out, fmt.Sprintf("%-9s %s=%s", "", k, settings[k]))
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
