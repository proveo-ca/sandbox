// SPEC: _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/wireframe.puml
// Package choiceui renders the one-shot harness choice prompt.
package choiceui

import (
	"fmt"
	"os"
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
	Hover    int
	Reason   string
	Multi    bool
	On       []bool
	Off      []bool
	Help     map[string]string
	OffWhy   map[string]string
	Radio    bool
	Divider  bool

	// A field row takes typing instead of options.
	// SPEC: _spec/cmd/proveo/init-credential-wireframe.puml
	Field       bool
	Heading     string            // the divider's text, when the group is not named for this row
	Cols        []int             // 0-based column of each option; nil packs left to right
	Paint       map[string]string // option → cell text, when the cell is shorter than the option
	More        *More             // multi-select list opened from More.Cell
	Value       string
	Placeholder string
	Masked      bool
	Held        bool
	Warn        bool
}

// More is the multi-select list one cell of a four-column row opens.
// SPEC: _spec/internal/devports/dev-ports.puml
type More struct {
	Cell    int
	Options []string
	On      []bool
	Off     []bool // greyed entries; Row.OffWhy names why
	At      int
}

func (m *More) visible(r Row) bool {
	return m != nil && r.Selected == m.Cell && !r.offAt(m.Cell) && len(m.Options) > 0
}

func (m *More) anyOn() bool {
	for i, on := range m.On {
		if on && !m.offAt(i) {
			return true
		}
	}
	return false
}

func (m *More) offAt(i int) bool { return i < len(m.Off) && m.Off[i] }

// columnText is the cell drawn for opt: Paint when set, else the option, at most 10 runes.
func (r Row) columnText(opt string) string {
	if s, ok := r.Paint[opt]; ok && s != "" {
		return clipRunes(s, columnRunes)
	}
	return clipRunes(opt, columnRunes)
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

func (r *Row) cursorAt() int {
	if r.Locked {
		return r.Hover
	}
	return r.Selected
}

func (r *Row) hoverable() bool { return r.Locked && len(r.Options) > 0 }

type helpLine struct {
	text string
	warn bool
}

func (r *Row) helpLines(width int) []helpLine {
	idx := r.cursorAt()
	if idx < 0 || idx >= len(r.Options) {
		return nil
	}
	if r.More.visible(*r) {
		return r.moreHelp(width)
	}
	opt := r.Options[idx]
	var out []helpLine
	if r.Locked && r.Reason != "" {
		for _, l := range wrap(r.Reason, width, 2) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	if h := r.Help[opt]; h != "" {
		for _, l := range wrap("› "+opt+" — "+h, width, 2) {
			out = append(out, helpLine{text: l})
		}
	}
	if why := r.OffWhy[opt]; why != "" {
		label := "off: "
		if r.onAt(idx) {
			label = "always on: "
		}
		for _, l := range wrap(label+why, width, len([]rune(label))) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	if r.Reason != "" && (len(out) == 0 || r.unreachableGate()) {
		for _, l := range wrap(r.Reason, width, 2) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	return out
}

func (r *Row) moreHelp(width int) []helpLine {
	m := r.More
	if m.At < 0 || m.At >= len(m.Options) {
		return nil
	}
	opt := m.Options[m.At]
	h := r.Help[opt]
	if h == "" {
		h = "publish this port"
	}
	var out []helpLine
	for _, l := range wrap("› "+opt+" — "+h, width, 2) {
		out = append(out, helpLine{text: l})
	}
	if why := r.OffWhy[opt]; why != "" && m.offAt(m.At) {
		for _, l := range wrap("off: "+why, width, len("off: ")) {
			out = append(out, helpLine{text: l, warn: true})
		}
	}
	return out
}

func (r *Row) unreachableGate() bool {
	if r.Multi || r.Locked {
		return false
	}
	for i := range r.Options {
		if r.offAt(i) {
			return true
		}
	}
	return false
}

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
	Glyphs   GlyphTier
	Topology func(f *Form, cursor int) *Frame
	// NoAxis hides "◀ riskier … safer ▶". The legend is a claim about policy
	// radios (egress, credentials). Init's radios are not a safety axis.
	NoAxis      bool
	StartRow    string // row label the cursor opens on; empty means first selectable
	StartOption string // option the cursor opens on within StartRow
	scroll      int
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
			if r.More != nil && j == r.More.Cell {
				continue
			}
			if r.onAt(j) && !r.offAt(j) {
				out = append(out, opt)
			}
		}
		if r.More != nil {
			for j, opt := range r.More.Options {
				if j < len(r.More.On) && r.More.On[j] && !r.More.offAt(j) {
					out = append(out, opt)
				}
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
	preferUTF8Locale()
	screen, err := tcell.NewScreen()
	if err != nil {
		return false, fmt.Errorf("choice prompt: %w", err)
	}
	if err := screen.Init(); err != nil {
		return false, fmt.Errorf("choice prompt: %w", err)
	}
	registerGlyphFallbacks(screen)
	defer screen.Fini()
	tick := newTicker(screen.PostEvent)
	defer tick.stop()

	cursor := f.startCursor()
	for {
		f.draw(screen, cursor, tick.frame())
		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventKey:
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
			if f.typing(cursor, ev) {
				continue
			}
			if f.moreKey(cursor, ev) {
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

// typing routes a keystroke into a field row, and answers true when the key
// belonged to it.
func (f *Form) typing(cursor int, ev *tcell.EventKey) bool {
	if cursor < 0 || cursor >= len(f.Rows) || !f.Rows[cursor].Field {
		return false
	}
	r := &f.Rows[cursor]
	switch ev.Key() {
	case tcell.KeyRune:
		r.Value += string(ev.Rune())
		f.changed()
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if runes := []rune(r.Value); len(runes) > 0 {
			r.Value = string(runes[:len(runes)-1])
			f.changed()
		}
		return true
	case tcell.KeyCtrlU:
		r.Value = ""
		f.changed()
		return true
	}
	return false
}

// Value is what was typed into a field row, empty when it was left alone.
func (f *Form) FieldValue(label string) string {
	for _, r := range f.Rows {
		if r.Field && r.Label == label {
			return r.Value
		}
	}
	return ""
}

func (f *Form) axisLabel() bool {
	if f.NoAxis {
		return false
	}
	for _, r := range f.Rows {
		if !r.Multi && len(r.Options) > 1 {
			return true
		}
	}
	return false
}

// startCursor opens on StartRow/StartOption when the form names them.
func (f *Form) startCursor() int {
	if f.StartRow != "" {
		for i := range f.Rows {
			if f.Rows[i].Label != f.StartRow {
				continue
			}
			for j, opt := range f.Rows[i].Options {
				if opt == f.StartOption {
					f.Rows[i].Selected = j
					break
				}
			}
			return i
		}
	}
	return f.firstSelectable()
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
		r.Hover = ((r.Hover+delta)%n + n) % n
		return
	}
	for step := 1; step <= n; step++ {
		next := ((r.Selected+delta*step)%n + n) % n
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
	if r.More != nil && r.Selected == r.More.Cell {
		return
	}
	if r.Locked || !r.Multi || r.Selected >= len(r.Options) || r.offAt(r.Selected) {
		return
	}
	for len(r.On) < len(r.Options) {
		r.On = append(r.On, false)
	}
	r.On[r.Selected] = !r.On[r.Selected]
	f.changed()
}

// moreKey drives the open list. It answers true when the key stayed inside it.
func (f *Form) moreKey(cursor int, ev *tcell.EventKey) bool {
	r, ok := f.moreRow(cursor)
	if !ok {
		return false
	}
	m := r.More
	n := len(m.Options)
	switch ev.Key() {
	case tcell.KeyDown:
		if m.At+1 < n {
			m.At++
			return true
		}
	case tcell.KeyUp:
		if m.At > 0 {
			m.At--
			return true
		}
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'j':
			if m.At+1 < n {
				m.At++
				return true
			}
		case 'k':
			if m.At > 0 {
				m.At--
				return true
			}
		case ' ':
			f.toggleMore(r)
			return true
		}
	}
	return false
}

func (f *Form) moreRow(cursor int) (*Row, bool) {
	if cursor < 0 || cursor >= len(f.Rows) {
		return nil, false
	}
	r := &f.Rows[cursor]
	if !r.More.visible(*r) {
		return nil, false
	}
	return r, true
}

func (f *Form) toggleMore(r *Row) {
	m := r.More
	if m.offAt(m.At) {
		return
	}
	for len(m.On) < len(m.Options) {
		m.On = append(m.On, false)
	}
	m.On[m.At] = !m.On[m.At]
	for len(r.On) <= m.Cell {
		r.On = append(r.On, false)
	}
	r.On[m.Cell] = m.anyOn()
	f.changed()
}

func (f *Form) changed() {
	if f.OnChange != nil {
		f.OnChange(f)
	}
}

func hexColor(c int) tcell.Color { return tcell.NewHexColor(int32(c)) }

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
		drawTooSmall(s, w, h, f.minHeight(w))
		s.Show()
		return
	}

	c := newCanvas(s)

	if lay.banner {
		c.y++
		if n := len(f.Banner); n > 0 {
			for _, b := range f.Banner[:n-1] {
				c.put(0, p.brand, b)
				c.y++
			}
			c.put(0, p.body, f.Banner[n-1])
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
		c.put(optOrigin, p.body, "◀ riskier")
		c.put(optOrigin+3*colPitch, p.body, "safer ▶")
		c.y += 2
	}

	lines := f.bodyLines()
	f.scroll = scrollTo(f.scroll, lay.body, lines, cursor)
	g := newGutter(f.scroll, lay.body, len(lines))
	named := headingsInWindow(lines, f.scroll, lay.body)
	limit := w
	if lay.strip == stripPane {
		limit = lay.pane - paneGutter
	}
	bodyTop := c.y
	c.clipTo(bodyTop, bodyTop+lay.body)
	dropY := -1
	for i := 0; i < lay.body && f.scroll+i < len(lines); i++ {
		c.y = bodyTop + i
		ln := lines[f.scroll+i]
		if ln.kind == lineRow && ln.row == cursor && f.Rows[ln.row].More.visible(f.Rows[ln.row]) {
			dropY = c.y
		}
		if glyph, thumb := g.glyph(i, f.Glyphs == GlyphsASCII || f.Glyphs == GlyphsOff); glyph != "" {
			st := p.idle
			if thumb {
				st = p.brand
			}
			c.put(0, st, glyph)
		}
		f.drawBodyLine(c, p, ln, cursor, limit, named[ln.row])
	}
	c.unclip()

	if lay.strip == stripPane && f.Topology != nil && lay.body >= paneRows &&
		bodyTop+paneRows <= h && lay.pane+paneCols.width <= w {
		if fr := f.Topology(f, cursor); fr != nil {
			drawFigure(s, lay.pane, bodyTop, paneCols, *fr, f.Glyphs, p, tick)
		}
	}
	if dropY >= 0 {
		f.drawMore(c, p, cursor, optOrigin+3*colPitch, dropY+1)
	}

	c.y = bodyTop + lay.body + 1

	hint := "↑↓ row · ←→ choose · enter accept · esc cancel"
	for _, r := range f.Rows {
		if r.Multi {
			hint = "↑↓ row · ←→ move · space toggle · enter accept · esc cancel"
			break
		}
	}
	hint = clip(hint, w)
	if n := g.hiddenRows(lines); n > 0 {
		hint = clip(hint+fmt.Sprintf(" · %d more below", n), w)
	}
	c.put(0, p.body, hint)
	c.y++

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

	if (lay.strip == stripDigest || lay.strip == stripBlock) && f.Topology != nil {
		if fr := f.Topology(f, cursor); fr != nil {
			switch {
			case lay.strip == stripDigest:
				c.y++
				c.put(2, p.aside, clip(fr.Caption, w-3))
			case c.y+stripRows <= h:
				drawTopology(s, c.y+1, *fr, f.Glyphs, p, tick)
			}
		}
	}
	s.Show()
}

func (f *Form) drawMore(c *canvas, p palette, row, x, y int) {
	m := f.Rows[row].More
	if m == nil || len(m.Options) == 0 {
		return
	}
	inner := 0
	labels := make([]string, len(m.Options))
	for i, opt := range m.Options {
		labels[i] = clipRunes(opt, 32)
		if n := textWidth("[x] " + labels[i]); n > inner {
			inner = n
		}
	}
	_, h := c.s.Size()
	boxH := len(labels) + 2
	if y+boxH > h {
		y = y - 2 - boxH
	}
	if y < 0 {
		y = 0
	}
	bar := strings.Repeat("─", inner)
	c.y = y
	c.put(x, p.body, "┌"+bar+"┐")
	for i, label := range labels {
		c.y = y + 1 + i
		mark := "[ ] "
		st := p.idle
		if i < len(m.On) && m.On[i] {
			mark, st = "[x] ", p.brand
		}
		if m.offAt(i) {
			st = p.warn
		}
		if i == m.At {
			st = st.Underline(true)
		}
		cell := mark + label
		if pad := inner - textWidth(cell); pad > 0 {
			cell += strings.Repeat(" ", pad)
		}
		c.put(x, p.body, "│")
		c.put(x+1, st, cell)
		c.put(x+1+inner, p.body, "│")
	}
	c.y = y + 1 + len(labels)
	c.put(x, p.body, "└"+bar+"┘")
}

func (f *Form) drawBodyLine(c *canvas, p palette, ln bodyLine, cursor, limit int, named bool) {
	r := f.Rows[ln.row]
	switch ln.kind {
	case lineBlank:
		return
	case lineHeading:
		text := r.Label
		if r.Heading != "" {
			text = r.Heading
		}
		label := " " + text + " "
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
	if r.Divider && named && r.Heading == "" {
		rowLabel = "" // the heading above it is on screen and already says this
	}
	c.put(bodyIndent, labelStyle, marker+clipRunes(rowLabel, labelWidth-textWidth(marker)))
	x := optOrigin
	if r.Field {
		f.drawField(c, p, r, x, ln.row == cursor)
		return
	}
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
		at := x
		if j < len(r.Cols) {
			at = optOrigin + r.Cols[j]*colPitch
		}
		cell := r.columnText(opt)
		c.put(at, st, glyph+cell)
		if j >= len(r.Cols) {
			x += textWidth(glyph+cell) + 3
		}
	}
}

func headingsInWindow(lines []bodyLine, off, window int) map[int]bool {
	named := map[int]bool{}
	for i := off; i < off+window && i < len(lines); i++ {
		if lines[i].kind == lineHeading {
			named[lines[i].row] = true
		}
	}
	return named
}

func (f *Form) navigable(s tcell.Screen) bool {
	w, h := s.Size()
	return !f.layout(w, h).tooSmall
}

func (f *Form) minHeight(w int) int {
	need := 2 + minBodyLines + 2
	if help := f.maxHelpLines(w - 4); help > 0 {
		need += help + 1
	}
	return need
}

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
			col += textWidth(string(rs[i:j]))
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
			head := string(tok[:k])
			put(col, p.accent, head)
			put(col+textWidth(head), p.body, string(tok[k:]))
		default:
			put(col, p.body, word)
		}
		if aside && strings.HasSuffix(word, ")") {
			aside = false
		}
		afterOn = word == "on"
		col += textWidth(word)
		i = j
	}
}

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
	var names []string
	flush := func() {
		if len(names) == 0 {
			return
		}
		out = append(out, fmt.Sprintf("%-9s %s", "", strings.Join(names, "  ")))
		names = nil
	}
	for _, k := range sortedKeys(settings) {
		if settingValueHidden(k) {
			names = append(names, k)
			continue
		}
		flush()
		out = append(out, fmt.Sprintf("%-9s %s=%s", "", k, settings[k]))
	}
	flush()
	return out
}

func settingValueHidden(k string) bool {
	switch k {
	case "CODE_THEME", "DARK_MODE":
		return true
	}
	return false
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

const fieldWidth = 38

// drawField renders one field: its state glyph, then the box.
func (f *Form) drawField(c *canvas, p palette, r Row, x int, focused bool) {
	glyph, st := "  ", p.idle
	switch {
	case r.Warn:
		glyph, st = "! ", p.warn
	case r.Held:
		glyph, st = "\u2713 ", p.brand
	}
	c.put(x, st, glyph)
	x += 2

	body, bodyStyle := r.Placeholder, p.idle
	if r.Value != "" {
		body, bodyStyle = r.Value, p.body
		if r.Masked {
			body = strings.Repeat("\u2022", len([]rune(r.Value)))
		}
	}
	if w := len([]rune(body)); w > fieldWidth {
		body = string([]rune(body)[:fieldWidth])
	} else {
		body += strings.Repeat(" ", fieldWidth-w)
	}
	if focused {
		bodyStyle = bodyStyle.Underline(true)
	}
	c.put(x, p.idle, "[")
	c.put(x+1, bodyStyle, body)
	c.put(x+1+fieldWidth, p.idle, "]")
}

func preferUTF8Locale() {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		if v == "C" || v == "POSIX" {
			_ = os.Setenv("LC_ALL", "C.UTF-8")
		}
		return
	}
	_ = os.Setenv("LC_ALL", "C.UTF-8")
}

func registerGlyphFallbacks(s tcell.Screen) {
	ascii := glyphsFor(GlyphsASCII)
	var pairs [][2]string
	for _, set := range []glyphSet{glyphsFor(GlyphsNerd), glyphsFor(GlyphsSafe)} {
		pairs = append(pairs, [2]string{set.cloud, ascii.cloud}, [2]string{set.key, ascii.key},
			[2]string{set.quiet, ascii.quiet}, [2]string{set.speaking, ascii.speaking})
	}
	for _, p := range pairs {
		for _, r := range p[0] {
			s.RegisterRuneFallback(r, p[1])
		}
	}
}
