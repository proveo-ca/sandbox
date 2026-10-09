// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestSandboxCloseNamesTheForceRemove(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)
	drawSandboxClose(sim)
	out := rendered(t, sim)
	for _, want := range []string{
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"y  remove the sandbox",
		"ctrl+c  interrupt the agent and keep the sandbox",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("modal missing %q\n%s", want, out)
		}
	}
}

func TestSandboxCloseKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  tcell.Key
		r    rune
		want SandboxClose
	}{
		{"y removes", tcell.KeyRune, 'y', SandboxCloseRemove},
		{"n stays", tcell.KeyRune, 'n', SandboxCloseStay},
		{"esc stays", tcell.KeyEscape, 0, SandboxCloseStay},
		{"enter stays", tcell.KeyEnter, 0, SandboxCloseStay},
		{"ctrl+c passes the interrupt", tcell.KeyCtrlC, 0, SandboxClosePass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sim := tcell.NewSimulationScreen("UTF-8")
			if err := sim.Init(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sim.Fini)
			sim.SetSize(80, 24)
			sim.InjectKey(tc.key, tc.r, tcell.ModNone)
			if got := ConfirmSandboxClose(sim); got != tc.want {
				t.Errorf("ConfirmSandboxClose = %v, want %v", got, tc.want)
			}
		})
	}
}
