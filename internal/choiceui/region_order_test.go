package choiceui

import (
	"strings"
	"testing"
)

// The figure is the LAST region: hint, then the help block, then the strip.
// SPEC: _spec/internal/choiceui/topology-strip.puml
func TestFigureStaysBelowTheHintAndTheHelpBlock(t *testing.T) {
	t.Parallel()
	fr := Frame{Host: "pluvo", HostOS: "darwin", Square: "sbx · claudecode",
		Interface: "tui", Hop: "sbx proxy", Lane: LaneWatched, Open: 3,
		Caption: "allow-all — the host baseline"}
	f := &Form{
		Title: "run claudecode",
		Rows: []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}, Locked: true,
				Reason: "host-wide, not per-run — to change, run on the host: `sbx policy reset && sbx policy init allow-all|balanced|deny-all`",
				Help:   map[string]string{"allow-all": "every destination the host baseline permits"}},
			{Label: "add-ons", Options: []string{"tui", "browser", "claude-in-chrome"}, Multi: true,
				Selected: 2, On: []bool{true, true, false}, Off: []bool{true, false, true},
				Help:   map[string]string{"claude-in-chrome": "Claude Code drives YOUR Chrome"},
				OffWhy: map[string]string{"claude-in-chrome": "docker backend only — set PROVEO_SBX=0"}},
		},
		Topology: func(_ *Form, _ int) *Frame { return &fr },
	}
	for _, cursor := range []int{0, 1} {
		rows := renderAt(t, f, cursor, 150, 40)
		hint, help, figure := -1, -1, -1
		for i, l := range rows {
			switch {
			case strings.Contains(l, "enter accept"):
				hint = i
			case strings.Contains(l, "sbx policy init") || strings.Contains(l, "docker backend only"):
				if help < 0 {
					help = i
				}
			case strings.Contains(l, "sbx · claudecode"):
				figure = i
			}
		}
		t.Logf("cursor=%d  hint=%d help=%d figure=%d", cursor, hint, help, figure)
		if hint < 0 || help < 0 || figure < 0 {
			t.Errorf("cursor=%d: missing region (hint=%d help=%d figure=%d)", cursor, hint, help, figure)
			continue
		}
		if hint >= help || help >= figure {
			t.Errorf("cursor=%d: order must be hint < help < figure, got %d %d %d", cursor, hint, help, figure)
		}
	}
}
