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
	Danger bool // a destructive answer: its mnemonic is drawn in the failure colour
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
}

// newDialog is a dialog whose safe answer is selected.
func newDialog(title string, lines []string, safe int, buttons ...dialogButton) dialog {
	return dialog{lines: lines, title: title, buttons: buttons, safe: safe, onCtrlC: safe, sel: safe, answer: -1, holdOn: -1}
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

// box is the bordered dialog: the body, then the button row and its key line, or the held line.
func (d dialog) box() string {
	p := styles()
	var rows []string
	for _, text := range d.lines {
		rows = append(rows, modalLineTea(p, text, d.title).Render(text))
	}
	if n := len(d.lines); n > 0 && d.lines[n-1] != "" {
		rows = append(rows, "")
	}
	if d.held != "" {
		rows = append(rows, p.warn.Render(fmt.Sprintf("%s — %s", d.held, d.now.Sub(d.heldAt).Round(time.Second))))
	} else {
		rows = append(rows, d.buttonRow(), "", p.aside.Render(d.keyLine()))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(hexOf(ui.ColorWarn)).
		Padding(0, modalPad).
		Render(strings.Join(rows, "\n"))
}

// buttonRow draws the answers left to right; the selected one is filled and carries the enter mark.
func (d dialog) buttonRow() string {
	cells := make([]string, len(d.buttons))
	for i, b := range d.buttons {
		cells[i] = d.button(b, i == d.sel)
	}
	return strings.Join(cells, "  ")
}

func (d dialog) button(b dialogButton, selected bool) string {
	face := lipgloss.NewStyle().Foreground(hexOf(ui.ColorSecondary)).Background(hexOf(ui.ColorRule))
	if selected {
		face = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(hexOf(ui.ColorBrand)).Bold(true)
	}
	key := face.Bold(true)
	if b.Danger {
		key = key.Foreground(hexOf(ui.ColorFail))
	}
	label := face.Render(b.Label)
	if i := strings.IndexRune(strings.ToLower(b.Label), unicode.ToLower(b.Key)); b.Key != 0 && i >= 0 {
		n := len(string(b.Key))
		label = face.Render(b.Label[:i]) + key.Render(b.Label[i:i+n]) + face.Render(b.Label[i+n:])
	}
	if selected {
		return face.Render(" ") + label + face.Render(" "+enterMark()+" ")
	}
	return face.Render(" ") + label + face.Render(" ")
}

// enterMark is the glyph the selected button carries.
func enterMark() string {
	if ui.GlyphTierFrom(os.Getenv) == ui.GlyphsASCII {
		return "<enter>"
	}
	return "↵"
}

// keyLine names the keys: how to move, what Enter does, and what Esc gives.
func (d dialog) keyLine() string {
	parts := []string{"←/→ move", "enter accept", "esc " + strings.ToLower(d.buttons[d.safe].Label)}
	if d.onCtrlC != d.safe {
		parts = append(parts, "ctrl+c "+strings.ToLower(d.buttons[d.onCtrlC].Label))
	}
	if ui.GlyphTierFrom(os.Getenv) == ui.GlyphsASCII {
		parts[0] = "left/right move"
	}
	return strings.Join(parts, " · ")
}

// Done reports whether the dialog has its answer; the proxy stops feeding it keys then.
func (d dialog) Done() bool { return d.answer >= 0 }

// dialogAnswer is the accepted button of a finished dialog model; ok is false for any other model.
func dialogAnswer(m tea.Model) (d dialog, ok bool) {
	d, ok = m.(dialog)
	return d, ok && d.answer >= 0
}
