package choiceui

import (
	"strings"
	"testing"
)

func lockedForm() *Form {
	return &Form{
		Title: "t",
		Rows: []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"},
				Selected: 0, Locked: true,
				Reason: "host-wide, not per-run — to change, run on the host: `sbx policy reset && sbx policy init allow-all|balanced|deny-all`",
				Help: map[string]string{
					"allow-all": "every destination the host baseline permits",
					"balanced":  "the host's own allows, plus proveo's",
					"deny-all":  "nothing but what the Kit adds",
				}},
			{Label: "credentials", Options: []string{"broker", "forward"}, Selected: 0},
		},
	}
}

// A locked row reports a FACT the operator cannot change, and its options are
// the only statement of what that fact implies. The cursor used to skip it, so
// they were unreadable.
func TestLockedRowIsHoverableButNeverChangeable(t *testing.T) {
	f := lockedForm()
	// The cursor starts on something answerable, never on the fact.
	if got := f.firstSelectable(); got != 1 {
		t.Fatalf("firstSelectable = %d, want the credentials row", got)
	}
	// ...but it can move onto the locked row.
	if got := f.move(1, -1); got != 0 {
		t.Fatalf("move onto a locked row = %d, want 0", got)
	}
	// Arrows read; they do not choose.
	for i := 0; i < 2; i++ {
		f.cycle(0, 1)
	}
	if f.Rows[0].Selected != 0 {
		t.Errorf("hovering rewrote the answer: Selected = %d", f.Rows[0].Selected)
	}
	if f.Rows[0].Hover != 2 {
		t.Errorf("Hover = %d, want 2", f.Rows[0].Hover)
	}
	if got := f.Selection("egress"); got != "allow-all" {
		t.Errorf("Selection = %q, want the baseline unchanged", got)
	}
	// The help block describes what the HOVERED option implies, and states the
	// row's reason in full rather than clipped.
	lines := f.Rows[0].helpLines(70)
	var text []string
	for _, l := range lines {
		text = append(text, l.text)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "deny-all") || !strings.Contains(joined, "nothing but what the Kit adds") {
		t.Errorf("hovered option not described:\n%s", joined)
	}
	if !strings.Contains(joined, "sbx policy init") {
		t.Errorf("the row's reason must appear IN FULL:\n%s", joined)
	}
}

// The reason has ONE home: the help block, on hover. It used to trail the
// options clipped to whatever width was left, which meant the row's only
// runnable command existed nowhere in full.
func TestLockedRowReasonLivesOnlyInTheHelpBlock(t *testing.T) {
	t.Parallel()
	f := lockedForm()
	offRow := strings.Join(renderAt(t, f, 1, 150, 40), "\n")
	onRow := strings.Join(renderAt(t, f, 0, 150, 40), "\n")

	if strings.Contains(offRow, "host-wide, not per-run") {
		t.Errorf("nothing inline when the cursor is elsewhere:\n%s", offRow)
	}
	if n := strings.Count(onRow, "host-wide, not per-run"); n != 1 {
		t.Errorf("reason appears %d times while hovering, want 1:\n%s", n, onRow)
	}
	if !strings.Contains(onRow, "sbx policy init") {
		t.Errorf("the full command must be readable while hovering:\n%s", onRow)
	}
}
