// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gdamore/tcell/v2"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// plainLines is a view's text without styling, right-trimmed, one string per row.
func plainLines(s string) []string {
	lines := strings.Split(sgr.ReplaceAllString(s, ""), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

func press(m tea.Model, keys ...tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = m.Update(k)
	}
	return m, cmd
}

func typed(s string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, r := range s {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}

var (
	teaEnter     = tea.KeyPressMsg{Code: tea.KeyEnter}
	teaEsc       = tea.KeyPressMsg{Code: tea.KeyEscape}
	teaBackspace = tea.KeyPressMsg{Code: tea.KeyBackspace}
	teaCtrlC     = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	teaCtrlU     = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
)

func TestTheCloseDialogAnswersAsItsTcellTwinDoes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		keys []tea.KeyPressMsg
		want SandboxClose
	}{
		{"y then enter", append(typed("y"), teaEnter), SandboxCloseRemove},
		{"yes then enter", append(typed("Yes"), teaEnter), SandboxCloseRemove},
		{"n then enter", append(typed("n"), teaEnter), SandboxCloseStay},
		{"enter alone", []tea.KeyPressMsg{teaEnter}, SandboxCloseStay},
		{"y then esc", append(typed("y"), teaEsc), SandboxCloseStay},
		{"y then ctrl+c", append(typed("y"), teaCtrlC), SandboxClosePass},
		{"y, backspace, enter", append(typed("y"), teaBackspace, teaEnter), SandboxCloseStay},
		{"no, ctrl+u, y, enter", append(append(typed("no"), teaCtrlU), append(typed("y"), teaEnter)...), SandboxCloseRemove},
	} {
		m, cmd := press(NewSandboxClose(), c.keys...)
		if cmd == nil {
			t.Errorf("%s: the dialog did not end", c.name)
			continue
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: the ending command is not Quit", c.name)
		}
		if got := SandboxCloseAnswer(m); got != c.want {
			t.Errorf("%s: answer = %d, want %d", c.name, got, c.want)
		}
	}
	if got := SandboxCloseAnswer(nil); got != SandboxCloseStay {
		t.Errorf("a missing model answered %d, want stay", got)
	}
}

func TestTheTypedFieldTakesTextAndPasteUpToItsCap(t *testing.T) {
	t.Parallel()
	m, cmd := press(NewSandboxClose(), typed("ab")...)
	if cmd != nil {
		t.Fatal("typing ended the dialog")
	}
	m, _ = m.Update(tea.PasteMsg{Content: "cd\nef\x1b"})
	if got := m.(typedModal).input; got != "abcdef" {
		t.Errorf("input = %q, want the printable text only", got)
	}
	m, _ = press(m, typed(strings.Repeat("x", 2*modalInputMax))...)
	if n := len([]rune(m.(typedModal).input)); n != modalInputMax {
		t.Errorf("input holds %d runes, want the cap %d", n, modalInputMax)
	}
}

// TestTheLipglossDialogMatchesTheTcellDialogCellForCell holds the rewritten view to
// the renderer it replaces, at each size and input.
func TestTheLipglossDialogMatchesTheTcellDialogCellForCell(t *testing.T) {
	t.Parallel()
	modals := []struct {
		name    string
		lines   []string
		title   string
		choices []modalChoice
	}{
		{"close", SandboxCloseCopy(), "close this sandbox?", sandboxCloseChoices()},
		{"pushed", FetchPushedCopy([]string{"origin/feat", "origin/fix"}, 15*time.Second), "the agent pushed these branches", fetchPushedChoices()},
	}
	for _, size := range [][2]int{{100, 36}, {80, 24}, {120, 40}, {61, 19}, {200, 60}} {
		for _, input := range []string{"", "y", "a much longer answer than the field has room for"} {
			for _, md := range modals {
				sim := tcell.NewSimulationScreen("UTF-8")
				if err := sim.Init(); err != nil {
					t.Fatal(err)
				}
				sim.SetSize(size[0], size[1])
				drawModal(sim, md.lines, md.title, md.choices, input)
				want := plainLines(rendered(t, sim))
				sim.Fini()

				m := typedModal{lines: md.lines, title: md.title, choices: md.choices, input: input, w: size[0], h: size[1]}
				got := plainLines(m.View().Content)
				for len(want) > 0 && want[len(want)-1] == "" {
					want = want[:len(want)-1]
				}
				for len(got) > 0 && got[len(got)-1] == "" {
					got = got[:len(got)-1]
				}
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s at %dx%d, input %q:\n--- lipgloss ---\n%s\n--- tcell ---\n%s",
						md.name, size[0], size[1], input, strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
			}
		}
	}
}
