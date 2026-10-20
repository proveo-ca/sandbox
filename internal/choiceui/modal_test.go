// SPEC: _spec/internal/sbx/clone-workspace.puml, _spec/internal/ptyproxy/pty-ownership.puml
package choiceui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestModalOptionsAreLeftRowsAndTheInputSitsAboveEnterSubmits(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 36)

	drawModal(sim, SandboxCloseCopy(), "close this sandbox?", sandboxCloseChoices(), "y")
	lines := strings.Split(rendered(t, sim), "\n")
	title := lineCol(t, lines, "close this sandbox?")
	for _, key := range []string{"[y/N]", "[esc]", "[ctrl+c]"} {
		if got := lineCol(t, lines, key); got.line == title.line || got.col != title.col {
			t.Errorf("%s at line %d col %d, want its own row at col %d with the title", key, got.line, got.col, title.col)
		}
	}
	desc := lineCol(t, lines, "remove the sandbox")
	for _, text := range []string{"go back", "interrupt the agent and keep the sandbox"} {
		if got := lineCol(t, lines, text); got.col != desc.col {
			t.Errorf("%q col %d, want %d", text, got.col, desc.col)
		}
	}
	input := lineCol(t, lines, "[y_")
	hint := lineCol(t, lines, "enter submits")
	if hint.line != input.line+1 || hint.col != input.col {
		t.Errorf("enter submits at line %d col %d, want the row under [y_ (line %d col %d)", hint.line, hint.col, input.line, input.col)
	}

	drawModal(sim, FetchPushedCopy([]string{"origin/feat"}, 15*time.Second), "the agent pushed these branches", fetchPushedChoices(), "")
	pushed := strings.Split(rendered(t, sim), "\n")
	head := lineCol(t, pushed, "the agent pushed these branches")
	for _, key := range []string{"[y/N]", "[esc]"} {
		if got := lineCol(t, pushed, key); got.line == head.line || got.col != head.col {
			t.Errorf("fetch %s at line %d col %d, want its own row at col %d", key, got.line, got.col, head.col)
		}
	}
	empty := lineCol(t, pushed, "[_")
	under := lineCol(t, pushed, "enter submits")
	if under.line != empty.line+1 || under.col != empty.col {
		t.Errorf("fetch enter submits at line %d col %d, want the row under [_ (line %d col %d)", under.line, under.col, empty.line, empty.col)
	}
}

func TestFetchPushedEnterSubmitsTheRecordedAnswer(t *testing.T) {
	t.Parallel()
	if !confirmFetch(t, time.Second, key('y'), keyEnter()) {
		t.Fatal("y then enter did not fetch")
	}
	if !confirmFetch(t, time.Second, key('y'), key('e'), key('s'), keyEnter()) {
		t.Fatal("yes then enter did not fetch")
	}
	if confirmFetch(t, time.Second, key('n'), keyEnter()) {
		t.Fatal("n then enter fetched")
	}
	if confirmFetch(t, time.Second, key('y'), backspace(), keyEnter()) {
		t.Fatal("a deleted y still fetched")
	}
	if confirmFetch(t, time.Second, keyEnter()) {
		t.Fatal("empty enter fetched")
	}
	if confirmFetch(t, time.Second, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) {
		t.Fatal("esc fetched")
	}
	if confirmFetch(t, 20*time.Millisecond) {
		t.Fatal("silence fetched")
	}
}

func confirmFetch(t *testing.T, wait time.Duration, keys ...*tcell.EventKey) bool {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 36)
	for _, ev := range keys {
		sim.InjectKey(ev.Key(), ev.Rune(), ev.Modifiers())
	}
	return ConfirmFetchPushed(sim, []string{"origin/feat"}, wait)
}

func keyEnter() *tcell.EventKey { return tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone) }

func backspace() *tcell.EventKey {
	return tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone)
}

type modalSpot struct {
	line int
	col  int
}

func lineCol(t *testing.T, lines []string, needle string) modalSpot {
	t.Helper()
	for i, line := range lines {
		if n := strings.Index(line, needle); n >= 0 {
			return modalSpot{line: i, col: textWidth(line[:n])}
		}
	}
	t.Fatalf("missing %q", needle)
	return modalSpot{}
}
