// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

// teaPalette is the prompt palette as lipgloss styles.
type teaPalette struct {
	brand, accent, warn, body, typed, title, idle, aside lipgloss.Style
}

func teaStyles() teaPalette {
	fg := func(c int) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%06X", c)))
	}
	return teaPalette{
		brand:  fg(ui.ColorBrand).Bold(true),
		accent: fg(ui.ColorAccent).Bold(true),
		warn:   fg(ui.ColorWarn),
		body:   fg(ui.ColorSecondary),
		typed:  fg(ui.ColorSecondary).Bold(true),
		title:  lipgloss.NewStyle().Bold(true),
		idle:   fg(ui.ColorSecondary),
		aside:  fg(ui.ColorSecondary).Italic(true),
	}
}

// typedModal is the typed-answer confirm as a Bubble Tea model: body lines, one
// row per choice, an input field, and the key that ended it.
type typedModal struct {
	lines   []string
	title   string
	choices []modalChoice

	input  string
	signal modalSignal
	w, h   int
}

func (m typedModal) Init() tea.Cmd { return nil }

func (m typedModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.PasteMsg:
		m.input = modalTyped(m.input, msg.Content)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			m.signal = modalEsc
			return m, tea.Quit
		case "ctrl+c":
			m.signal = modalCtrlC
			return m, tea.Quit
		case "enter":
			m.signal = modalEnter
			return m, tea.Quit
		case "backspace":
			if r := []rune(m.input); len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		case "ctrl+u":
			m.input = ""
		default:
			m.input = modalTyped(m.input, msg.Text)
		}
	}
	return m, nil
}

// modalTyped appends the printable part of text to input, up to the field's cap.
func modalTyped(input, text string) string {
	for _, r := range text {
		if r < 0x20 || len([]rune(input)) >= modalInputMax {
			continue
		}
		input += string(r)
	}
	return input
}

func (m typedModal) View() tea.View {
	return tea.NewView(lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, m.box()))
}

// box is the bordered dialog: the body, the choices, the field, and "enter submits".
func (m typedModal) box() string {
	p := teaStyles()
	var rows []string
	for _, text := range m.lines {
		rows = append(rows, modalLineTea(p, text, m.title).Render(text))
	}
	if n := len(m.lines); n > 0 && m.lines[n-1] != "" {
		rows = append(rows, "")
	}
	keyW := 0
	for _, c := range m.choices {
		keyW = max(keyW, textWidth("["+c.Key+"]"))
	}
	for _, c := range m.choices {
		key := "[" + c.Key + "]"
		gap := strings.Repeat(" ", keyW-textWidth(key)) + "  "
		rows = append(rows, p.accent.Render(key)+p.body.Render(gap+c.Text))
	}
	rows = append(rows, "")
	marker := modalAskMarker()
	shown := []rune(m.input)
	if len(shown) > modalFieldRoom {
		shown = shown[len(shown)-modalFieldRoom:]
	}
	pad := strings.Repeat(" ", modalFieldRoom-len(shown))
	rows = append(rows,
		p.brand.Render(marker)+p.idle.Render("[")+p.typed.Render(string(shown))+p.brand.Render("_")+p.idle.Render(pad+"]"),
		p.aside.Render(strings.Repeat(" ", textWidth(marker))+"enter submits"),
	)
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(fmt.Sprintf("#%06X", ui.ColorWarn))).
		Padding(0, modalPad).
		Render(strings.Join(rows, "\n"))
}

// modalLineTea styles one body line: the title bold, a consequence in the warning colour.
func modalLineTea(p teaPalette, text, title string) lipgloss.Style {
	switch {
	case text == title:
		return p.title
	case modalWarns(text):
		return p.warn
	}
	return p.body
}
