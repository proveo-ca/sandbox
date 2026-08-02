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
