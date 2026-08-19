package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

func render(t *testing.T, f *Form) []string {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 40)
	f.draw(s, f.firstSelectable())

	cells, w, h := s.GetContents()
	lines := make([]string, 0, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		if line := strings.TrimRight(b.String(), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func joined(t *testing.T, f *Form) string { return strings.Join(render(t, f), "\n") }

func TestBannerRendersOnTop(t *testing.T) {
	t.Parallel()
	f := &Form{Banner: Banner(), Title: "run opencode", Rows: []Row{{Label: "egress", Options: []string{"open"}}}}
	lines := render(t, f)
	if len(lines) == 0 {
		t.Fatal("nothing rendered")
	}
	want := strings.Split(ui.BrandBanner, "\n")[0]
	if lines[0] != strings.TrimRight(want, " ") {
		t.Errorf("first line = %q, want ui.BrandBanner's first line %q", lines[0], want)
	}
	if !strings.Contains(joined(t, f), ui.BrandTagline) {
		t.Error("the brand tagline must render with the banner")
	}
	if !strings.Contains(joined(t, f), "run opencode") {
		t.Error("title must render below the banner")
	}
}

func TestHeaderShowsGitAndEnvWithSecretsByNameOnly(t *testing.T) {
	t.Parallel()
	header := append([]string{"git:      sandbox on main (uncommitted changes)"},
		EnvHeader([]string{"ANTHROPIC_API_KEY"}, map[string]string{"ARCHITECT_MODEL": "claude-opus-5"})...)
	f := &Form{Banner: Banner(), Title: "t", Header: header, Rows: []Row{{Label: "egress", Options: []string{"open"}}}}
	out := joined(t, f)

	for _, want := range []string{"sandbox on main", "uncommitted changes", "ANTHROPIC_API_KEY", "ARCHITECT_MODEL=claude-opus-5"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q\n--- rendered ---\n%s", want, out)
		}
	}
}

func TestEnvHeaderNeverPrintsASecretValue(t *testing.T) {
	t.Parallel()
	const secret = "sk-do-not-render-this"
	lines := EnvHeader([]string{"ANTHROPIC_API_KEY"}, map[string]string{"DARK_MODE": "1"})
	all := strings.Join(lines, "\n")
	if strings.Contains(all, secret) {
		t.Fatalf("EnvHeader leaked a secret value:\n%s", all)
	}
	if !strings.Contains(all, "ANTHROPIC_API_KEY") {
		t.Error("secret keys must still be listed by name")
	}
	if !strings.Contains(all, "DARK_MODE=1") {
		t.Error("non-secret settings must show their value")
	}
}

func TestSingleSelectRowsCycleAndReportSelection(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist", "review"}, Selected: 1},
	}}
	if got := f.Selection("egress"); got != "allowlist" {
		t.Fatalf("pre-selected = %q, want allowlist", got)
	}
	f.cycle(0, +1)
	if got := f.Selection("egress"); got != "review" {
		t.Errorf("after → = %q, want review", got)
	}
	f.cycle(0, +1) // wraps
	if got := f.Selection("egress"); got != "open" {
		t.Errorf("after wrap = %q, want open", got)
	}
}

func TestLockedRowIsSkippedAndCannotChange(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open"}, Locked: true, Reason: "only tier"},
		{Label: "credentials", Options: []string{"broker", "forward"}},
	}}
	if got := f.firstSelectable(); got != 1 {
		t.Errorf("cursor started on row %d, want the first unlocked row (1)", got)
	}
	f.cycle(0, +1)
	if got := f.Selection("egress"); got != "open" {
		t.Errorf("a locked row must not change, got %q", got)
	}
	if !strings.Contains(joined(t, f), "only tier") {
		t.Error("a locked row must render its reason rather than hide the row")
	}
}

func TestMultiRowTogglesIndependently(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true, On: make([]bool, 2)},
	}}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Fatalf("nothing should start checked, got %v", got)
	}
	f.toggle(0) // browser
	f.cycle(0, +1)
	f.toggle(0) // dind
	got := f.Selections("add-ons")
	if len(got) != 2 || got[0] != "browser" || got[1] != "dind" {
		t.Errorf("both add-ons should be checked, got %v", got)
	}
	f.toggle(0) // dind off
	if got := f.Selections("add-ons"); len(got) != 1 || got[0] != "browser" {
		t.Errorf("toggling dind off must leave browser on, got %v", got)
	}
}

func TestDisabledAddonIsNeitherCheckableNorReported(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true, On: []bool{false, true}, Off: []bool{false, true}},
	}}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Errorf("a disabled option must not be reported even if On, got %v", got)
	}
	f.Rows[0].Selected = 1
	f.toggle(0)
	if f.Rows[0].On[1] != true {
		t.Error("toggle must not mutate a disabled option")
	}
}

func TestOnChangeFiresSoConstraintsStayLive(t *testing.T) {
	t.Parallel()
	calls := 0
	f := &Form{Rows: []Row{{Label: "egress", Options: []string{"open", "allowlist"}}}}
	f.OnChange = func(*Form) { calls++ }
	f.cycle(0, +1)
	if calls != 1 {
		t.Errorf("OnChange fired %d times after a change, want 1", calls)
	}
}

// A gated option is unreachable by design — cycle() will not land on it — so the
// row MUST say why, or the operator sees an unresponsive checkbox with no cause.
func TestGatedOptionRendersItsReason(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true,
		On: make([]bool, 2), Off: []bool{false, true},
		Reason: "dind needs egress open + credentials forward",
	}}}
	if !strings.Contains(joined(t, f), "dind needs egress open") {
		t.Errorf("a gated option must render its reason\n--- rendered ---\n%s", joined(t, f))
	}
	f.Rows[0].Selected = 0
	f.cycle(0, +1)
	if f.Rows[0].Selected != 0 {
		t.Error("cycle must not land on a gated option")
	}
}

// Both policy axes are ordered riskier → safer; the legend says so once rather
// than leaving the operator to infer direction from option names.
func TestSafetyAxisLegend(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist", "review"}, Selected: 1},
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1},
	}}
	out := joined(t, f)
	if !strings.Contains(out, "riskier") || !strings.Contains(out, "safer") {
		t.Errorf("no safety axis rendered:\n%s", out)
	}
	// A prompt with only unordered checkboxes must not claim an axis.
	only := &Form{Rows: []Row{{Label: "add-ons", Options: []string{"browser"}, Multi: true, On: []bool{false}}}}
	if o := joined(t, only); strings.Contains(o, "riskier") {
		t.Errorf("checkbox-only prompt should have no axis legend:\n%s", o)
	}
}

// The safety axis governs the POLICY rows only. A centred divider closes it so the
// legend does not appear to order the add-on toggles, which are unordered.
func TestAddonsDividerClosesTheSafetyAxis(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist", "review"}, Selected: 1},
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1},
		{Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true, On: make([]bool, 2)},
	}}
	lines := render(t, f)
	out := strings.Join(lines, "\n")

	var axisRow, divRow, addonRow = -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "riskier"):
			axisRow = i
		case strings.Contains(l, "─ add-ons ─"):
			divRow = i
		case strings.Contains(l, "[ ] browser"):
			addonRow = i
		}
	}
	if divRow < 0 {
		t.Fatalf("no centred add-ons divider rendered:\n%s", out)
	}
	if axisRow >= divRow || divRow >= addonRow {
		t.Errorf("order = axis:%d divider:%d addons:%d, want the divider between them", axisRow, divRow, addonRow)
	}
	// The divider names the section, so the row must not repeat the label.
	if strings.Contains(lines[addonRow], "add-ons") {
		t.Errorf("add-ons label repeated on its row: %q", lines[addonRow])
	}
	// It should be indented, not flush left like the policy labels.
	if i := strings.Index(lines[divRow], "─"); i < 10 {
		t.Errorf("divider starts at column %d — not centred", i)
	}
}

func TestSupportingTextUsesLightPaletteNotSlate(t *testing.T) {
	t.Parallel()
	f := &Form{
		Title:  "run cursor",
		Header: []string{"git:      sandbox on main", "keys:     CURSOR_API_KEY"},
		Rows:   []Row{{Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true, On: []bool{true, false}}},
	}
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 40)
	f.draw(s, 0)

	cells, w, h := s.GetContents()
	slate := tcell.NewHexColor(int32(ui.ColorCloud))
	light := tcell.NewHexColor(int32(ui.ColorSecondary))
	var sawLight, sawSlate bool
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fg, _, _ := cells[y*w+x].Style.Decompose()
			if fg == light {
				sawLight = true
			}
			if fg == slate {
				sawSlate = true
			}
		}
	}
	if !sawLight {
		t.Error("header/hint body text must use ColorSecondary (light), not terminal default alone")
	}
	if sawSlate {
		t.Error("ColorCloud slate is too dark for TUI body text on a black background")
	}
}

// Only the row the divider is named after gives up its own label. A second
// checkbox section has no divider to name it, so dropping its label too would
// leave the operator staring at an anonymous pair of boxes.
func TestSecondMultiRowKeepsItsLabel(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist"}, Selected: 1},
		{Label: "add-ons", Options: []string{"browser", "dind"}, Multi: true, On: make([]bool, 2)},
		{Label: "agent evidence", Options: []string{"default", "verbose"}, Multi: true, On: []bool{false, true}},
	}}
	lines := render(t, f)
	out := strings.Join(lines, "\n")

	var addonRow, evidenceRow = -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "[ ] browser"):
			addonRow = i
		case strings.Contains(l, "[x] verbose"):
			evidenceRow = i
		}
	}
	if addonRow < 0 || evidenceRow < 0 {
		t.Fatalf("both checkbox rows should render:\n%s", out)
	}
	if strings.Contains(lines[addonRow], "add-ons") {
		t.Errorf("the divider already names add-ons; row repeats it: %q", lines[addonRow])
	}
	if !strings.Contains(lines[evidenceRow], "agent evidence") {
		t.Errorf("second checkbox row lost its label: %q", lines[evidenceRow])
	}
	if strings.Count(out, "─ agent evidence ─") != 0 {
		t.Errorf("only the first checkbox row gets a divider:\n%s", out)
	}
}
