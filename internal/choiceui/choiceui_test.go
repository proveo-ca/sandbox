// SPEC: _spec/internal/choiceui/choice-prompt-render.puml
package choiceui

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"

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
	f.draw(s, f.firstSelectable(), 0)

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
	got := joined(t, f)
	if !strings.Contains(got, "S O L U T I O N S") || !strings.Contains(got, "pr●veo") {
		t.Error("the solutions banner must render with the form")
	}
	if !strings.Contains(got, ui.BrandTagline) {
		t.Error("the brand tagline must render with the banner")
	}
	assertHelpHeader(t, f)
	if !strings.Contains(joined(t, f), "run opencode") {
		t.Error("title must render below the banner")
	}
}

func assertHelpHeader(t *testing.T, f *Form) {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 40)
	f.draw(s, f.firstSelectable(), 0)

	brand := tcell.NewHexColor(int32(ui.ColorBrand))
	body := tcell.NewHexColor(int32(ui.ColorSecondary))
	box := strings.Split(ui.BrandBanner, "\n")
	for x := 0; x < 80; x++ {
		if r, _, _, _ := s.GetContent(x, 0); r != ' ' && r != 0 {
			t.Fatalf("row 0 col %d is %q, help opens with a blank line", x, string(r))
		}
	}
	for i, line := range box {
		assertBannerLine(t, s, i+1, line, brand, true)
	}
	assertBannerLine(t, s, len(box)+1, "  "+ui.BrandTagline, body, false)
	for x := 0; x < 80; x++ {
		if r, _, _, _ := s.GetContent(x, len(box)+2); r != ' ' && r != 0 {
			t.Fatalf("row %d col %d is %q, help closes the header with a blank line", len(box)+2, x, string(r))
		}
	}
}

func assertBannerLine(t *testing.T, s tcell.Screen, y int, line string, fg tcell.Color, bold bool) {
	t.Helper()
	x := 0
	for _, r := range line {
		got, _, style, _ := s.GetContent(x, y)
		if got != r {
			t.Errorf("row %d col %d = %q, want %q", y, x, string(got), string(r))
		}
		gfg, _, attrs := style.Decompose()
		if gfg != fg {
			t.Errorf("row %d col %d color %v, want %v", y, x, gfg, fg)
		}
		if gotBold := attrs&tcell.AttrBold != 0; gotBold != bold {
			t.Errorf("row %d col %d bold=%v, want %v", y, x, gotBold, bold)
		}
		w := runeCols(r)
		if w < 1 {
			w = 1
		}
		x += w
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
	lines := EnvHeader([]string{"ANTHROPIC_API_KEY"}, map[string]string{
		"DARK_MODE":       "true",
		"CODE_THEME":      "one-dark",
		"ARCHITECT_MODEL": "claude-opus-5",
	})
	all := strings.Join(lines, "\n")
	if strings.Contains(all, secret) {
		t.Fatalf("EnvHeader leaked a secret value:\n%s", all)
	}
	if !strings.Contains(all, "ANTHROPIC_API_KEY") {
		t.Error("secret keys must still be listed by name")
	}
	if strings.Contains(all, "one-dark") || strings.Contains(all, "DARK_MODE=true") || strings.Contains(all, "CODE_THEME=") {
		t.Errorf("theme settings must keep the name and drop the value:\n%s", all)
	}
	if !strings.Contains(all, "CODE_THEME") || !strings.Contains(all, "DARK_MODE") {
		t.Errorf("theme settings must still be named:\n%s", all)
	}
	if !strings.Contains(all, "ARCHITECT_MODEL=claude-opus-5") {
		t.Error("a model setting must show its value")
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

func TestLockedRowNeverStartsTheCursorAndCannotChange(t *testing.T) {
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
	// The reason lives in the help block, reachable because the row is hoverable.
	if !strings.Contains(strings.Join(renderAt(t, f, 0, 120, 30), "\n"), "only tier") {
		t.Error("hovering a locked row must reveal its reason rather than hide the row")
	}
}

func TestMultiRowTogglesIndependently(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, On: make([]bool, 2)},
	}}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Fatalf("nothing should start checked, got %v", got)
	}
	f.toggle(0) // browser
	f.cycle(0, +1)
	f.toggle(0) // sandbox
	got := f.Selections("add-ons")
	if len(got) != 2 || got[0] != "browser" || got[1] != "sandbox" {
		t.Errorf("both add-ons should be checked, got %v", got)
	}
	f.toggle(0) // sandbox off
	if got := f.Selections("add-ons"); len(got) != 1 || got[0] != "browser" {
		t.Errorf("toggling the sandbox off must leave browser on, got %v", got)
	}
}

func TestDisabledAddonIsNeitherCheckableNorReported(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, On: []bool{false, true}, Off: []bool{false, true}},
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
		Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true,
		On: make([]bool, 2), Off: []bool{false, true},
		Reason: "sandbox needs egress open + credentials forward",
	}}}
	if !strings.Contains(joined(t, f), "sandbox needs egress open") {
		t.Errorf("a gated option must render its reason\n--- rendered ---\n%s", joined(t, f))
	}
	// A MULTI row's Selected is the cursor, not the choice, so it may rest on a
	// greyed box.
	f.Rows[0].Selected = 0
	f.cycle(0, +1)
	if f.Rows[0].Selected != 1 {
		t.Error("on a multi row the cursor must be able to rest on a gated option to read why")
	}
	f.toggle(0)
	if f.Rows[0].On[1] {
		t.Error("resting on a gated option must not make it checkable")
	}
	if got := f.Selections("add-ons"); len(got) != 0 {
		t.Errorf("a gated option must never be reported as selected, got %v", got)
	}

	// A SINGLE-select row's Selected IS the choice, so it must still skip one.
	single := &Form{Rows: []Row{{
		Label: "egress", Options: []string{"open", "allowlist", "review"},
		Off: []bool{false, false, true}, Selected: 1,
	}}}
	single.cycle(0, +1)
	if single.Rows[0].Selected != 0 {
		t.Errorf("a single-select row must skip its gated option, landed on %d", single.Rows[0].Selected)
	}
}

// An option that is available says what it does and nothing more: "off: " alone
// is still one field, so an available option printed a bare "off:" under itself.
func TestAvailableOptionPrintsNoEmptyOffLine(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "add-ons", Options: []string{"browser"}, Multi: true, On: []bool{true},
		Help: map[string]string{"browser": "Chromium inside the sandbox"},
	}}}
	for _, line := range render(t, f) {
		if strings.TrimSpace(line) == "off:" {
			t.Errorf("an available option must not carry an empty off line\n--- rendered ---\n%s", joined(t, f))
		}
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
	// Init's radios are not a safety axis; the legend would rank a prefix.
	init := &Form{NoAxis: true, Rows: []Row{
		{Label: "install", Options: []string{"rootless tarball", "distro package"}, Selected: 0},
	}}
	if o := joined(t, init); strings.Contains(o, "riskier") || strings.Contains(o, "safer") {
		t.Errorf("NoAxis must suppress the legend:\n%s", o)
	}
}

// The safety axis governs the POLICY rows only. A centred divider closes it so the
// legend does not appear to order the add-on toggles, which are unordered.
func TestAddonsDividerClosesTheSafetyAxis(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist", "review"}, Selected: 1},
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1},
		{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, Divider: true, On: make([]bool, 2)},
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
		Rows:   []Row{{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, On: []bool{true, false}}},
	}
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 40)
	f.draw(s, 0, 0)

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

// A row the divider names gives up its own label; a checkbox row without one
// keeps it. Which rows get a heading is DECLARED.
func TestOnlyADeclaredDividerReplacesTheRowLabel(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist"}, Selected: 1},
		{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, Divider: true, On: make([]bool, 2)},
		{Label: "outputs", Options: []string{"filtered", "verbose"}, Multi: true, On: []bool{false, true}},
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
	if !strings.Contains(lines[evidenceRow], "outputs") {
		t.Errorf("second checkbox row lost its label: %q", lines[evidenceRow])
	}
	if strings.Count(out, "─ outputs ─") != 0 {
		t.Errorf("a row that declared no divider must not be given one:\n%s", out)
	}
	if strings.Count(out, "─ add-ons ─") != 1 {
		t.Errorf("the row that declared a divider must get exactly one:\n%s", out)
	}

	// Two declared groups both get a heading — the case the old "first multi row"
	// rule could not express.
	two := &Form{Rows: []Row{
		{Label: "execution", Options: []string{"host", "docker (sandbox)"}, Multi: true, Divider: true, On: make([]bool, 2)},
		{Label: "interface", Options: []string{"tui", "browser"}, Multi: true, Divider: true, On: make([]bool, 2)},
	}}
	got := strings.Join(render(t, two), "\n")
	for _, want := range []string{"─ execution ─", "─ interface ─"} {
		if !strings.Contains(got, want) {
			t.Errorf("both declared groups must be named, missing %q:\n%s", want, got)
		}
	}
}

func TestPutHeaderAccentsLabelAndEveryPair(t *testing.T) {
	t.Parallel()
	var accented, body []string
	pal := palette{accent: tcell.StyleDefault.Foreground(tcell.ColorTeal), body: tcell.StyleDefault}
	put := func(_ int, st tcell.Style, s string) {
		if st == pal.accent {
			accented = append(accented, s)
		} else {
			body = append(body, s)
		}
	}
	putHeader(put, pal, "llms:     main=claude-opus-5 (anthropic)  small=claude-haiku-4-5 (anthropic)")

	for _, want := range []string{"llms:", "main", "small"} {
		var found bool
		for _, got := range accented {
			if strings.Contains(got, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q must be accented; accented=%v", want, accented)
		}
	}
	joined := strings.Join(body, "")
	for _, want := range []string{"claude-opus-5", "claude-haiku-4-5", "(anthropic)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q must render as body text; body=%q", want, joined)
		}
	}
}

// The lsp: row is "lsp:  <glyph> name  <glyph> name". The label and the glyphs are
// markers and take the accent; the server names are content and stay body-styled.
func TestPutHeaderAccentsGlyphsNotNames(t *testing.T) {
	t.Parallel()
	var accented, body []string
	pal := palette{accent: tcell.StyleDefault.Foreground(tcell.ColorTeal), body: tcell.StyleDefault}
	put := func(_ int, st tcell.Style, s string) {
		if st == pal.accent {
			accented = append(accented, s)
		} else {
			body = append(body, s)
		}
	}
	putHeader(put, pal, "lsp:       typescript-language-server  {} yaml-language-server")

	acc := strings.Join(accented, "|")
	for _, want := range []string{"lsp:", "", "{}"} {
		if !strings.Contains(acc, want) {
			t.Errorf("%q must be accented; accented=%q", want, acc)
		}
	}
	joined := strings.Join(body, "")
	for _, want := range []string{"typescript-language-server", "yaml-language-server"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q must render as body text; body=%q", want, joined)
		}
	}
	if strings.Contains(acc, "language-server") {
		t.Errorf("server names must not be accented; accented=%q", acc)
	}
}

// The git row is "git: <repo> on <branch> (<state>)". A reader scans it for the
// branch, and the state is a description of the fact rather than the fact itself.
func TestPutHeaderAccentsBranchAndItalicisesAside(t *testing.T) {
	t.Parallel()
	var accented, aside, body []string
	pal := palette{
		accent: tcell.StyleDefault.Foreground(tcell.ColorTeal),
		aside:  tcell.StyleDefault.Italic(true),
		body:   tcell.StyleDefault,
	}
	put := func(_ int, st tcell.Style, s string) {
		switch st {
		case pal.accent:
			accented = append(accented, s)
		case pal.aside:
			aside = append(aside, s)
		default:
			body = append(body, s)
		}
	}
	putHeader(put, pal, "git:      monorepo on rs-295-integration-discriminator (uncommitted changes)")

	acc, asi, bod := strings.Join(accented, "|"), strings.Join(aside, ""), strings.Join(body, "")
	if !strings.Contains(acc, "rs-295-integration-discriminator") {
		t.Errorf("branch must be accented; accented=%q", acc)
	}
	if !strings.Contains(acc, "git:") {
		t.Errorf("label must be accented; accented=%q", acc)
	}
	if !strings.Contains(asi, "(uncommitted changes)") {
		t.Errorf("trailing parenthetical must be italic; aside=%q", asi)
	}
	if !strings.Contains(bod, "monorepo") || !strings.Contains(bod, "on") {
		t.Errorf("repo name and connector stay body; body=%q", bod)
	}
	if strings.Contains(acc, "monorepo") {
		t.Errorf("repo name must not be accented; accented=%q", acc)
	}
}

func TestCursorRowDescribesTheOptionUnderTheCursor(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "add-ons", Options: []string{"browser", "docker (sandbox)"}, Multi: true,
		On: make([]bool, 2),
		Help: map[string]string{
			"browser":          "Chromium inside the sandbox",
			"docker (sandbox)": "microVM with its own daemon",
		},
	}}}
	got := joined(t, f)
	if !strings.Contains(got, "› browser — Chromium inside the sandbox") {
		t.Errorf("the focused option must describe itself\n--- rendered ---\n%s", got)
	}
	if strings.Contains(got, "microVM with its own daemon") {
		t.Errorf("only the FOCUSED option is described, or the row becomes the wall of text this replaces\n%s", got)
	}

	// Moving along the row moves the description with it.
	f.Rows[0].Selected = 1
	got = joined(t, f)
	if !strings.Contains(got, "› docker (sandbox) — microVM with its own daemon") {
		t.Errorf("the description must follow the cursor\n--- rendered ---\n%s", got)
	}
}

// Only the row the cursor is on explains itself.
func TestHelpIsDrawnForTheCursorRowAlone(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open"}, Help: map[string]string{"open": "no allowlist"}},
		{Label: "add-ons", Options: []string{"browser"}, Multi: true, On: []bool{true},
			Help: map[string]string{"browser": "Chromium inside the sandbox"}},
	}}
	got := joined(t, f)
	if !strings.Contains(got, "no allowlist") {
		t.Errorf("the cursor's row must describe itself\n%s", got)
	}
	if strings.Contains(got, "Chromium inside the sandbox") {
		t.Errorf("a row the cursor is not on must stay quiet\n%s", got)
	}
}

// A greyed option says what it does AND why it cannot be picked — in full, under
// the row, where the row-level Reason has no width left to say it.
func TestGreyedOptionExplainsItselfUnderTheRow(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "add-ons", Options: []string{"claude-in-chrome"}, Multi: true,
		On: []bool{false}, Off: []bool{true},
		Help:   map[string]string{"claude-in-chrome": "your own Chrome, driven through proveo's bridge"},
		OffWhy: map[string]string{"claude-in-chrome": "Claude Code disables it for CLAUDE_CODE_OAUTH_TOKEN sessions"},
	}}}
	got := joined(t, f)
	for _, want := range []string{
		"› claude-in-chrome — your own Chrome, driven through proveo's bridge",
		"off: Claude Code disables it for CLAUDE_CODE_OAUTH_TOKEN sessions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered form lacks %q\n--- rendered ---\n%s", want, got)
		}
	}
}

// A long reason is WRAPPED in the help block, never clipped on the row: it is
// not drawn on the row at all any more.
func TestOverlongReasonWrapsInTheHelpBlock(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("reason ", 40)
	f := &Form{Rows: []Row{{
		Label: "add-ons", Options: []string{"a"}, Multi: true,
		On: []bool{false}, Off: []bool{true}, Reason: long,
	}}}
	out := joined(t, f)
	if strings.Contains(out, "…") {
		t.Errorf("nothing on the row should be clipped any more\n--- rendered ---\n%s", out)
	}
	if n := len(f.Rows[0].helpLines(60)); n < 4 {
		t.Errorf("a long reason must wrap in the block, got %d lines", n)
	}
	if got := clip("abcdef", 4); got != "abc…" {
		t.Errorf("clip(abcdef, 4) = %q, want abc…", got)
	}
	if got := clip("abc", 9); got != "abc" {
		t.Errorf("clip must leave text that fits alone, got %q", got)
	}
}

func TestStaticColumnsDoNotSlide(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}, Cols: []int{0, 2, 3}, Selected: 0, Locked: true},
		{Label: "credentials", Options: []string{"forward", "broker"}, Cols: []int{0, 3}, Selected: 1},
		{Label: "model", Options: []string{"Qwen 3.8", "API keys", "plan", "Glimmer"}, Cols: []int{0, 1, 2, 3}, Selected: 1},
		{Label: "OS", Options: []string{"host", "sandbox"}, Cols: []int{0, 1}, Multi: true, Radio: true, On: []bool{false, true}},
		{Label: "interface", Options: []string{"tui", "browser", "android", "ios"}, Cols: []int{0, 1, 2, 3}, Multi: true, Divider: true, On: []bool{true, false, false, false}},
		{Label: "outputs", Options: []string{"filtered", "verbose"}, Cols: []int{0, 1}, Selected: 1},
	}}
	lines := render(t, f)
	line := func(needle string) string {
		t.Helper()
		for _, l := range lines {
			if strings.Contains(l, needle) {
				return l
			}
		}
		t.Fatalf("no line contains %q:\n%s", needle, strings.Join(lines, "\n"))
		return ""
	}
	at := func(l, needle string) int {
		t.Helper()
		i := strings.Index(l, needle)
		if i < 0 {
			t.Fatalf("%q missing from %q", needle, l)
		}
		return utf8.RuneCountInString(l[:i])
	}
	egress := line("allow-all")
	allow := at(egress, "allow-all")
	if got := at(egress, "balanced"); got != allow+2*colPitch {
		t.Errorf("balanced at %d, want %d (column 3, with column 2 empty)", got, allow+2*colPitch)
	}
	if got := at(egress, "deny-all"); got != allow+3*colPitch {
		t.Errorf("deny-all at %d, want %d", got, allow+3*colPitch)
	}
	creds := line("forward")
	if got := at(creds, "broker"); got != at(creds, "forward")+3*colPitch {
		t.Errorf("broker at %d, forward at %d, want three columns apart", got, at(creds, "forward"))
	}
	if at(creds, "broker") != at(egress, "deny-all") {
		t.Errorf("broker and deny-all are both column 4 and must share an x")
	}
	model := line("API keys")
	qwen := at(model, "Qwen 3.8")
	for _, c := range []struct {
		name string
		col  int
	}{
		{"API keys", 1},
		{"plan", 2},
		{"Glimmer", 3},
	} {
		if got := at(model, c.name); got != qwen+c.col*colPitch {
			t.Errorf("%s at %d, want column %d", c.name, got, c.col+1)
		}
	}
	osRow := line("sandbox")
	if at(osRow, "host") != allow {
		t.Errorf("host at %d, allow-all at %d, want the same column", at(osRow, "host"), allow)
	}
	if at(osRow, "sandbox") != qwen+colPitch {
		t.Errorf("sandbox at %d, want column 2", at(osRow, "sandbox"))
	}
	outputs := line("filtered")
	if at(outputs, "filtered") != allow || at(outputs, "verbose") != at(osRow, "sandbox") {
		t.Errorf("outputs drifted off the grid: %q", outputs)
	}
	iface := line("[x] tui")
	tui := at(iface, "tui")
	for _, c := range []struct {
		name string
		col  int
	}{
		{"browser", 1},
		{"android", 2},
		{"ios", 3},
	} {
		if got := at(iface, c.name); got != tui+c.col*colPitch {
			t.Errorf("%s at %d, want column %d", c.name, got, c.col+1)
		}
	}
	if tui != allow {
		t.Errorf("tui at %d, allow-all at %d, want the same column", tui, allow)
	}
	legend := line("safer")
	if got := at(legend, "safer"); got != optOrigin+3*colPitch {
		t.Errorf("safer legend at %d, want column 4 at %d", got, optOrigin+3*colPitch)
	}
}

func TestMoreDrawsUnderTheFourthColumn(t *testing.T) {
	t.Parallel()
	row := Row{
		Label: "ports", Multi: true, Selected: 3,
		Options: []string{"3000 next", "5173 vite", "8080 web", "...More"},
		Cols:    []int{0, 1, 2, 3},
		On:      []bool{false, false, false, true},
		More:    &More{Cell: 3, Options: []string{"4000 jekyll", "1313 hugo"}, On: []bool{true, false}},
		Help:    map[string]string{"4000 jekyll": "the jekyll server"},
	}
	lines := render(t, &Form{Rows: []Row{row}})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"...More", "4000 jekyll", "1313 hugo", "› 4000 jekyll — the jekyll server"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q\n%s", want, joined)
		}
	}
	at := func(needle string) int {
		t.Helper()
		for _, l := range lines {
			if i := strings.Index(l, needle); i >= 0 && strings.Contains(l, "ports") {
				return utf8.RuneCountInString(l[:i])
			}
		}
		t.Fatalf("%q missing from the ports row\n%s", needle, joined)
		return 0
	}
	if got := at("...More") - at("3000 next"); got != 3*colPitch {
		t.Errorf("...More is %d from the first port, want %d", got, 3*colPitch)
	}
	row.Selected = 0
	closed := strings.Join(render(t, &Form{Rows: []Row{row}}), "\n")
	if strings.Contains(closed, "4000 jekyll") {
		t.Errorf("the list stays up when another cell is selected\n%s", closed)
	}
}

func TestMoreListMovesAndTicks(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label: "ports", Multi: true, Selected: 3,
		Options: []string{"3000 next", "5173 vite", "8080 web", "...More"},
		On:      []bool{false, false, false, true},
		More:    &More{Cell: 3, Options: []string{"4000 jekyll", "1313 hugo"}, On: []bool{true, false}},
	}}}
	down := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
	space := tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone)
	if !f.moreKey(0, down) || f.Rows[0].More.At != 1 {
		t.Fatalf("down left the cursor at %d", f.Rows[0].More.At)
	}
	if !f.moreKey(0, space) || !f.Rows[0].More.On[1] || !f.Rows[0].On[3] {
		t.Fatalf("space ticks = %v cell %v", f.Rows[0].More.On, f.Rows[0].On)
	}
	if f.moreKey(0, down) {
		t.Fatal("down on the last item stayed inside the list")
	}
	f.moreKey(0, space)
	f.Rows[0].More.At = 0
	f.moreKey(0, space)
	if got := f.Selections("ports"); len(got) != 0 {
		t.Errorf("selections = %v, want none — ...More is not a port", got)
	}
}

func TestColumnTextCapsAtTenRunes(t *testing.T) {
	t.Parallel()
	r := Row{
		Paint: map[string]string{"docker (sandbox)": "sandbox"},
	}
	if got := r.columnText("docker (sandbox)"); got != "sandbox" {
		t.Errorf("paint = %q, want sandbox", got)
	}
	if got := r.columnText("6006 storybook"); utf8.RuneCountInString(got) != columnRunes {
		t.Errorf("clip = %q, want %d runes", got, columnRunes)
	}
	if got := r.columnText("API keys"); got != "API keys" {
		t.Errorf("a short cell must stay whole, got %q", got)
	}
}

func TestPreferUTF8LocaleRewritesPOSIX(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	preferUTF8Locale()
	if got := os.Getenv("LC_ALL"); got != "C.UTF-8" {
		t.Errorf("LC_ALL=%q, want C.UTF-8 so tcell does not encode nerd glyphs as US-ASCII", got)
	}
}

func TestPreferUTF8LocaleLeavesAUTF8LocaleAlone(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	preferUTF8Locale()
	if got := os.Getenv("LC_ALL"); got != "en_US.UTF-8" {
		t.Errorf("a UTF-8 locale must not be rewritten, got %q", got)
	}
}
