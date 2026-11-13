// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package choiceui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CheckState is how one check came out.
type CheckState int

const (
	CheckOK CheckState = iota
	CheckWarn
	CheckFail
	CheckSkip
)

// CheckRow is one check on a CheckPage.
type CheckRow struct {
	State    CheckState
	Name     string
	Detail   string
	Evidence []string // what the check found; drawn under a row that is not CheckOK, above its hint
	Hint     string   // what to do about it; drawn under a row that is not CheckOK
	Group    string   // a heading drawn above the first row that carries it
}

// CheckPage shows what was checked before a wizard asks anything: one row per check.
type CheckPage struct {
	Banner  []string
	Title   string
	Rows    []CheckRow
	Step    Step
	Next    string // what Enter continues to: "setup"
	Blocked string // why the wizard cannot go on; when set, Enter does nothing
	Glyphs  GlyphTier

	top int // the first line in the window
}

// Run draws the page on the terminal: Accepted when the operator continues.
func (cp *CheckPage) Run() (Outcome, error) {
	in, term, done, err := openTerminal()
	if err != nil {
		return Cancelled, fmt.Errorf("check page: %w", err)
	}
	defer done()
	final, err := tea.NewProgram(checkModel{page: cp},
		tea.WithInput(in), tea.WithOutput(term), tea.WithoutSignalHandler()).Run()
	if err != nil {
		return Cancelled, fmt.Errorf("check page: %w", err)
	}
	if m, ok := final.(checkModel); ok {
		return m.out, nil
	}
	return Cancelled, nil
}

// checkModel runs a CheckPage as a Bubble Tea model.
type checkModel struct {
	page *CheckPage
	w, h int
	out  Outcome
	quit quitGate
}

func (m checkModel) Init() tea.Cmd { return nil }

func (m checkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		out, done := m.page.key(msg.String(), m.h)
		switch {
		case done && out == Quit && m.page.layout(m.h).window >= 1:
			m.quit = m.quit.open(m.w, m.h, m.page.Step.Leave, m.page.Glyphs)
		case done:
			m.out = out
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m checkModel) View() tea.View {
	if m.quit.up() {
		return m.quit.view()
	}
	s := newScreen(m.w, m.h)
	m.page.draw(s)
	v := tea.NewView(s.view())
	v.AltScreen = true
	return v
}

// checkLine is one drawn line of the list: a heading, a check, or a line of evidence or the hint under one.
type checkLine struct {
	kind  int // 0 heading, 1 check, 2 hint, 3 evidence
	row   CheckRow
	title string // the heading, or the line of evidence
}

func (cp *CheckPage) lines() []checkLine {
	var out []checkLine
	group := ""
	for _, r := range cp.Rows {
		if r.Group != "" && r.Group != group {
			group = r.Group
			out = append(out, checkLine{kind: 0, title: group})
		}
		out = append(out, checkLine{kind: 1, row: r})
		if r.State == CheckOK {
			continue
		}
		for _, e := range r.Evidence {
			out = append(out, checkLine{kind: 3, row: r, title: e})
		}
		if r.Hint != "" {
			out = append(out, checkLine{kind: 2, row: r})
		}
	}
	return out
}

// checkFoot is the rows under the list: the foot rule, the verdict, the key line, the bottom border;
// unframed, a blank, the verdict, a blank, the key line.
const checkFoot = 4

// checkLayout is which of the page's regions a terminal of this height holds.
type checkLayout struct {
	banner bool
	window int // list lines on screen; under 1 the page cannot draw
}

func (cp *CheckPage) layout(h int) checkLayout {
	var lay checkLayout
	n := len(cp.lines())
	fixed := 2 + checkFoot // the top border and the title; unframed, the title and a blank
	if banner := len(cp.Banner) + 2; len(cp.Banner) > 0 && h >= fixed+banner+n {
		lay.banner = true
		fixed += banner
	}
	lay.window = min(n, h-fixed)
	return lay
}

// key applies one key press: it scrolls, continues, or leaves.
func (cp *CheckPage) key(key string, h int) (Outcome, bool) {
	switch key {
	case "ctrl+c":
		if cp.Step.wizard() {
			return Quit, true
		}
		return Cancelled, true
	case "esc":
		if cp.Step.backable() {
			return Back, true
		}
		return Cancelled, true
	case "enter":
		return Accepted, cp.Blocked == ""
	}
	window := cp.layout(h).window
	if window < 1 {
		return Cancelled, false
	}
	last := max(0, len(cp.lines())-window)
	switch key {
	case "up":
		cp.top = max(0, cp.top-1)
	case "down":
		cp.top = min(last, cp.top+1)
	case "pgup":
		cp.top = max(0, cp.top-window)
	case "pgdown":
		cp.top = min(last, cp.top+window)
	case "home":
		cp.top = 0
	case "end":
		cp.top = last
	}
	return Cancelled, false
}

// verdict is the sentence under the list: what the checks add up to, and what Enter does about it.
func (cp *CheckPage) verdict() (string, bool) {
	if cp.Blocked != "" {
		return "blocked: " + cp.Blocked, true
	}
	attention := 0
	for _, r := range cp.Rows {
		if r.State == CheckWarn || r.State == CheckFail {
			attention++
		}
	}
	if attention == 0 {
		return fmt.Sprintf("every check passed — enter continues to %s", cp.Next), false
	}
	return fmt.Sprintf("%d of %s need attention — enter continues to %s", attention, plural(len(cp.Rows), "check"), cp.Next), true
}

// chip is the head's step chip on a w-column terminal, or "".
func (cp *CheckPage) chip(w int) string { return stepChip(cp.Step, "checks ·", cp.Title, w) }

func (cp *CheckPage) hintParts(w, hidden int) []hintPart {
	parts := pageHint(cp.Step, cp.chip(w))
	if hidden > 0 || cp.top > 0 {
		parts = append(parts, hintPart{"↑↓", "scroll", 3})
	}
	if cp.Blocked == "" {
		parts = append(parts, hintPart{"enter", "continue", 9})
	}
	esc := hintPart{"esc", "cancel", 8}
	if cp.Step.backable() {
		esc.word = "back"
	}
	parts = append(parts, esc)
	if cp.Step.wizard() {
		parts = append(parts, hintPart{"ctrl+c", "quit", 2})
	}
	if hidden > 0 {
		parts = append(parts, hintPart{"", fmt.Sprintf("%d more below", hidden), 4})
	}
	return narrowed(parts, keyRoom(w))
}

// hint is the key line a w-column terminal shows, as plain text.
func (cp *CheckPage) hint(w, hidden int) string {
	return clip(joinHint(cp.hintParts(w, hidden)), keyRoom(w))
}

// mark is a check's glyph and the style it is drawn in.
func (cp *CheckPage) mark(st CheckState, p palette) (string, lipgloss.Style) {
	ascii := plainGlyphs(cp.Glyphs)
	switch st {
	case CheckWarn:
		return "!", p.warn
	case CheckFail:
		if ascii {
			return "x", p.fail
		}
		return "×", p.fail
	case CheckSkip:
		return "-", p.idle
	}
	if ascii {
		return "+", p.ok
	}
	return "✓", p.ok
}

func (cp *CheckPage) draw(s *screen) {
	p := styles()
	w, h := s.Size()
	c := newCanvas(s)
	lay := cp.layout(h)
	if lay.window < 1 {
		c.put(0, p.warn, clip(fmt.Sprintf("terminal too small: %dx%d, need %d rows", w, h, 3+checkFoot), w))
		c.y = 1
		verdict, _ := cp.verdict()
		c.put(0, p.body, clip(verdict+" · esc cancels", w))
		return
	}
	lines := cp.lines()
	if lay.banner {
		drawBanner(c, p, cp.Banner)
	}
	k := newCard(w, cp.Glyphs, p)
	x0 := inset(w)
	k.head(c, cp.Title, cp.chip(w))
	cp.top = max(0, min(cp.top, len(lines)-lay.window))
	for i := 0; i < lay.window; i++ {
		k.line(c, false, func() { cp.drawLine(c, p, lines[cp.top+i], k.edge) })
	}
	k.foot(c)
	c.y++
	verdict, attention := cp.verdict()
	st := p.body
	if attention {
		st = p.warn
	}
	k.line(c, false, func() { c.put(x0, st, clip(verdict, keyRoom(w))) })
	if !k.framed {
		c.y++
	}
	k.line(c, false, func() { paintHint(c, p, cp.hintParts(w, len(lines)-cp.top-lay.window), x0, keyRoom(w)) })
	k.bottom(c)
}

// drawLine paints one list line short of column edge: a heading at 2, a check's mark at 2 and its
// name at 4, a line of evidence or the hint at 6.
func (cp *CheckPage) drawLine(c *canvas, p palette, ln checkLine, edge int) {
	switch ln.kind {
	case 0:
		c.put(2, p.accent, clip(ln.title, max(1, edge-2)))
	case 1:
		glyph, st := cp.mark(ln.row.State, p)
		c.put(2, st, glyph)
		name := clip(ln.row.Name, max(1, edge-4))
		c.put(4, p.title, name)
		if x := 4 + textWidth(name); ln.row.Detail != "" && x < edge {
			c.put(x, p.body, clip(" — "+ln.row.Detail, edge-x))
		}
	case 2:
		c.put(6, p.aside, clip(ln.row.Hint, max(1, edge-6)))
	case 3:
		c.put(6, p.idle, clip(ln.title, max(1, edge-6)))
	}
}
