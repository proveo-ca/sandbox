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
	// greyed box. SPEC: _spec/internal/choiceui/choice-prompt-render.puml
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
		t.Errorf("a row that declared no divider must not be given one:\n%s", out)
	}
	if strings.Count(out, "─ add-ons ─") != 1 {
		t.Errorf("the row that declared a divider must get exactly one:\n%s", out)
	}

	// Two declared groups both get a heading — the case the old "first multi row"
	// rule could not express.
	two := &Form{Rows: []Row{
		{Label: "execution", Options: []string{"host", "docker (sandbox)"}, Multi: true, Divider: true, On: make([]bool, 2)},
		{Label: "interface", Options: []string{"tui (this session)", "browser"}, Multi: true, Divider: true, On: make([]bool, 2)},
	}}
	got := strings.Join(render(t, two), "\n")
	for _, want := range []string{"─ execution ─", "─ interface ─"} {
		if !strings.Contains(got, want) {
			t.Errorf("both declared groups must be named, missing %q:\n%s", want, got)
		}
	}
}

// The models row carries a label and several KEY=value pairs at once. Accenting
// stopped at the label before, so every slot after "llms:" rendered as body text
// and the row read as one model mattering more than the others.
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

// The picker could only ever explain what it had taken away: Reason is written
// when an option is gated off, so the add-ons that were available and ticked
// carried no text at all. Help describes the option the cursor is on.
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
