// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

// dialogButton is one answer in a dialog's button row.
type dialogButton struct {
	Label  string
	Key    rune // the mnemonic: pressing it selects the button
	Danger bool // a destructive answer: its mnemonic is drawn on the failure colour
}

// dialog is a confirmation as a Bubble Tea model: body lines over one row of
// buttons. Enter gives the selected button, and the safe one is selected first.
type dialog struct {
	lines   []string
	title   string
	buttons []dialogButton
	safe    int           // what Enter gives untouched, and what Esc and a timeout give
	onCtrlC int           // what ctrl+c gives
	wait    time.Duration // answer `safe` after this long without a key; 0 waits

	// hold keeps the dialog up after button `holdOn` is accepted: it runs once,
	// and the line it returns replaces the buttons until the Program is ended.
	hold   func() string
	holdOn int

	sel      int
	answer   int // the accepted button; -1 while the dialog is open
	held     string
	heldAt   time.Time
	now      time.Time
	timedOut bool
	w, h     int
	glyphs   GlyphTier
}

// newDialog is a dialog whose safe answer is selected, drawn in the glyph tier PROVEO_GLYPHS names.
func newDialog(title string, lines []string, safe int, buttons ...dialogButton) dialog {
	return dialog{lines: lines, title: title, buttons: buttons, safe: safe, onCtrlC: safe, sel: safe, answer: -1, holdOn: -1,
		glyphs: ui.GlyphTierFrom(os.Getenv)}
}

type (
	dialogTimeout struct{}
	dialogTick    struct{ at time.Time }
)

func (d dialog) Init() tea.Cmd {
	if d.wait <= 0 {
		return nil
	}
	return tea.Tick(d.wait, func(time.Time) tea.Msg { return dialogTimeout{} })
}

func (d dialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.w, d.h = msg.Width, msg.Height
	case dialogTimeout:
		if d.answer < 0 {
			d.timedOut = true
			return d.accept(d.safe)
		}
	case dialogTick:
		d.now = msg.at
		return d, holdTick()
	case tea.KeyPressMsg:
		if d.held != "" {
			return d, nil
		}
		switch key := msg.String(); key {
		case "enter":
			return d.accept(d.sel)
		case "esc":
			return d.accept(d.safe)
		case "ctrl+c":
			return d.accept(d.onCtrlC)
		case "left", "shift+tab":
			d.sel = (d.sel + len(d.buttons) - 1) % len(d.buttons)
		case "right", "tab":
			d.sel = (d.sel + 1) % len(d.buttons)
		default:
			if i, ok := d.buttonFor(msg.Text); ok {
				d.sel = i
			}
		}
	}
	return d, nil
}

// accept records button i. It ends the dialog, unless i is the answer the dialog holds on.
func (d dialog) accept(i int) (tea.Model, tea.Cmd) {
	d.answer = i
	if i != d.holdOn || d.hold == nil {
		return d, tea.Quit
	}
	d.held, d.heldAt = d.hold(), time.Now()
	d.now = d.heldAt
	return d, holdTick()
}

func holdTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return dialogTick{at: t} })
}

// buttonFor is the button a typed character selects: its mnemonic, or y for the
// first answer that is not the safe one and n for the safe one.
func (d dialog) buttonFor(text string) (int, bool) {
	r := []rune(strings.ToLower(text))
	if len(r) != 1 {
		return 0, false
	}
	for i, b := range d.buttons {
		if unicode.ToLower(b.Key) == r[0] {
			return i, true
		}
	}
	switch r[0] {
	case 'n':
		return d.safe, true
	case 'y':
		for i := range d.buttons {
			if i != d.safe {
				return i, true
			}
		}
	}
	return 0, false
}

func (d dialog) View() tea.View {
	v := tea.NewView(lipgloss.Place(d.w, d.h, lipgloss.Center, lipgloss.Center, d.box()))
	v.AltScreen = true
	return v
}

// box is the dialog's card, sized to its content: the title, the body, a rule, then the button row,
// a blank and the key line, or the held line.
func (d dialog) box() string {
	p := styles()
	lines := d.lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	held := ""
	if d.held != "" {
		held = fmt.Sprintf("%s — %s", d.held, d.now.Sub(d.heldAt).Round(time.Second))
	}
	w, h := 0, len(lines)+3
	grow := func(x, cols int) { w = max(w, x+cols+modalRight) }
	for _, text := range lines {
		if text == d.title {
			grow(modalTitleX, textWidth(text))
		} else {
			grow(modalBodyX, textWidth(text))
		}
	}
	if held != "" {
		grow(modalBodyX, textWidth(held))
		h++
	} else {
		grow(modalButtonX, lipgloss.Width(d.buttonRow()))
		grow(modalKeyX, textWidth(joinHint(d.keyParts())))
		h += 3
	}

	s := newScreen(w, h)
	c := newCanvas(s)
	k := card{w: w, framed: true, edge: w - 2, g: glyphsFor(d.glyphs), p: p}
	k.border(c, k.g.frameTL, k.g.frameTR)
	c.y++
	for _, text := range lines {
		k.line(c, false, func() {
			switch {
			case text == d.title:
				c.put(modalTitleX, p.title, text)
			case modalWarns(text):
				c.put(modalTitleX, p.alert, "!")
				c.put(modalBodyX, p.title, text)
			default:
				c.put(modalBodyX, p.body, text)
			}
		})
	}
	k.foot(c)
	c.y++
	if held != "" {
		k.line(c, false, func() { c.put(modalBodyX, p.warn, held) })
	} else {
		k.line(c, false, func() { d.putButtons(c, modalButtonX) })
		k.line(c, false, func() {})
		k.line(c, false, func() { paintHint(c, p, d.keyParts(), modalKeyX, k.edge-modalKeyX) })
	}
	k.bottom(c)
	return s.view()
}

// buttonRow draws the answers left to right; the selected one is filled and carries the enter mark.
func (d dialog) buttonRow() string {
	cells := make([]string, len(d.buttons))
	for i, b := range d.buttons {
		cells[i] = d.button(b, i == d.sel)
	}
	return strings.Join(cells, "  ")
}

// putButtons paints the button row from column x.
func (d dialog) putButtons(c *canvas, x int) {
	for i, b := range d.buttons {
		if i > 0 {
			x += 2
		}
		for _, pc := range d.buttonPieces(b, i == d.sel) {
			c.put(x, pc.st, pc.text)
			x += textWidth(pc.text)
		}
	}
}

func (d dialog) button(b dialogButton, selected bool) string {
	var out strings.Builder
	for _, pc := range d.buttonPieces(b, selected) {
		out.WriteString(pc.st.Render(pc.text))
	}
	return out.String()
}

// buttonPieces are one button's runs: its face, its mnemonic, and the enter mark when selected.
func (d dialog) buttonPieces(b dialogButton, selected bool) []piece {
	face := fill(ui.ColorSecondary, ui.ColorSurface)
	if selected {
		face = fill(black, ui.ColorBrand).Bold(true)
	}
	key := face.Bold(true)
	if b.Danger {
		key = fill(ui.ColorWhite, ui.ColorFail).Bold(true)
	}
	pieces := []piece{{face, " "}}
	if i := strings.IndexRune(strings.ToLower(b.Label), unicode.ToLower(b.Key)); b.Key != 0 && i >= 0 {
		n := len(string(b.Key))
		pieces = append(pieces, piece{face, b.Label[:i]}, piece{key, b.Label[i : i+n]}, piece{face, b.Label[i+n:]})
	} else {
		pieces = append(pieces, piece{face, b.Label})
	}
	if selected {
		return append(pieces, piece{face, " " + enterMark(d.glyphs) + " "})
	}
	return append(pieces, piece{face, " "})
}

// enterMark is the glyph the selected button carries.
func enterMark(t GlyphTier) string {
	if plainGlyphs(t) {
		return "<enter>"
	}
	return "↵"
}

// keyParts are the key line's segments: how to move, what Enter does, and what Esc and ctrl+c give.
func (d dialog) keyParts() []hintPart {
	move := "←/→"
	if plainGlyphs(d.glyphs) {
		move = "left/right"
	}
	parts := []hintPart{{move, "move", 0}, {"enter", "accept", 0}, {"esc", strings.ToLower(d.buttons[d.safe].Label), 0}}
	if d.onCtrlC != d.safe {
		parts = append(parts, hintPart{"ctrl+c", strings.ToLower(d.buttons[d.onCtrlC].Label), 0})
	}
	return parts
}

// Done reports whether the dialog has its answer; the proxy stops feeding it keys then.
func (d dialog) Done() bool { return d.answer >= 0 }

// dialogAnswer is the accepted button of a finished dialog model; ok is false for any other model.
func dialogAnswer(m tea.Model) (d dialog, ok bool) {
	d, ok = m.(dialog)
	return d, ok && d.answer >= 0
}
