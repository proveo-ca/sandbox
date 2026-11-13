// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// FilterList is a list narrowed by typing. Enter takes the cursor's row.
type FilterList struct {
	Title  string
	Prompt string // drawn before the typed text, as given: "attach> "
	Items  []string

	query string
	at    int // the cursor, as an index into shown()
	top   int
}

// Run draws the list on the terminal and returns the row taken, as a one-index slice.
func (l *FilterList) Run() (picked []int, out Outcome, err error) {
	in, term, done, err := openTerminal()
	if err != nil {
		return nil, Cancelled, fmt.Errorf("list: %w", err)
	}
	defer done()
	final, err := tea.NewProgram(listModel{list: l},
		tea.WithInput(in), tea.WithOutput(term), tea.WithoutSignalHandler()).Run()
	if err != nil {
		return nil, Cancelled, fmt.Errorf("list: %w", err)
	}
	if m, ok := final.(listModel); ok && m.out == Accepted {
		return l.picked(), Accepted, nil
	}
	return nil, Cancelled, nil
}

// ErrListClosed is Pick's error when the operator closed the list without choosing.
var ErrListClosed = errors.New("list closed")

// Pick runs the list and returns the row taken, never none.
func (l *FilterList) Pick() ([]int, error) {
	picked, out, err := l.Run()
	switch {
	case err != nil:
		return nil, err
	case out != Accepted || len(picked) == 0:
		return nil, ErrListClosed
	}
	return picked, nil
}

// listModel runs a FilterList as a Bubble Tea model.
type listModel struct {
	list *FilterList
	w, h int
	out  Outcome
}

func (m listModel) Init() tea.Cmd { return nil }

func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		p := press{key: msg.String()}
		if msg.Text != "" {
			p = press{text: msg.Text}
		}
		if out, done := m.list.key(p, m.h); done {
			m.out = out
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m listModel) View() tea.View {
	s := newScreen(m.w, m.h)
	m.list.draw(s)
	v := tea.NewView(s.view())
	v.AltScreen = true
	return v
}

// shown is the indexes of the items that hold every word typed, in list order.
func (l *FilterList) shown() []int {
	words := strings.Fields(strings.ToLower(l.query))
	var out []int
	for i, item := range l.Items {
		low := strings.ToLower(item)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
			out = append(out, i)
		}
	}
	return out
}

// picked is what Enter takes: the cursor's row, or none when nothing matches.
func (l *FilterList) picked() []int {
	if shown := l.shown(); l.at < len(shown) {
		return []int{shown[l.at]}
	}
	return nil
}

// listFoot is the rows under the list: a blank and the key line.
const listFoot = 2

// window is how many list rows a terminal of height h holds; under 1 the list cannot draw.
func (l *FilterList) window(h int) int {
	fixed := 1 + listFoot // the prompt line
	if l.Title != "" {
		fixed += 2
	}
	return h - fixed
}

// key applies one key press: it moves the cursor, edits the text, or ends the list.
func (l *FilterList) key(p press, h int) (Outcome, bool) {
	if p.key == "esc" || p.key == "ctrl+c" {
		return Cancelled, true
	}
	window := l.window(h)
	if window < 1 {
		return Cancelled, false
	}
	if p.key == "enter" {
		return Accepted, len(l.picked()) > 0
	}
	shown := l.shown()
	move := func(delta int) { l.at = max(0, min(l.at+delta, len(shown)-1)) }
	switch {
	case p.key == "up":
		move(-1)
	case p.key == "down":
		move(+1)
	case p.key == "pgup":
		move(-window)
	case p.key == "pgdown":
		move(+window)
	case p.key == "home":
		move(-len(shown))
	case p.key == "end":
		move(+len(shown))
	case p.text != "":
		l.query, l.at = l.query+p.text, 0
	case p.key == "backspace":
		if runes := []rune(l.query); len(runes) > 0 {
			l.query, l.at = string(runes[:len(runes)-1]), 0
		}
	case p.key == "ctrl+u":
		l.query, l.at = "", 0
	}
	return Cancelled, false
}

func (l *FilterList) hintParts(w, hidden int) []hintPart {
	parts := []hintPart{{"↑↓", "row", 3}, {"type", "to narrow", 1}, {"enter", "choose", 9}, {"esc", "cancel", 5}}
	if hidden > 0 {
		parts = append(parts, hintPart{"", fmt.Sprintf("%d more below", hidden), 4})
	}
	return narrowed(parts, w)
}

func (l *FilterList) draw(s *screen) {
	p := styles()
	w, h := s.Size()
	c := newCanvas(s)
	window := l.window(h)
	if window < 1 {
		c.put(0, p.warn, clip(fmt.Sprintf("terminal too small: %dx%d, need %d rows", w, h, h-window+1), w))
		c.y = 1
		c.put(0, p.body, clip("esc cancels", w))
		return
	}
	if l.Title != "" {
		c.put(0, p.title, clip(l.Title, w))
		c.y += 2
	}
	pen := newPen(s, 0, c.y)
	pen.write(p.accent, l.Prompt)
	pen.write(p.body, clip(l.query+"_", max(1, w-pen.col())))
	c.y++

	shown := l.shown()
	l.at = max(0, min(l.at, len(shown)-1))
	l.top = min(max(l.top, l.at-window+1), l.at)
	l.top = max(0, min(l.top, len(shown)-window))
	if len(shown) == 0 {
		c.put(2, p.idle, clip("nothing matches", max(1, w-2)))
	}
	for i := 0; i < window && l.top+i < len(shown); i++ {
		item := shown[l.top+i]
		marker, st := "  ", p.body
		if l.top+i == l.at {
			marker, st = "› ", p.brand
		}
		row := newPen(s, 0, c.y+i)
		row.write(p.brand, marker)
		row.write(st, clip(l.Items[item], max(1, w-row.col())))
	}
	c.y += min(window, max(1, len(shown))) + 1
	paintHint(c, p, l.hintParts(w, len(shown)-l.top-window), 0, w)
}
