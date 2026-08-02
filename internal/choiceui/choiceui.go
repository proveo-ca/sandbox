// Package choiceui renders the one-shot harness choice prompt.
// SPEC: _spec/_plans/harness-choice-cache.puml
package choiceui

import (
	"fmt"
	"strings"

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
	defer screen.Fini()

	cursor := f.firstSelectable()
	for {
		f.draw(screen, cursor)
		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventKey:
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

// axisLabel reports whether any row is an ordered policy axis (a radio row with
// more than one option). Add-on checkboxes are unordered, so a lone add-ons row
// gets no legend.
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
		if !r.offAt(next) {
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

func styles() (brand, bold, dim tcell.Style) {
	hex := func(c int) tcell.Color { return tcell.NewHexColor(int32(c)) }
	return tcell.StyleDefault.Foreground(hex(ui.ColorBrand)).Bold(true),
		tcell.StyleDefault.Bold(true),
		tcell.StyleDefault.Foreground(hex(ui.ColorSecondary))
}

func (f *Form) draw(s tcell.Screen, cursor int) {
	s.Clear()
	sel, bold, dim := styles()

	y := 0
	put := func(x int, style tcell.Style, text string) {
		for i, r := range []rune(text) {
			s.SetContent(x+i, y, r, nil, style)
		}
	}

	for _, b := range f.Banner {
		put(0, sel, b)
		y++
	}
	if len(f.Banner) > 0 {
		y++
	}
	put(0, bold, f.Title)
	y += 2
	for _, h := range f.Header {
		put(2, dim, h)
		y++
	}
	if len(f.Header) > 0 {
		y++
	}

	// Axis legend above the rows: both policy axes are ordered riskier → safer, so
	// one label explains every row at once and the direction is not something the
	// operator has to infer from the option names.
	if f.axisLabel() {
		put(22, dim, "◀ riskier")
		put(60, dim, "safer ▶")
		y += 2
	}

	for i, r := range f.Rows {
		marker := "  "
		style := tcell.StyleDefault
		if i == cursor {
			marker = "› "
			style = bold
		}
		put(0, style, marker+r.Label)
		x := 22
		for j, opt := range r.Options {
			var glyph string
			st := tcell.StyleDefault
			switch {
			case r.Multi && r.onAt(j):
				glyph, st = "[x] ", sel
			case r.Multi:
				glyph = "[ ] "
			case j == r.Selected:
				glyph, st = "(•) ", sel
			default:
				glyph = "( ) "
			}
			if i == cursor && j == r.Selected {
				st = st.Underline(true)
			}
			if r.Locked || r.offAt(j) {
				st = dim
			}
			put(x, st, glyph+opt)
			x += len(glyph) + len(opt) + 3
		}
		if r.Reason != "" && (r.Locked || r.anyOff()) {
			put(x, dim, "— "+r.Reason)
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
	put(0, dim, hint)
	s.Show()
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
