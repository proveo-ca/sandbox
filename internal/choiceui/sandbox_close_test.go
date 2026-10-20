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
	drawModal(sim, SandboxCloseCopy(), "close this sandbox?", sandboxCloseChoices(), "")
	out := rendered(t, sim)
	for _, want := range []string{
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"[y/N]",
		"remove the sandbox",
		"[esc]",
		"[ctrl+c]",
		"interrupt the agent and keep the sandbox",
		"enter submits",
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
		keys []tcell.EventKey
		want SandboxClose
	}{
		{"y then enter removes", []tcell.EventKey{
			*tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone),
			*tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		}, SandboxCloseRemove},
		{"yes then enter removes", []tcell.EventKey{
			*tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone),
			*tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone),
			*tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone),
			*tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		}, SandboxCloseRemove},
		{"n then enter stays", []tcell.EventKey{
			*tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone),
			*tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		}, SandboxCloseStay},
		{"esc stays", []tcell.EventKey{*tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)}, SandboxCloseStay},
		{"enter stays", []tcell.EventKey{*tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)}, SandboxCloseStay},
		{"ctrl+c passes the interrupt", []tcell.EventKey{*tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)}, SandboxClosePass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sim := tcell.NewSimulationScreen("UTF-8")
			if err := sim.Init(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sim.Fini)
			sim.SetSize(80, 24)
			for _, ev := range tc.keys {
				sim.InjectKey(ev.Key(), ev.Rune(), ev.Modifiers())
			}
			if got := ConfirmSandboxClose(sim); got != tc.want {
				t.Errorf("ConfirmSandboxClose = %v, want %v", got, tc.want)
			}
		})
	}
}
