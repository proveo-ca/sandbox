// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func checkPage() *CheckPage {
	return &CheckPage{
		Banner: Banner(),
		Title:  "init — what was checked on darwin/arm64",
		Step:   Step{At: 1, Of: 4},
		Next:   "setup",
		Rows: []CheckRow{
			{State: CheckOK, Group: "to install sbx", Name: "Apple silicon", Detail: "macOS Sonoma 14 or later on Apple silicon"},
			{State: CheckOK, Group: "sbx diagnose", Name: "virtualization", Detail: "Hypervisor.framework is available"},
			{State: CheckFail, Group: "sbx diagnose", Name: "daemon", Detail: "not running", Hint: "start it with `sbx daemon start`"},
			{State: CheckWarn, Group: "sbx diagnose", Name: "disk", Detail: "12 GB free", Hint: "a sandbox image needs about 8 GB"},
			{State: CheckSkip, Group: "sbx diagnose", Name: "kvm", Detail: "not a Linux host"},
		},
	}
}

func checkAt(cp *CheckPage, w, h int) tea.Model {
	m, _ := checkModel{page: cp}.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func checkLines(cp *CheckPage, w, h int) []string {
	s := newScreen(w, h)
	cp.draw(s)
	return screenLines(s)
}

func TestTheCheckPagesKeys(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		step    Step
		blocked string
		keys    []tea.KeyPressMsg
		out     Outcome
		ends    bool
	}{
		{"enter continues", Step{At: 1, Of: 4}, "", []tea.KeyPressMsg{teaEnter}, Accepted, true},
		{"esc on page 1 closes", Step{At: 1, Of: 4}, "", []tea.KeyPressMsg{teaEsc}, Cancelled, true},
		{"esc past page 1 steps back", Step{At: 2, Of: 4}, "", []tea.KeyPressMsg{teaEsc}, Back, true},
		{"ctrl+c asks first", Step{At: 1, Of: 4}, "", []tea.KeyPressMsg{teaCtrlC}, Cancelled, false},
		{"ctrl+c twice quits", Step{At: 1, Of: 4}, "", []tea.KeyPressMsg{teaCtrlC, teaCtrlC}, Quit, true},
		{"a blocked page does not continue", Step{At: 1, Of: 4}, "no KVM", []tea.KeyPressMsg{teaEnter}, Cancelled, false},
		{"a blocked page still closes", Step{At: 1, Of: 4}, "no KVM", []tea.KeyPressMsg{teaEnter, teaEsc}, Cancelled, true},
		{"scrolling does not leave", Step{At: 1, Of: 4}, "", []tea.KeyPressMsg{teaKey(tea.KeyDown, 0), teaKey(tea.KeyEnd, 0)}, Cancelled, false},
		{"a lone page cancels on ctrl+c at once", Step{}, "", []tea.KeyPressMsg{teaCtrlC}, Cancelled, true},
	} {
		cp := checkPage()
		cp.Step, cp.Blocked = c.step, c.blocked
		m, cmd := pressKeys(checkAt(cp, 100, 30), c.keys...)
		if got := m.(checkModel).out; quits(cmd) != c.ends || (c.ends && got != c.out) {
			t.Errorf("%s: ended %v, outcome %v; want %v, %v", c.name, quits(cmd), got, c.ends, c.out)
		}
	}
}

func TestTheCheckPageShowsEveryCheckItsMarkAndTheHintUnderOneThatFailed(t *testing.T) {
	t.Parallel()
	joined := strings.Join(checkLines(checkPage(), 110, 30), "\n")
	for _, want := range []string{
		"to install sbx", "✓ Apple silicon — macOS Sonoma 14 or later on Apple silicon",
		"sbx diagnose", "✓ virtualization", "× daemon — not running", "start it with `sbx daemon start`",
		"! disk — 12 GB free", "- kvm — not a Linux host",
		"2 of 5 checks need attention — enter continues to setup",
		"checks · 1/4", "page 1/4 · enter continue · esc cancel · ctrl+c quit",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the page lacks %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "sbx diagnose") != 1 {
		t.Errorf("a group's heading must draw once:\n%s", joined)
	}
	if strings.Contains(joined, "enter accept") {
		t.Errorf("the page says \"enter accept\", which e2e reads as a form:\n%s", joined)
	}
	clean := checkPage()
	clean.Rows = clean.Rows[:2]
	if joined = strings.Join(checkLines(clean, 110, 30), "\n"); !strings.Contains(joined, "every check passed — enter continues to setup") {
		t.Errorf("a clean page must say so:\n%s", joined)
	}
}

func TestABlockedCheckPageSaysWhyAndOffersNoWayOn(t *testing.T) {
	t.Parallel()
	cp := checkPage()
	cp.Blocked = "nothing good follows an install here: KVM"
	joined := strings.Join(checkLines(cp, 110, 30), "\n")
	if !strings.Contains(joined, "blocked: nothing good follows an install here: KVM") {
		t.Errorf("the page does not say why it is blocked:\n%s", joined)
	}
	if strings.Contains(joined, "enter continue") || !strings.Contains(joined, "esc cancel") {
		t.Errorf("a blocked page must offer the way out and no way on:\n%s", joined)
	}
}

func TestAShortCheckPageScrollsAndKeepsItsVerdictAndItsKeys(t *testing.T) {
	t.Parallel()
	cp := checkPage()
	joined := strings.Join(checkLines(cp, 110, 10), "\n")
	for _, want := range []string{"need attention", "enter continue", "↑↓ scroll", "more below"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a short page lacks %q:\n%s", want, joined)
		}
	}
	cp.key("end", 10)
	if joined = strings.Join(checkLines(cp, 110, 10), "\n"); !strings.Contains(joined, "kvm") || strings.Contains(joined, "more below") {
		t.Errorf("End must show the last check and hide nothing below:\n%s", joined)
	}
	tiny := strings.Join(checkLines(checkPage(), 110, 5), "\n")
	if !strings.Contains(tiny, "terminal too small") || !strings.Contains(tiny, "esc cancels") {
		t.Errorf("a terminal under the floor must say so:\n%s", tiny)
	}
}

// TestTheCheckPageMatchesItsGoldenFrames holds the page to recorded frames.
// PROVEO_UPDATE_FRAMES=1 records them.
func TestTheCheckPageMatchesItsGoldenFrames(t *testing.T) {
	dir := filepath.Join("testdata", "checks")
	update := os.Getenv("PROVEO_UPDATE_FRAMES") != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		page func() *CheckPage
	}{
		{"mixed", checkPage},
		{"clean", func() *CheckPage { cp := checkPage(); cp.Rows = cp.Rows[:2]; return cp }},
		{"blocked", func() *CheckPage {
			cp := checkPage()
			cp.Blocked = "nothing good follows an install here: KVM"
			return cp
		}},
		{"ascii", func() *CheckPage { cp := checkPage(); cp.Glyphs = GlyphsASCII; return cp }},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		for _, size := range [][2]int{{140, 40}, {80, 24}, {60, 10}} {
			name := fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1])
			seen[name] = true
			got := frameText(checkLines(c.page(), size[0], size[1]))
			path := filepath.Join(dir, name)
			if update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: no golden frame (%v); record it with PROVEO_UPDATE_FRAMES=1", name, err)
				continue
			}
			if got != string(want) {
				t.Errorf("%s differs from its golden frame:\n--- got ---\n%s--- want ---\n%s", name, got, want)
			}
		}
	}
	if !update {
		files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
		for _, f := range files {
			if !seen[filepath.Base(f)] {
				t.Errorf("%s is a golden frame no case draws; delete it", f)
			}
		}
	}
}
