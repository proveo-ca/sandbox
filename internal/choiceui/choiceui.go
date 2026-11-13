// SPEC: _spec/internal/choiceui/choice-prompt-render.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/internal/choiceui/wireframe.puml
// Package choiceui renders the one-shot harness choice prompt.
package choiceui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	RadioSet []string // options drawn as one radio group inside a checkbox row
	Divider  bool

	// A field row takes typing instead of options.
	// SPEC: _spec/cmd/proveo/init-credential-wireframe.puml
	Field       bool
	Heading     string            // the divider's text, when the group is not named for this row
	Cols        []int             // 0-based column of each option; nil packs left to right
	Paint       map[string]string // option → cell text, when the cell is shorter than the option
	More        *More             // multi-select list opened from More.Cell
	Lists       map[int]*More
	CellRunes   int
	Value       string
	Placeholder string
	Masked      bool
	Held        bool
	Warn        bool
}

// More is the selectable or read-only list a row cell opens.
// SPEC: _spec/internal/devports/dev-ports.puml, _spec/cmd/proveo/github-access.puml
type More struct {
	Cell     int
	Options  []string
	On       []bool
	Off      []bool // greyed entries; Row.OffWhy names why
	At       int
	ReadOnly bool
}

func (m *More) visible(r Row) bool {
	return m != nil && r.cursorAt() == m.Cell && (m.ReadOnly || !r.offAt(m.Cell)) && len(m.Options) > 0
}

func (r *Row) activeMore() *More {
	if r.More.visible(*r) {
		return r.More
	}
	if m := r.Lists[r.cursorAt()]; m.visible(*r) {
		return m
	}
	return nil
}

func (r *Row) cellMore(i int) *More {
	if r.More != nil && r.More.Cell == i {
		return r.More
	}
	return r.Lists[i]
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

// columnText clips the option or its Paint label to the row's cell width.
func (r Row) columnText(opt string) string {
	limit := columnRunes
	if r.CellRunes > 0 {
		limit = r.CellRunes
	}
	if s, ok := r.Paint[opt]; ok && s != "" {
		return clipRunes(s, limit)
	}
	return clipRunes(opt, limit)
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

// The heads a gated answer's help line opens with.
const (
	offHead      = "off: "
	alwaysOnHead = "always on: "
)

func (r *Row) helpLines(width int) []helpLine {
	idx := r.cursorAt()
	if idx < 0 || idx >= len(r.Options) {
		return nil
	}
	if r.activeMore() != nil {
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
		label := offHead
		if r.onAt(idx) {
			label = alwaysOnHead
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
	m := r.activeMore()
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
		for _, l := range wrap(offHead+why, width, len(offHead)) {
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

func (r *Row) radioAt(i int) bool {
	return r.Radio || (i < len(r.Options) && slices.Contains(r.RadioSet, r.Options[i]))
}

type Form struct {
	Banner      []string
	Title       string
	Header      []string
	Rows        []Row
	OnChange    func(*Form)
	Glyphs      GlyphTier
	Topology    func(f *Form, cursor int) *Frame
	StartRow    string // row label the cursor opens on; empty means first selectable
	StartOption string // option the cursor opens on within StartRow
	Step        Step   // place in a sequence of screens; the zero value is a lone form
	Skip        string // label of a focusable button under the rows; Enter on it is Skipped
	// OnEnter names what accepting the form runs, in order, for the answers as they stand.
	// SPEC: _spec/internal/choiceui/wireframe.puml
	OnEnter func(*Form) []string
	scroll  int
	onSkip  bool // the cursor is on the Skip button
}

// Step is screen At of Of; Esc leaves a screen past the first as Back.
// SPEC: _spec/internal/choiceui/wizard-keys.puml
type Step struct {
	At, Of int
	Leave  Leave // what Ctrl+C asks on this page; the zero value asks the generic question
}

// Leave words the question Ctrl+C asks on a wizard's page: a title, and a body with one line per "\n".
type Leave struct{ Title, Body string }

func (s Step) backable() bool { return s.At > 1 }

// wizard reports whether the form is one page of several.
func (s Step) wizard() bool { return s.Of > 1 }

// Outcome is how the operator left a form.
type Outcome int

const (
	Cancelled Outcome = iota
	Accepted
	Back
	Skipped // the Skip button: this page changes nothing, the next one follows
	Quit    // ctrl+c, confirmed: the wizard ends here
)

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

// Adopt copies the answers of prev into every row of f that offers the same
// options; a locked row and an option f greys out keep f's own answer.
func (f *Form) Adopt(prev *Form) {
	if prev == nil {
		return
	}
	for i := range f.Rows {
		r := &f.Rows[i]
		at := slices.IndexFunc(prev.Rows, func(o Row) bool {
			return o.Label == r.Label && o.Field == r.Field && slices.Equal(o.Options, r.Options)
		})
		if at < 0 || r.Locked {
			continue
		}
		was := prev.Rows[at]
		if r.Field {
			r.Value = was.Value
			continue
		}
		if !r.offAt(was.Selected) {
			r.Selected = was.Selected
		}
		for j := range r.On {
			if j < len(was.On) && !r.offAt(j) {
				r.On[j] = was.On[j]
			}
		}
		r.More.adopt(was.More)
		for cell, m := range r.Lists {
			m.adopt(was.Lists[cell])
		}
	}
}

// adopt copies the ticks of prev when both lists offer the same entries.
func (m *More) adopt(prev *More) {
	if m == nil || prev == nil || m.ReadOnly || !slices.Equal(m.Options, prev.Options) {
		return
	}
	for i := range m.On {
		if i < len(prev.On) && !m.offAt(i) {
			m.On[i] = prev.On[i]
		}
	}
}

func (f *Form) Run() (confirmed bool, err error) {
	out, err := f.RunStep()
	return out == Accepted, err
}

// RunStep is Run that also reports a step back.
func (f *Form) RunStep() (Outcome, error) {
	in, out, done, err := openTerminal()
	if err != nil {
		return Cancelled, fmt.Errorf("choice prompt: %w", err)
	}
	defer done()
	final, err := tea.NewProgram(formModel{f: f, cursor: f.startCursor(), tick: 1},
		tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
	if err != nil {
		return Cancelled, fmt.Errorf("choice prompt: %w", err)
	}
	if m, ok := final.(formModel); ok {
		return m.out, nil
	}
	return Cancelled, nil
}

// press is one key as the form reads it: a named key, or typed text.
type press struct {
	key  string // "enter", "esc", "shift+tab", "up", …; empty for typed text
	text string
}

// formModel runs a Form as a Bubble Tea model.
type formModel struct {
	f      *Form
	cursor int
	tick   int
	w, h   int
	out    Outcome
	quit   quitGate
}

// formTick advances the figure's motion by one frame.
type formTick struct{}

func animTick() tea.Cmd {
	return tea.Tick(animFrame, func(time.Time) tea.Msg { return formTick{} })
}

func (m formModel) Init() tea.Cmd { return animTick() }

func (m formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.quit.up() {
			m.quit, _ = m.quit.Update(msg)
		}
	case formTick:
		m.tick++
		return m, animTick()
	case tea.KeyPressMsg:
		if m.quit.up() {
			var leave bool
			if m.quit, leave = m.quit.Update(msg); leave {
				m.out = Quit
				return m, tea.Quit
			}
			return m, nil
		}
		p := press{key: msg.String()}
		if msg.Text != "" {
			p = press{text: msg.Text}
		}
		out, done := m.f.key(&m.cursor, p, m.w, m.h)
		switch {
		case done && out == Quit && m.f.navigable(m.w, m.h):
			m.quit = m.quit.open(m.w, m.h, m.f.Step.Leave, m.f.Glyphs)
		case done:
			m.out = out
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m formModel) View() tea.View {
	if m.quit.up() {
		return m.quit.view()
	}
	v := tea.NewView(m.f.frame(m.w, m.h, m.cursor, m.tick))
	v.AltScreen = true
	return v
}

// frame is the form drawn at w by h, as the string a terminal shows.
func (f *Form) frame(w, h, cursor, tick int) string {
	s := newScreen(w, h)
	f.draw(s, cursor, tick)
	return s.view()
}

// key applies one key press: it moves the cursor, changes an answer, or ends the form.
func (f *Form) key(cursor *int, p press, w, h int) (Outcome, bool) {
	if out, done := f.leave(p); done {
		return out, true
	}
	if !f.navigable(w, h) {
		return f.quitKey(), p.text == "q"
	}
	if f.onSkip {
		if p.key == "up" || p.key == "home" || p.key == "pgup" {
			f.onSkip = false
		}
		return Cancelled, false
	}
	if f.typing(*cursor, p) || f.moreKey(*cursor, p) {
		return Cancelled, false
	}
	switch p.key {
	case "tab":
		f.toggle(*cursor)
	case "up":
		*cursor = f.move(*cursor, -1)
	case "down":
		next := f.move(*cursor, +1)
		f.onSkip = f.Skip != "" && next == *cursor
		*cursor = next
	case "pgup":
		*cursor = f.page(*cursor, -1)
	case "pgdown":
		*cursor = f.page(*cursor, +1)
	case "home":
		*cursor = f.firstSelectable()
	case "end":
		*cursor = f.lastSelectable()
	case "left":
		f.cycle(*cursor, -1)
	case "right":
		f.cycle(*cursor, +1)
	}
	switch p.text {
	case "k":
		*cursor = f.move(*cursor, -1)
	case "j":
		*cursor = f.move(*cursor, +1)
	case "h":
		f.cycle(*cursor, -1)
	case "l":
		f.cycle(*cursor, +1)
	case " ":
		if *cursor < len(f.Rows) && f.Rows[*cursor].Multi {
			f.toggle(*cursor)
		} else {
			f.cycle(*cursor, +1)
		}
	case "q":
		return f.quitKey(), true
	}
	return Cancelled, false
}

// quitKey is what ctrl+c and q give: Quit on a wizard's page, which the model
// confirms first, and Cancelled on a lone form.
func (f *Form) quitKey() Outcome {
	if f.Step.wizard() {
		return Quit
	}
	return Cancelled
}

// leave answers the keys that end a form. Esc steps back past the first page and
// cancels on it; shift+tab still steps back, unadvertised.
// SPEC: _spec/internal/choiceui/wizard-keys.puml
func (f *Form) leave(p press) (Outcome, bool) {
	switch p.key {
	case "ctrl+c":
		return f.quitKey(), true
	case "esc":
		if f.Step.backable() {
			return Back, true
		}
		return Cancelled, true
	case "enter":
		if f.onSkip {
			return Skipped, true
		}
		return Accepted, true
	case "shift+tab":
		if f.Step.backable() {
			return Back, true
		}
	}
	return Cancelled, false
}

// typing routes a keystroke into a field row, and answers true when the key
// belonged to it.
func (f *Form) typing(cursor int, p press) bool {
	if cursor < 0 || cursor >= len(f.Rows) || !f.Rows[cursor].Field {
		return false
	}
	r := &f.Rows[cursor]
	switch {
	case p.text != "":
		r.Value += p.text
		f.changed()
		return true
	case p.key == "backspace":
		if runes := []rune(r.Value); len(runes) > 0 {
			r.Value = string(runes[:len(runes)-1])
			f.changed()
		}
		return true
	case p.key == "ctrl+u":
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
	if r.activeMore() != nil {
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
func (f *Form) moreKey(cursor int, p press) bool {
	r, ok := f.moreRow(cursor)
	if !ok {
		return false
	}
	m := r.activeMore()
	n := len(m.Options)
	switch {
	case p.key == "down" || p.text == "j":
		if m.At+1 < n {
			m.At++
			return true
		}
	case p.key == "up" || p.text == "k":
		if m.At > 0 {
			m.At--
			return true
		}
	case p.text == " ":
		f.toggleMore(r)
		return true
	}
	return false
}

func (f *Form) moreRow(cursor int) (*Row, bool) {
	if cursor < 0 || cursor >= len(f.Rows) {
		return nil, false
	}
	r := &f.Rows[cursor]
	if r.activeMore() == nil {
		return nil, false
	}
	return r, true
}

func (f *Form) toggleMore(r *Row) {
	m := r.activeMore()
	if m == nil || m.ReadOnly || r.Locked || m.offAt(m.At) {
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

// palette is the prompt's styles: one per role a piece of text can play.
type palette struct {
	brand  lipgloss.Style // the cursor's row
	accent lipgloss.Style // first-party emphasis: row labels, header keys
	warn   lipgloss.Style // a consequence
	body   lipgloss.Style // the terminal's own ink
	title  lipgloss.Style
	idle   lipgloss.Style
	aside  lipgloss.Style // italic — parenthetical descriptions
	key    lipgloss.Style // a key on the key line: bold
	word   lipgloss.Style // the word beside it: grey
	rule   lipgloss.Style // a tree guide outside the cursor's folder

	focus       lipgloss.Style // the cursor's answer: black on brand
	focusOff    lipgloss.Style // the cursor on a fixed or unavailable answer: black on grey
	chosen      lipgloss.Style // an answer in force the operator chose: bold
	offered     lipgloss.Style // an answer not chosen: grey
	fixed       lipgloss.Style // an answer in force the operator cannot change: plain
	gated       lipgloss.Style // an answer that is unavailable: faint, struck through
	lockedLabel lipgloss.Style // a locked row's label: faint
	frame       lipgloss.Style // card walls and rules: grey
	rail        lipgloss.Style // the walls beside the cursor's section: brand
	chip        lipgloss.Style // the step chip: brand on the surface
	ok          lipgloss.Style // a passing check's mark: green
	fail        lipgloss.Style // a failing check's mark: bold red
	alert       lipgloss.Style // the mark beside a line that states a loss: black on lime
}

const black = 0x000000

func styles() palette {
	fg := func(c int) lipgloss.Style { return lipgloss.NewStyle().Foreground(hexOf(c)) }
	return palette{
		brand:  fg(ui.ColorBrand).Bold(true),
		accent: fg(ui.ColorAccentBright).Bold(true),
		warn:   fg(ui.ColorWarn),
		body:   lipgloss.NewStyle(),
		title:  lipgloss.NewStyle().Bold(true),
		idle:   fg(ui.ColorMuted),
		aside:  lipgloss.NewStyle().Italic(true),
		key:    lipgloss.NewStyle().Bold(true),
		word:   fg(ui.ColorMuted),
		rule:   fg(ui.ColorRule),

		focus:       fill(black, ui.ColorBrand),
		focusOff:    fill(black, ui.ColorMuted),
		chosen:      lipgloss.NewStyle().Bold(true),
		offered:     fg(ui.ColorMuted),
		fixed:       lipgloss.NewStyle(),
		gated:       lipgloss.NewStyle().Faint(true).Strikethrough(true),
		lockedLabel: lipgloss.NewStyle().Faint(true),
		frame:       fg(ui.ColorMuted),
		rail:        fg(ui.ColorBrand),
		chip:        fill(ui.ColorBrand, ui.ColorSurface),
		ok:          fg(ui.ColorSuccess),
		fail:        fg(ui.ColorFailure).Bold(true),
		alert:       fill(black, ui.ColorWarn),
	}
}

// fill is ink on a face, drawn reversed.
func fill(ink, face int) lipgloss.Style {
	return lipgloss.NewStyle().Reverse(true).Foreground(hexOf(face)).Background(hexOf(ink))
}

// answerStyle is an answer's state style; the cursor's answer is filled.
func answerStyle(p palette, on, fixed, gated, focused bool) lipgloss.Style {
	switch {
	case focused && (fixed || gated):
		return p.focusOff.Strikethrough(gated)
	case focused:
		return p.focus.Bold(on)
	case gated:
		return p.gated
	case fixed:
		return p.fixed
	case on:
		return p.chosen
	}
	return p.offered
}

func (f *Form) draw(s *screen, cursor, tick int) {
	p := styles()
	w, h := s.Size()
	lay := f.layout(w, h)
	if lay.tooSmall {
		drawTooSmall(s, w, h, f.minHeight(w))
		return
	}

	k := newCard(w, f.Glyphs, p)
	c := newCanvas(s)

	if lay.banner {
		drawBanner(c, p, f.Banner)
	}
	k.head(c, f.Title, f.headChip(w))
	if lay.header {
		for _, row := range f.facts(w) {
			x := factCol(w)
			for _, fact := range row {
				putHeader(c.put, p, x, fact)
				x += textWidth(fact) + len(factJoin)
			}
			k.walls(c, false)
			c.y++
		}
	}

	lines := f.bodyLines()
	f.scroll = scrollTo(f.scroll, lay.body, lines, cursor)
	g := newGutter(f.scroll, lay.body, len(lines))
	named := headingsInWindow(lines, f.scroll, lay.body)
	groups := f.groups()
	mark := cursor
	if f.onSkip {
		mark = -1
	}
	lit := func(row int) bool { return mark >= 0 && mark < len(groups) && groups[row] == groups[mark] }
	pt := pitch(k.w, f.lastCol())
	bodyTop := c.y
	window := min(lay.body, len(lines)-f.scroll)
	c.clipTo(bodyTop, bodyTop+lay.body)
	c.right = k.edge
	dropY := -1
	for i := range window {
		c.y = bodyTop + i
		ln := lines[f.scroll+i]
		if ln.kind == lineRow && ln.row == cursor && f.Rows[ln.row].activeMore() != nil {
			dropY = c.y
		}
		f.drawBodyLine(c, p, k, ln, mark, pt, lit(ln.row), named[ln.row])
	}
	c.unclip()
	c.clipTo(bodyTop, bodyTop+lay.body)
	for i := range window {
		c.y = bodyTop + i
		if lines[f.scroll+i].kind == lineRow {
			k.wallL(c, lit(lines[f.scroll+i].row))
		}
		k.wallR(c)
		if glyph, thumb := g.glyph(i, plainGlyphs(f.Glyphs)); glyph != "" {
			st := p.frame
			if thumb {
				st = p.rail
			}
			c.put(k.gutterX(), st, glyph)
		}
	}
	c.unclip()

	if lay.strip == stripPane && f.Topology != nil && lay.body >= paneRows &&
		bodyTop+paneRows <= h && lay.pane+paneCols.width <= w {
		if fr := f.Topology(f, cursor); fr != nil {
			drawFigure(s, lay.pane, bodyTop, paneCols, *fr, f.Glyphs, p, tick)
		}
	}

	c.y = bodyTop + lay.body
	if f.Skip != "" {
		k.walls(c, false)
		c.y++
		f.drawSkip(c, p)
		k.walls(c, false)
		c.y++
	}
	k.foot(c)
	c.y++
	f.drawHint(c, p, w, g.hiddenRows(lines))
	k.walls(c, false)
	c.y++
	k.bottom(c)
	c.y++

	if lay.helpSlot > 0 {
		helpTop := c.y
		if cursor >= 0 && cursor < len(f.Rows) && !f.onSkip {
			for i, line := range f.Rows[cursor].helpLines(helpWidth(w)) {
				c.y = helpTop + i
				putHelp(c, p, line)
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
	switch lay.strip {
	case stripDigest:
		c.y++
	case stripBlock:
		c.y += stripRows
	}
	f.drawOnEnter(c, p, w, h)

	if dropY >= 0 {
		r := &f.Rows[cursor]
		cell := r.activeMore().Cell
		col := cell
		if cell < len(r.Cols) {
			col = r.Cols[cell]
		}
		f.drawMore(c, p, cursor, optOrigin+col*pt, dropY+1, k.edge)
	}
}

// putHelp paints one help line at column 2; a warning's head is bold.
func putHelp(c *canvas, p palette, line helpLine) {
	if line.warn {
		for _, head := range []string{offHead, alwaysOnHead} {
			if rest, ok := strings.CutPrefix(line.text, head); ok {
				c.put(2, p.title, head)
				c.put(2+textWidth(head), p.body, rest)
				return
			}
		}
	}
	c.put(2, p.body, line.text)
}

// onEnter is what accepting the form runs; nothing while the Skip button is focused.
func (f *Form) onEnter() []string {
	if f.OnEnter == nil || f.onSkip {
		return nil
	}
	return f.OnEnter(f)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// drawOnEnter lists what Enter runs in the rows left under every other region: the heading at the
// title's column, each number two columns in, its text five. With too few rows the list ends on a
// count of what it left out.
func (f *Form) drawOnEnter(c *canvas, p palette, w, h int) {
	steps := f.onEnter()
	room := h - c.y - 2 // a blank, then the heading
	if len(steps) == 0 || room < 1 {
		return
	}
	x := inset(w)
	c.y++
	c.put(x, p.accent, clip("on enter", w-x))
	for i, text := range steps {
		c.y++
		if i == room-1 && len(steps) > room {
			c.put(x+2, p.idle, clip(fmt.Sprintf("… and %d more", len(steps)-i), w-x-2))
			return
		}
		c.put(x+2, p.idle, fmt.Sprintf("%d", i+1))
		c.put(x+5, p.body, clip(text, w-x-5))
	}
}

const hintScroll = "pgup/pgdn scroll"

// skipRows is what the Skip button costs inside the walls under the rows: a blank and the button.
const skipRows = 2

// drawSkip paints the Skip button in the label column; focused, it is filled and carries the enter mark.
func (f *Form) drawSkip(c *canvas, p palette) {
	if !f.onSkip {
		c.put(bodyIndent+2, p.idle, "[ "+f.Skip+" ]")
		return
	}
	c.put(bodyIndent, p.brand, "› ")
	c.put(bodyIndent+2, p.focus.Bold(true), " "+f.Skip+" "+enterMark(f.Glyphs)+" ")
}

// hintPart is one key-line segment: a key and the word for what it does. A
// narrow screen drops the lowest keep first.
type hintPart struct {
	key, word string
	keep      int
}

// text is the segment as the plain key line spells it.
func (p hintPart) text() string {
	if p.key == "" || p.word == "" {
		return p.key + p.word
	}
	return p.key + " " + p.word
}

const hintJoin = " · "

// hintParts is the key line's segments, narrowed one by one so "enter accept" goes last.
// SPEC: _spec/internal/choiceui/wireframe.puml
func (f *Form) hintParts(w, hidden int) []hintPart {
	parts := append(pageHint(f.Step, f.headChip(w)), hintPart{"↑↓", "row", 3})
	if slices.ContainsFunc(f.Rows, func(r Row) bool { return r.Multi }) {
		parts = append(parts, hintPart{"←→", "move", 3}, hintPart{"space", "toggle", 2})
	} else {
		parts = append(parts, hintPart{"←→", "choose", 3})
	}
	parts = append(parts, hintPart{"pgup/pgdn", "scroll", 1})
	enter, esc := hintPart{"enter", "accept", 9}, hintPart{"esc", "cancel", 5}
	if f.onSkip {
		enter.word = "skip"
	}
	if f.Step.backable() {
		esc = hintPart{"esc", "back", 6}
	}
	parts = append(parts, enter)
	if n := len(f.onEnter()); n > 0 {
		parts = append(parts, hintPart{"", plural(n, "step"), 3})
	}
	parts = append(parts, esc)
	if f.Step.wizard() {
		parts = append(parts, hintPart{"ctrl+c", "quit", 2})
	}
	if hidden > 0 {
		parts = append(parts, hintPart{"", fmt.Sprintf("%d more below", hidden), 4})
	}
	return narrowed(parts, keyRoom(w))
}

// headChip is the step chip the head draws on a w-column terminal, or "" when it draws none.
// SPEC: _spec/internal/choiceui/wireframe.puml
func (f *Form) headChip(w int) string { return stepChip(f.Step, "page", f.Title, w) }

// narrowed drops the lowest-kept segment, again and again, until the line fits w or one is left.
func narrowed(parts []hintPart, w int) []hintPart {
	for len([]rune(joinHint(parts))) > w && len(parts) > 1 {
		low := 0
		for i, p := range parts {
			if p.keep < parts[low].keep {
				low = i
			}
		}
		parts = slices.Delete(parts, low, low+1)
	}
	return parts
}

func joinHint(parts []hintPart) string {
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.text()
	}
	return strings.Join(texts, hintJoin)
}

// hint is the key line a w-column terminal shows, as plain text; tests and e2e read this projection.
func (f *Form) hint(w, hidden int) string {
	return clip(joinHint(f.hintParts(w, hidden)), keyRoom(w))
}

// drawHint paints the form's key line inside the card.
func (f *Form) drawHint(c *canvas, p palette, w, hidden int) {
	paintHint(c, p, f.hintParts(w, hidden), inset(w), keyRoom(w))
}

// paintHint paints a key line from column x0 in two tones, cut to w: each key bold, its word grey.
func paintHint(c *canvas, p palette, parts []hintPart, x0, w int) {
	var pieces []piece
	for i, part := range parts {
		if i > 0 {
			pieces = append(pieces, piece{p.word, hintJoin})
		}
		if part.key != "" {
			pieces = append(pieces, piece{p.key, part.key})
		}
		if part.word != "" {
			word := part.word
			if part.key != "" {
				word = " " + word
			}
			pieces = append(pieces, piece{p.word, word})
		}
	}
	left := []rune(clip(joinHint(parts), w))
	x := x0
	for _, pc := range pieces {
		n := min(len([]rune(pc.text)), len(left))
		if n == 0 {
			break
		}
		shown := string(left[:n])
		c.put(x, pc.st, shown)
		x += textWidth(shown)
		left = left[n:]
	}
}

// piece is one styled run of a line.
type piece struct {
	st   lipgloss.Style
	text string
}

// drawMore paints the open list under its cell, ending before column right.
func (f *Form) drawMore(c *canvas, p palette, row, x, y, right int) {
	m := f.Rows[row].activeMore()
	if m == nil || len(m.Options) == 0 {
		return
	}
	_, h := c.s.Size()
	x = min(x, max(0, right-8))
	inner := 0
	labels := make([]string, len(m.Options))
	for i, opt := range m.Options {
		labels[i] = clipRunes(opt, max(1, right-x-6))
		if n := textWidth("[x] " + labels[i]); n > inner {
			inner = n
		}
	}
	visible := min(len(labels), max(1, h-4))
	first := max(0, min(m.At-visible+1, len(labels)-visible))
	boxH := visible + 2
	if y+boxH > h {
		y = y - 2 - boxH
	}
	if y < 0 {
		y = 0
	}
	bar := strings.Repeat("─", inner)
	c.y = y
	c.put(x, p.body, "┌"+bar+"┐")
	for i := first; i < first+visible; i++ {
		label := labels[i]
		c.y = y + 1 + i - first
		on := i < len(m.On) && m.On[i]
		mark := "[ ] "
		if on {
			mark = "[x] "
		}
		gated := m.offAt(i) && !on
		st := answerStyle(p, on, !gated && (m.ReadOnly || m.offAt(i)), gated, i == m.At)
		cell := mark + label
		if pad := inner - textWidth(cell); pad > 0 {
			cell += strings.Repeat(" ", pad)
		}
		c.put(x, p.body, "│")
		c.put(x+1, st, cell)
		c.put(x+1+inner, p.body, "│")
	}
	c.y = y + 1 + visible
	c.put(x, p.body, "└"+bar+"┘")
}

// drawBodyLine paints one body line: a section's stub, or a row's marker, label and cells on a grid of pitch pt.
func (f *Form) drawBodyLine(c *canvas, p palette, k card, ln bodyLine, cursor, pt int, lit, named bool) {
	r := f.Rows[ln.row]
	if ln.kind == lineHeading {
		text := r.Label
		if r.Heading != "" {
			text = r.Heading
		}
		k.stub(c, text, lit)
		return
	}

	marker := "  "
	labelStyle := p.accent
	if r.Locked {
		labelStyle = p.lockedLabel
	}
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
		on := false
		switch {
		case r.Multi && r.radioAt(j) && r.onAt(j):
			glyph, on = "(•) ", true
		case r.Multi && r.radioAt(j):
			glyph = "( ) "
		case r.Multi && r.onAt(j):
			glyph, on = "[x] ", true
		case r.Multi:
			glyph = "[ ] "
		case j == r.Selected:
			glyph, on = "(•) ", true
		default:
			glyph = "( ) "
		}
		held := r.Locked || r.offAt(j)
		st := answerStyle(p, on, held && on, held && !on, ln.row == cursor && j == r.cursorAt())
		at, room := x, k.edge-x
		if j < len(r.Cols) {
			at = optOrigin + r.Cols[j]*pt
			room = min(pt-2, k.edge-at)
		}
		cell, arrow := r.columnText(opt), ""
		if m := r.cellMore(j); m != nil && len(m.Options) > 0 {
			arrow = " ▾"
			if plainGlyphs(f.Glyphs) {
				arrow = " v"
			}
		}
		c.put(at, st, fitCell(glyph, cell, arrow, room))
		if j >= len(r.Cols) {
			x += textWidth(glyph+cell+arrow) + 3
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

func (f *Form) navigable(w, h int) bool {
	return !f.layout(w, h).tooSmall
}

func (f *Form) minHeight(w int) int {
	need := cardRows + minBodyLines + f.maxHelpLines(helpWidth(w))
	if f.Skip != "" {
		need += skipRows
	}
	return need
}

func drawTooSmall(s *screen, w, h, needH int) {
	p := styles()
	msg := fmt.Sprintf("terminal too small: %dx%d, need %d rows", w, h, needH)
	c := newCanvas(s)
	c.put(0, p.warn, clip(msg, w))
	if h > 1 {
		c.y = 1
		c.put(0, p.body, clip("enter accepts the resolved values · esc cancels", w))
	}
}

const factJoin = "    "

// facts is the header as card rows on a w-column terminal.
func (f *Form) facts(w int) [][]string { return factRows(f.Header, edge(w)-factCol(w)) }

// factRows lays the header out as card rows of width columns: unindented `key: value` facts flow
// joined by four spaces and wrap at a fact boundary; any other line keeps a row of its own. An
// indented line starts at the facts' column, two columns further in when it continues a fact.
// SPEC: _spec/internal/choiceui/wireframe.puml
func factRows(header []string, width int) [][]string {
	var rows [][]string
	var run []string
	used, underFact := 0, false
	flush := func() {
		if len(run) > 0 {
			rows, run, used = append(rows, run), nil, 0
		}
	}
	for _, line := range header {
		if text := strings.TrimLeftFunc(line, unicode.IsSpace); text != line && text != "" {
			flush()
			if underFact {
				text = "  " + text
			}
			rows = append(rows, []string{clip(text, width)})
			continue
		}
		fact, ok := asFact(line)
		underFact = ok
		if !ok {
			flush()
			rows = append(rows, []string{clip(line, width)})
			continue
		}
		fact = clip(fact, width)
		if len(run) > 0 && used+len(factJoin)+textWidth(fact) > width {
			flush()
		}
		if len(run) > 0 {
			used += len(factJoin)
		}
		run, used = append(run, fact), used+textWidth(fact)
	}
	flush()
	return rows
}

// asFact is an unindented `key:   value` header line as `key: value`.
func asFact(line string) (string, bool) {
	if line == "" || unicode.IsSpace([]rune(line)[0]) {
		return "", false
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 || strings.ContainsRune(line[:colon], ' ') {
		return "", false
	}
	if value := strings.TrimSpace(line[colon+1:]); value != "" {
		return line[:colon+1] + " " + value, true
	}
	return line[:colon+1], true
}

func putHeader(put func(int, lipgloss.Style, string), p palette, x0 int, line string) {
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

func putPairs(put func(int, lipgloss.Style, string), p palette, col int, s string) {
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
	for _, k := range sortedKeys(settings) {
		if settingValueHidden(k) {
			continue
		}
		out = append(out, fmt.Sprintf("%-9s %s=%s", "", k, settings[k]))
	}
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
