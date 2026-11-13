// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// ScopePage asks where in a repository the agent works: a folder tree, the root first.
type ScopePage struct {
	Banner []string
	Title  string
	Root   string // the repository's name, drawn on the root row
	Tree   *Tree
	Step   Step
	Glyphs GlyphTier

	filtering bool
	top       int // the first tree row in the window
}

// Run draws the page on the terminal and returns the folder chosen; "" is the root.
func (sp *ScopePage) Run() (scope string, out Outcome, err error) {
	in, term, done, err := openTerminal()
	if err != nil {
		return "", Cancelled, fmt.Errorf("scope page: %w", err)
	}
	defer done()
	final, err := tea.NewProgram(scopeModel{page: sp},
		tea.WithInput(in), tea.WithOutput(term), tea.WithoutSignalHandler()).Run()
	if err != nil {
		return "", Cancelled, fmt.Errorf("scope page: %w", err)
	}
	if m, ok := final.(scopeModel); ok && m.out == Accepted {
		return sp.Tree.At(), Accepted, nil
	}
	return "", Cancelled, nil
}

// scopeModel runs a ScopePage as a Bubble Tea model.
type scopeModel struct {
	page *ScopePage
	w, h int
	out  Outcome
	quit quitGate
}

func (m scopeModel) Init() tea.Cmd { return nil }

func (m scopeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.quit.up() {
			m.quit, _ = m.quit.Update(msg)
		}
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
		out, done := m.page.key(p, m.h)
		switch {
		case done && out == Quit && !m.page.layout(m.h).tooSmall:
			m.quit = m.quit.open(m.w, m.h, m.page.Step.Leave)
		case done:
			m.out = out
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m scopeModel) View() tea.View {
	if m.quit.up() {
		return m.quit.view()
	}
	v := tea.NewView(m.page.frame(m.w, m.h))
	v.AltScreen = true
	return v
}

// frame is the page drawn at w by h, as the string a terminal shows.
func (sp *ScopePage) frame(w, h int) string {
	s := newScreen(w, h)
	sp.draw(s)
	return s.view()
}

// key applies one key press: it moves the cursor, folds, narrows, or ends the page.
func (sp *ScopePage) key(p press, h int) (Outcome, bool) {
	t := sp.Tree
	switch p.key {
	case "ctrl+c":
		if sp.Step.wizard() {
			return Quit, true
		}
		return Cancelled, true
	case "enter":
		return Accepted, true
	case "esc":
		if !sp.filtering && t.Filter() == "" {
			return Cancelled, true
		}
		sp.filtering = false
		t.SetFilter("")
		return Cancelled, false
	}
	lay := sp.layout(h)
	if lay.tooSmall {
		return Cancelled, false
	}
	switch p.key {
	case "up":
		t.Move(-1)
	case "down":
		t.Move(+1)
	case "pgup":
		t.Move(-lay.window)
	case "pgdown":
		t.Move(+lay.window)
	case "home":
		t.Move(-len(t.Rows()))
	case "end":
		t.Move(+len(t.Rows()))
	}
	if sp.filtering {
		sp.typing(p)
		return Cancelled, false
	}
	switch {
	case p.key == "left":
		t.Fold()
	case p.key == "right":
		t.Unfold()
	case p.text == "/":
		sp.filtering = true
	}
	return Cancelled, false
}

// typing edits the filter; backspace on an empty filter closes it.
func (sp *ScopePage) typing(p press) {
	t := sp.Tree
	switch {
	case p.text != "":
		t.SetFilter(t.Filter() + p.text)
	case p.key == "backspace":
		runes := []rune(t.Filter())
		if len(runes) == 0 {
			sp.filtering = false
			return
		}
		t.SetFilter(string(runes[:len(runes)-1]))
	case p.key == "ctrl+u":
		t.SetFilter("")
	}
}

// scopeLayout is which of the page's regions a terminal of this height holds.
type scopeLayout struct {
	banner   bool
	filter   bool
	window   int // tree rows on screen
	tooSmall bool
}

// scopeFoot is the rows under the tree: a blank, where Enter starts, a blank, the key line.
const scopeFoot = 4

func (sp *ScopePage) layout(h int) scopeLayout {
	lay := scopeLayout{filter: sp.filtering || sp.Tree.Filter() != ""}
	fixed := 2 + scopeFoot // the title and its blank
	if lay.filter {
		fixed++
	}
	rows := len(sp.Tree.Rows())
	if banner := len(sp.Banner) + 2; len(sp.Banner) > 0 && h >= fixed+banner+rows {
		lay.banner = true
		fixed += banner
	}
	lay.window = min(rows, h-fixed)
	lay.tooSmall = lay.window < 1
	return lay
}

// where is the sentence under the tree: the folder Enter starts the agent in.
func (sp *ScopePage) where() string {
	at := sp.Tree.At()
	if at == "" {
		return "enter starts the agent at the repo root, with every folder below it"
	}
	return "enter starts the agent in " + at
}

// whereShort is where() for a terminal too small to draw the tree.
func (sp *ScopePage) whereShort() string {
	if at := sp.Tree.At(); at != "" {
		return "enter starts in " + at
	}
	return "enter keeps the repo root"
}

func (sp *ScopePage) hintParts(w, hidden int) []hintPart {
	var parts []hintPart
	if sp.Step.Of > 1 {
		parts = append(parts, hintPart{"", fmt.Sprintf("page %d/%d", sp.Step.At, sp.Step.Of), 4})
	}
	parts = append(parts, hintPart{"↑↓", "row", 3})
	if sp.filtering {
		parts = append(parts, hintPart{"type", "to narrow", 2}, hintPart{"enter", "choose", 9}, hintPart{"esc", "clear", 5})
	} else {
		parts = append(parts, hintPart{"←→", "fold", 2}, hintPart{"/", "filter", 1},
			hintPart{"enter", "choose", 9}, hintPart{"esc", "cancel", 5})
	}
	if sp.Step.wizard() {
		parts = append(parts, hintPart{"ctrl+c", "quit", 2})
	}
	if hidden > 0 {
		parts = append(parts, hintPart{"", fmt.Sprintf("%d more below", hidden), 4})
	}
	return narrowed(parts, w)
}

func (sp *ScopePage) draw(s *screen) {
	p := styles()
	w, h := s.Size()
	lay := sp.layout(h)
	c := newCanvas(s)
	if lay.tooSmall {
		c.put(0, p.warn, clip(fmt.Sprintf("terminal too small: %dx%d, need %d rows", w, h, 3+scopeFoot), w))
		c.y = 1
		c.put(0, p.body, clip(sp.whereShort()+" · esc cancels", w))
		return
	}
	if lay.banner {
		c.y++
		n := len(sp.Banner)
		for _, b := range sp.Banner[:n-1] {
			c.put(0, p.brand, b)
			c.y++
		}
		c.put(0, p.body, sp.Banner[n-1])
		c.y += 2
	}
	c.put(0, p.title, clip(sp.Title, w))
	if sp.Step.Of > 1 {
		chip := fmt.Sprintf("scope · %d/%d", sp.Step.At, sp.Step.Of)
		if x := w - textWidth(chip); x > textWidth(sp.Title)+2 {
			c.put(x, p.accent, chip)
		}
	}
	c.y += 2
	if lay.filter {
		mark := ""
		if sp.filtering {
			mark = "_"
		}
		c.put(2, p.accent, "/")
		c.put(4, p.body, clip(sp.Tree.Filter()+mark, w-4))
		c.y++
	}

	rows := sp.Tree.Rows()
	at := sp.Tree.index(rows)
	end := at // the last row the cursor's folder holds
	for end+1 < len(rows) && sp.Tree.Holds(rows[end+1].Path) {
		end++
	}
	sp.top = min(max(sp.top, end-lay.window+1), at)
	sp.top = max(0, min(sp.top, len(rows)-lay.window))
	for i := 0; i < lay.window; i++ {
		sp.drawRow(c, p, rows[sp.top+i], rows[at], w)
		c.y++
	}
	c.y++
	c.put(0, p.body, clip(sp.where(), w))
	c.y += 2
	paintHint(c, p, sp.hintParts(w, len(rows)-sp.top-lay.window), 0, w)
}

// drawRow paints one folder: the cursor mark, a guide per folder above it, the fold
// mark, the name, and what a fold hides. Guides inside the cursor's folder are brand.
func (sp *ScopePage) drawRow(c *canvas, p palette, r, cur TreeRow, w int) {
	marker, name := "  ", p.body
	if r.Path == cur.Path {
		marker, name = "› ", p.brand
	}
	c.put(0, p.brand, marker)
	held := sp.Tree.Holds(r.Path)
	for d := range r.Depth {
		st := p.rule
		if held && d >= cur.Depth {
			st = p.brand
		}
		c.put(2+2*d, st, sp.guide())
	}
	pen := newPen(c.s, 2+2*r.Depth, c.y)
	if r.Folder && r.Path != "" {
		pen.write(p.idle, sp.foldMark(r.Folded))
	}
	label := r.Name
	if r.Path == "" {
		label = sp.Root
	}
	pen.write(name, clip(label, max(1, w-pen.col())))
	switch {
	case r.Path == "":
		pen.write(p.idle, "  repo root").write(p.accent, "  default")
	case r.Folded:
		pen.write(p.idle, fmt.Sprintf("  %d inside", r.Hidden))
	}
}

func (sp *ScopePage) guide() string {
	if sp.Glyphs == GlyphsASCII || sp.Glyphs == GlyphsOff {
		return "|"
	}
	return "│"
}

func (sp *ScopePage) foldMark(folded bool) string {
	ascii := sp.Glyphs == GlyphsASCII || sp.Glyphs == GlyphsOff
	switch {
	case folded && ascii:
		return "+ "
	case folded:
		return "▸ "
	case ascii:
		return "- "
	}
	return "▾ "
}
