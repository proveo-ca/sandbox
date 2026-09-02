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
	Reason   string
	Multi    bool
	On       []bool
	Off      []bool
	// Help is what an option DOES, keyed by the option itself. Keyed rather than
	// indexed because callers assemble Options conditionally — a harness without a
	// -browser variant is never offered one — and a parallel slice would then
	// describe the wrong box.
	//
	// It exists because the picker could only ever explain what it had taken AWAY:
	// Reason is written when something is gated off, so the two add-ons that were
	// available and ticked carried no text at all, and the operator was left to
	// infer "browser" and "docker (sandbox)" from their names.
	Help map[string]string
	// OffWhy is why one option is greyed, keyed the same way. Reason states the
	// row's constraints at a glance; this states one option's, in full, next to
	// its description.
	OffWhy map[string]string
	// Divider draws the row's name as a centered heading instead of a label in
	// the left column, and is how a group of checkboxes announces itself.
	// Declared rather than inferred: it used to be "the first multi-select row",
	// which silently meant only one group could ever be named.
	Divider bool
}

// helpLine is one line under the cursor's row: what the option the cursor is on
// means, and why it cannot be picked.
type helpLine struct {
	text string
	warn bool
}

// helpLines describes the option the cursor is on, wrapped to width. Drawn BELOW
// the row, never appended to it: the row is already ~70 columns of checkboxes, so
// text placed after them runs off the terminal — which is how the one description
// the picker did have, a gated option's reason, came to end mid-sentence.
//
// Wrapped rather than clipped, because these are the sentences the operator is
// meant to READ. The row's own Reason is still clipped: it is a glance signal for
// a row the cursor is not on, and the same words are here in full.
func (r *Row) helpLines(width int) []helpLine {
	if r.Selected < 0 || r.Selected >= len(r.Options) {
		return nil
	}
	opt := r.Options[r.Selected]
	var out []helpLine
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
		if r.onAt(r.Selected) {
			label = "always on: "
		}
		for _, l := range wrap(label+why, width, len([]rune(label))) {
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

func (r *Row) anyOff() bool {
	for _, off := range r.Off {
		if off {
			return true
		}
	}
	return false
}

type Form struct {
	Banner   []string
	Title    string
	Header   []string
	Rows     []Row
	OnChange func(*Form)
	// Glyphs is how much decoration the terminal is trusted with — a capability
	// of the session, so it is constant for the run.
	Glyphs GlyphTier
	// Topology projects the form onto the picture drawn under the help block.
	// It is called at PAINT time, on every frame, so it tracks the cursor —
	// which `move` deliberately does not report through OnChange. It must be
	// pure: it reads rows and never mutates the form it is handed. A nil
	// Topology means no strip, which is what every caller that does not want
	// one gets for free.
	Topology func(f *Form, cursor int) *Frame
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
		if !f.Rows[i].Locked {
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
	if r.Locked || len(r.Options) == 0 {
		return
	}
	n := len(r.Options)
	for step := 1; step <= n; step++ {
		next := ((r.Selected+delta*step)%n + n) % n
		// A single-select row's Selected IS the choice, so it may never rest on a
		// gated option. On a MULTI row it is only the cursor — the checkbox is the
		// choice, and toggle refuses a gated one on its own — so the cursor is
		// allowed to stop there. That is what makes a greyed box's explanation
		// readable: it is the one whose reason the operator most needs, and it was
		// the one option the cursor could never reach.
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

	y := 0
	put := func(x int, style tcell.Style, text string) {
		for i, r := range []rune(text) {
			s.SetContent(x+i, y, r, nil, style)
		}
	}

	if lay.banner {
		for _, b := range f.Banner {
			put(0, p.brand, b)
			y++
		}
		y++
	}
	put(0, p.title, f.Title)
	y += 2
	if lay.header {
		for _, hl := range f.Header {
			putHeader(put, p, hl)
			y++
		}
		y++
	}

	if lay.axis {
		put(22, p.body, "◀ riskier")
		put(60, p.body, "safer ▶")
		y += 2
	}

	for i, r := range f.Rows {
		namedByDivider := false
		if r.Divider {
			namedByDivider = true
			label := " " + r.Label + " "
			pad := (72 - len([]rune(label))) / 2
			if pad < 0 {
				pad = 0
			}
			y++
			put(pad, p.body, strings.Repeat("─", 6)+label+strings.Repeat("─", 6))
			y += 2
		}
		marker := "  "
		labelStyle := p.accent
		if i == cursor {
			marker = "› "
			labelStyle = p.brand
		}
		rowLabel := r.Label
		if namedByDivider {
			rowLabel = ""
		}
		put(0, labelStyle, marker+rowLabel)
		x := 22
		for j, opt := range r.Options {
			var glyph string
			st := p.idle
			switch {
			case r.Multi && r.onAt(j):
				glyph, st = "[x] ", p.brand
			case r.Multi:
				glyph = "[ ] "
			case j == r.Selected:
				glyph, st = "(•) ", p.brand
			default:
				glyph = "( ) "
			}
			if i == cursor && j == r.Selected {
				st = st.Underline(true)
			}
			if r.Locked || r.offAt(j) {
				st = p.warn
			}
			put(x, st, glyph+opt)
			x += len(glyph) + len(opt) + 3
		}
		if r.Reason != "" && (r.Locked || r.anyOff()) {
			put(x, p.warn, clip("— "+r.Reason, w-x))
		}
		y++
	}

	y++
	hint := "↑↓ row · ←→ choose · enter accept · esc cancel"
	for _, r := range f.Rows {
		if r.Multi {
			hint = "↑↓ row · ←→ move · space toggle · enter accept · esc cancel"
			break
		}
	}
	put(0, p.body, hint)

	// The cursor's option explains itself HERE, below the hint, and nothing is
	// drawn after it. Under the row it belonged to, the block's height changed
	// with the cursor and shoved every row beneath it up and down — the form
	// moved while the operator was reading it. Pinned last, it can grow freely
	// and the rows above never shift.
	// The help block is drawn into a slot reserved at its MAXIMUM height, so the
	// figure below it does not jump a row every time the cursor reaches a wordier
	// option. Without the reservation the strip moved under the operator while
	// they were reading it — the same failure that pushed help to the bottom in
	// the first place, one level down.
	// y+2, not y+1: the hint sits at y and the block has always opened with a
	// blank line under it. Reserving from y+1 silently deleted that blank and
	// pushed the help up against the hint.
	helpTop := y + 2
	if cursor >= 0 && cursor < len(f.Rows) {
		if lines := f.Rows[cursor].helpLines(w - 4); len(lines) > 0 {
			for i, hl := range lines {
				st := p.body
				if hl.warn {
					st = p.warn
				}
				y = helpTop + i
				put(2, st, hl.text)
			}
		}
	}
	y = helpTop + lay.helpSlot

	if lay.strip != stripNone && f.Topology != nil {
		if fr := f.Topology(f, cursor); fr != nil {
			if lay.strip == stripDigest {
				y++
				put(2, p.aside, clip(fr.Caption, w-3))
			} else {
				drawTopology(s, y, *fr, f.Glyphs, p, tick)
			}
		}
	}
	s.Show()
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
