package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// preInited hands Consent a screen that is already running, so the test can queue
// keystrokes before calling it. Injecting concurrently with Consent's own Init is
// a genuine race, not just flakiness.
type preInited struct{ tcell.SimulationScreen }

func (preInited) Init() error { return nil }
func (preInited) Fini()       {}

func consentScreen(t *testing.T) (tcell.SimulationScreen, func() (tcell.Screen, error)) {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 30)
	return sim, func() (tcell.Screen, error) { return preInited{sim}, nil }
}

func rendered(t *testing.T, sim tcell.SimulationScreen) string {
	t.Helper()
	cells, w, h := sim.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// The modal must OWN the screen. Printing a line into a running TUI interleaves
// with its rendering and corrupts the display — observed against opencode.
func TestConsentDrawsACenteredModal(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)
	drawConsent(sim, "api.kimi.com", "443")

	out := rendered(t, sim)
	for _, want := range []string{"api.kimi.com:443", "allow this connection?", "y  allow", "┌", "┘"} {
		if !strings.Contains(out, want) {
			t.Errorf("modal missing %q\n%s", want, out)
		}
	}
	// Centred, not pinned to the top-left corner.
	lines := strings.Split(out, "\n")
	var firstRow, col int
	for i, l := range lines {
		if j := strings.Index(l, "┌"); j >= 0 {
			firstRow, col = i, j
			break
		}
	}
	if firstRow < 5 {
		t.Errorf("modal starts at row %d — not vertically centred", firstRow)
	}
	if col < 20 {
		t.Errorf("modal starts at column %d — not horizontally centred", col)
	}
}

func TestConsentKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  tcell.Key
		r    rune
		want bool
	}{
		{"y allows", tcell.KeyRune, 'y', true},
		{"Y allows", tcell.KeyRune, 'Y', true},
		{"n denies", tcell.KeyRune, 'n', false},
		{"esc denies", tcell.KeyEscape, 0, false},
		{"enter defaults to deny", tcell.KeyEnter, 0, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sim, newScreen := consentScreen(t)
			sim.InjectKey(tc.key, tc.r, tcell.ModNone)
			got, err := Consent(newScreen, "example.com", "443")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Consent = %v, want %v", got, tc.want)
			}
		})
	}
}
