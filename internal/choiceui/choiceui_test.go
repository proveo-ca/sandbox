// SPEC: _spec/internal/choiceui/choice-prompt-render.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

func render(t *testing.T, f *Form) []string {
	t.Helper()
	s := simScreen(t, 120, 40)
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
	s := simScreen(t, 120, 40)
	f.draw(s, f.firstSelectable(), 0)

	brand := hexName(ui.ColorBrand)
	const ink = "" // the terminal's own foreground
	box := strings.Split(ui.BrandBanner, "\n")
	for x := 0; x < 80; x++ {
		if r, _, _, _ := s.GetContent(x, 0); r != ' ' && r != 0 {
			t.Fatalf("row 0 col %d is %q, help opens with a blank line", x, string(r))
		}
	}
	for i, line := range box {
		assertBannerLine(t, s, i+1, line, brand, true)
	}
	assertBannerLine(t, s, len(box)+1, "  "+ui.BrandTagline, ink, false)
	for x := 0; x < 80; x++ {
		if r, _, _, _ := s.GetContent(x, len(box)+2); r != ' ' && r != 0 {
			t.Fatalf("row %d col %d is %q, help closes the header with a blank line", len(box)+2, x, string(r))
		}
	}
}

func assertBannerLine(t *testing.T, s *screen, y int, line string, fg string, bold bool) {
	t.Helper()
	x := 0
	for _, r := range line {
		got, _, style, _ := s.GetContent(x, y)
		if got != r {
			t.Errorf("row %d col %d = %q, want %q", y, x, string(got), string(r))
		}
		if r == ' ' {
			x++
			continue
		}
		if gfg := fgHex(style); gfg != fg {
			t.Errorf("row %d col %d color %v, want %v", y, x, gfg, fg)
		}
		if gotBold := style.GetBold(); gotBold != bold {
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
		t.Errorf("theme settings must never show their value:\n%s", all)
	}
	if strings.Contains(all, "CODE_THEME") || strings.Contains(all, "DARK_MODE") {
		t.Errorf("the header names no theme setting:\n%s", all)
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

// No form draws a riskier → safer legend; the columns keep their order without one.
func TestNoFormDrawsARiskLegend(t *testing.T) {
	t.Parallel()
	for _, f := range []*Form{
		{Rows: []Row{
			{Label: "egress", Options: []string{"open", "allowlist", "review"}, Selected: 1},
			{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1},
		}},
		budgetForm(),
		tallForm(),
	} {
		for _, size := range [][2]int{{120, 40}, {80, 24}} {
			out := strings.Join(renderAt(t, f, 0, size[0], size[1]), "\n")
			for _, legend := range []string{"riskier", "safer", "◀", "▶"} {
				if strings.Contains(out, legend) {
					t.Errorf("%dx%d draws %q:\n%s", size[0], size[1], legend, out)
				}
			}
		}
	}
}

// A section opens on a stub off the left wall: the name at column 4, the stub 16
// columns wide, longer when the name needs it, and never repeated on its row.
func TestASectionOpensOnAStubThatGrowsForALongName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		row  Row
		stub string
	}{
		{"short heading", Row{Label: "OS", Heading: "reach", Options: []string{"host"}, Multi: true, Divider: true}, "├── reach ──────"},
		{"nine runes", Row{Label: "OS", Heading: "execution", Options: []string{"host"}, Multi: true, Divider: true}, "├── execution ──"},
		{"long heading", Row{Label: "OS", Heading: "network baseline", Options: []string{"host"}, Multi: true, Divider: true}, "├── network baseline ──"},
		{"label as heading", Row{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, Divider: true}, "├── add-ons ────"},
	} {
		f := &Form{Rows: []Row{{Label: "egress", Options: []string{"open", "allowlist"}}, c.row}}
		lines := render(t, f)
		stub := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "├") && !strings.HasSuffix(l, "┤") })
		if stub < 0 || stub+1 >= len(lines) || !strings.Contains(lines[stub+1], c.row.Options[0]) {
			t.Fatalf("%s: no stub right above its row:\n%s", c.name, strings.Join(lines, "\n"))
		}
		row := stub + 1
		got := []rune(lines[stub])
		if want := []rune(c.stub); len(got) <= len(want) || string(got[:len(want)]) != c.stub || got[len(want)] != ' ' {
			t.Errorf("%s: stub reads %q, want %q and a blank after it", c.name, lines[stub], c.stub)
		}
		if c.row.Heading == "" && strings.Contains(lines[row], c.row.Label) {
			t.Errorf("%s: the stub names the section, yet the row repeats it: %q", c.name, lines[row])
		}
		if c.row.Heading != "" && !strings.Contains(lines[row], c.row.Label) {
			t.Errorf("%s: a row under a declared heading keeps its own label: %q", c.name, lines[row])
		}
	}
}

func TestSupportingTextUsesTerminalInkNotSlate(t *testing.T) {
	t.Parallel()
	f := &Form{
		Title:  "run cursor",
		Header: []string{"git:      sandbox on main", "keys:     CURSOR_API_KEY"},
		Rows:   []Row{{Label: "add-ons", Options: []string{"browser", "sandbox"}, Multi: true, On: []bool{true, false}}},
	}
	s := simScreen(t, 120, 40)
	f.draw(s, 0, 0)

	cells, w, h := s.GetContents()
	slate := hexName(ui.ColorCloud)
	light := hexName(ui.ColorSecondary)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch fgHex(cells[y*w+x].Style) {
			case light:
				t.Errorf("row %d col %d forces the light #E5E4E4, which vanishes on a white terminal", y, x)
			case slate:
				t.Errorf("row %d col %d uses ColorCloud slate, too dark for text on a black background", y, x)
			}
		}
	}
	lines := screenLines(s)
	for _, text := range []string{"sandbox on main", "CURSOR_API_KEY"} {
		for y, l := range lines {
			if i := strings.Index(l, text); i >= 0 {
				if _, _, st, _ := s.GetContent(textWidth(l[:i]), y); fgHex(st) != "" {
					t.Errorf("header text %q is %s, want the terminal's own ink", text, fgHex(st))
				}
			}
		}
	}
}

// Each answer draws its state; the cursor's answer is a reversed fill, and nothing is underlined.
func TestAnAnswerDrawsItsStateAndTheCursorFillsIt(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"open", "allowlist"}, Selected: 1},
		{Label: "model", Options: []string{"cloud", "local"}, Locked: true},
		{Label: "add-ons", Multi: true, Options: []string{"browser", "devices"}, On: []bool{false, false}, Off: []bool{false, true}},
	}}
	s := simScreen(t, 120, 40)
	f.draw(s, 0, 0)
	lines := screenLines(s)
	at := func(text string) lipgloss.Style {
		t.Helper()
		for y, l := range lines {
			if i := strings.Index(l, text); i >= 0 {
				_, _, st, _ := s.GetContent(textWidth(l[:i]), y)
				return st
			}
		}
		t.Fatalf("%q is not on the frame:\n%s", text, strings.Join(lines, "\n"))
		return lipgloss.Style{}
	}
	p := styles()
	for _, c := range []struct {
		text string
		want lipgloss.Style
		name string
	}{
		{"(•) allowlist", p.focus.Bold(true), "the cursor's chosen answer: filled and bold"},
		{"( ) open", p.offered, "an answer not chosen: grey"},
		{"(•) cloud", p.fixed, "a locked row's answer in force: plain"},
		{"( ) local", p.gated, "a locked row's other answer: faint, struck through"},
		{"[ ] browser", p.offered, "a box not ticked: grey"},
		{"[ ] devices", p.gated, "a box gated off: faint, struck through"},
		{"model", p.lockedLabel, "a locked row's label: faint"},
	} {
		if got := at(c.text); !sameStyle(got, c.want) {
			t.Errorf("%s: %q drawn %q", c.name, c.text, got.Render("x"))
		}
	}
	if !p.focus.GetReverse() || fgHex(p.focus) != hexName(ui.ColorBrand) || !strings.Contains(p.focus.Render("x"), "48;2;0;0;0") {
		t.Errorf("the cursor fill must be black on brand, reversed so it survives NO_COLOR: %q", p.focus.Render("x"))
	}
	if !p.gated.GetFaint() || !p.gated.GetStrikethrough() {
		t.Errorf("an unavailable answer must be faint and struck through: %q", p.gated.Render("x"))
	}
	cells, w, h := s.GetContents()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if cells[y*w+x].Style.GetUnderline() {
				t.Errorf("row %d col %d is underlined; the cursor is a fill", y, x)
			}
		}
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
	pal := palette{accent: lipgloss.NewStyle().Foreground(lipgloss.Color("#008080")), body: lipgloss.NewStyle()}
	put := func(_ int, st lipgloss.Style, s string) {
		if sameStyle(st, pal.accent) {
			accented = append(accented, s)
		} else {
			body = append(body, s)
		}
	}
	putHeader(put, pal, factCol(cardMax), "llms:     main=claude-opus-5 (anthropic)  small=claude-haiku-4-5 (anthropic)")

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

// A glyph row is "label:  <glyph> name  <glyph> name". The label and the glyphs are
// markers and take the accent; the names are content and stay body-styled.
func TestPutHeaderAccentsGlyphsNotNames(t *testing.T) {
	t.Parallel()
	var accented, body []string
	pal := palette{accent: lipgloss.NewStyle().Foreground(lipgloss.Color("#008080")), body: lipgloss.NewStyle()}
	put := func(_ int, st lipgloss.Style, s string) {
		if sameStyle(st, pal.accent) {
			accented = append(accented, s)
		} else {
			body = append(body, s)
		}
	}
	putHeader(put, pal, factCol(cardMax), "tooling:   typescript  {} yaml")

	acc := strings.Join(accented, "|")
	for _, want := range []string{"tooling:", "", "{}"} {
		if !strings.Contains(acc, want) {
			t.Errorf("%q must be accented; accented=%q", want, acc)
		}
	}
	joined := strings.Join(body, "")
	for _, want := range []string{"typescript", "yaml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q must render as body text; body=%q", want, joined)
		}
	}
	if strings.Contains(acc, "typescript") || strings.Contains(acc, "yaml") {
		t.Errorf("names must not be accented; accented=%q", acc)
	}
}

// The git row is "git: <repo> on <branch> (<state>)". A reader scans it for the
// branch, and the state is a description of the fact rather than the fact itself.
func TestPutHeaderAccentsBranchAndItalicisesAside(t *testing.T) {
	t.Parallel()
	var accented, aside, body []string
	pal := palette{
		accent: lipgloss.NewStyle().Foreground(lipgloss.Color("#008080")),
		aside:  lipgloss.NewStyle().Italic(true),
		body:   lipgloss.NewStyle(),
	}
	put := func(_ int, st lipgloss.Style, s string) {
		switch {
		case sameStyle(st, pal.accent):
			accented = append(accented, s)
		case sameStyle(st, pal.aside):
			aside = append(aside, s)
		default:
			body = append(body, s)
		}
	}
	putHeader(put, pal, factCol(cardMax), "git:      monorepo on rs-295-integration-discriminator (uncommitted changes)")

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
		Label: "add-ons", Options: []string{"ios"}, Multi: true,
		On: []bool{false}, Off: []bool{true},
		Help:   map[string]string{"ios": "an iOS simulator on this host"},
		OffWhy: map[string]string{"ios": "the iOS simulator path is not built yet"},
	}}}
	got := joined(t, f)
	for _, want := range []string{
		"› ios — an iOS simulator on this host",
		"off: the iOS simulator path is not built yet",
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

func TestRowNamesOccupySixteenColumns(t *testing.T) {
	t.Parallel()
	if labelWidth != 16 {
		t.Fatalf("labelWidth = %d, want 16", labelWidth)
	}
	f := &Form{Rows: []Row{
		{Label: "credentials", Options: []string{"forward", "broker"}, Selected: 1},
		{Label: "credentials-and-then-some", Options: []string{"forward"}, Selected: 0},
	}}
	lines := render(t, f)
	line := func(n int) string {
		t.Helper()
		var seen int
		for _, l := range lines {
			if strings.Contains(l, "forward") {
				if seen == n {
					return l
				}
				seen++
			}
		}
		t.Fatalf("forward line %d missing:\n%s", n, strings.Join(lines, "\n"))
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
	creds := line(0)
	if got := at(creds, "( ) "); got != optOrigin {
		t.Errorf("first option at %d, want the name column to end at %d:\n%s", got, optOrigin, creds)
	}
	name := string([]rune(creds)[:optOrigin])
	if !strings.Contains(name, "credentials") {
		t.Errorf("the name left its column:\n%s", creds)
	}
	long := line(1)
	if got := at(long, "(•) "); got != optOrigin {
		t.Errorf("a long name reached the option, glyph at %d:\n%s", got, long)
	}
	if !strings.HasSuffix(string([]rune(long)[:optOrigin]), "…") {
		t.Errorf("a name past the column must clip:\n%s", long)
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
}

// A narrower card closes the grid up to the 16-column pitch, and the fourth column ends inside the wall.
func TestAnEightyColumnCardDrawsTheGridAtPitchSixteen(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{
		{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}, Cols: []int{0, 2, 3}, Locked: true},
		{Label: "model", Options: []string{"Qwen 3.8", "API keys", "plan", "Glimmer"}, Cols: []int{0, 1, 2, 3}, Selected: 1},
	}}
	if got := pitch(cardWidth(80), f.lastCol()); got != minPitch {
		t.Fatalf("pitch at 80 columns = %d, want %d", got, minPitch)
	}
	if got := pitch(cardWidth(120), f.lastCol()); got != colPitch {
		t.Fatalf("pitch at 120 columns = %d, want %d", got, colPitch)
	}
	lines := renderAt(t, f, 1, 80, 24)
	col := func(needle string) (string, int) {
		t.Helper()
		for _, l := range lines {
			if i := strings.Index(l, needle); i >= 0 {
				return l, utf8.RuneCountInString(l[:i])
			}
		}
		t.Fatalf("no line holds %q:\n%s", needle, strings.Join(lines, "\n"))
		return "", 0
	}
	for _, c := range []struct {
		cell string
		at   int
	}{
		{"( ) Qwen 3.8", optOrigin},
		{"(•) API keys", optOrigin + minPitch},
		{"( ) plan", optOrigin + 2*minPitch},
		{"( ) Glimmer", optOrigin + 3*minPitch},
		{"( ) balanced", optOrigin + 2*minPitch},
		{"( ) deny-all", optOrigin + 3*minPitch},
	} {
		line, at := col(c.cell)
		if at != c.at {
			t.Errorf("%q at column %d, want %d", c.cell, at, c.at)
		}
		if end := at + utf8.RuneCountInString(c.cell); end > cardWidth(80)-2 {
			t.Errorf("%q ends at column %d, past the card's inner edge %d: %q", c.cell, end, cardWidth(80)-2, line)
		}
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
	under, bottom := -1, -1
	for y, l := range lines {
		if strings.HasPrefix(l, "╰") {
			bottom = y
		}
		if i := strings.Index(l, "[x] 4000 jekyll"); i >= 0 {
			under = y
			if got, want := utf8.RuneCountInString(l[:i]), at("[x] ...More"); got != want {
				t.Errorf("the list's entries start at %d, want the cell's column %d\n%s", got, want, joined)
			}
		}
	}
	if bottom < 0 || under <= bottom {
		t.Errorf("a one-row card has no room inside, so the list opens under the card's bottom border (row %d), got row %d\n%s", bottom, under, joined)
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
	down := named("down")
	space := key(' ')
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

func TestARadioSetDrawsRadiosBesideCheckboxes(t *testing.T) {
	t.Parallel()
	s := simScreen(t, 110, 30)
	f := &Form{Title: "run hermes", Rows: []Row{{Label: "devices", Options: []string{"tui", "desktop", "chrome"}, Multi: true,
		RadioSet: []string{"tui", "desktop"}, On: []bool{true, false, false}, Cols: []int{0, 1, 2}}}}
	f.draw(s, 0, 0)
	s.Show()
	got := rendered(t, s)
	for _, want := range []string{"(•) tui", "( ) desktop", "[ ] chrome"} {
		if !strings.Contains(got, want) {
			t.Errorf("devices row lacks %q:\n%s", want, got)
		}
	}
}

func TestAdoptCarriesAnswersToRowsThatOfferTheSameOptions(t *testing.T) {
	t.Parallel()
	rows := func() []Row {
		return []Row{
			{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}},
			{Label: "interface", Options: []string{"tui", "browser"}, Multi: true, On: []bool{true, false}},
			{Label: "auth", Options: []string{"usage", "subscription"}},
			{Label: "mcp", Options: []string{"on", "off"}, Locked: true},
			{Label: "key", Field: true},
			{Label: "ports", Options: []string{"forward"}, Multi: true, On: []bool{false},
				More: &More{Options: []string{"3000", "5173"}, On: []bool{true, true}}},
		}
	}
	prev := &Form{Rows: rows()}
	prev.Rows[0].Selected = 2
	prev.Rows[1].On = []bool{false, true}
	prev.Rows[2].Selected = 1
	prev.Rows[3].Selected = 1
	prev.Rows[4].Value = "typed"
	prev.Rows[5].More.On = []bool{false, true}

	next := &Form{Rows: rows()}
	next.Rows[2].Off = []bool{false, true} // subscription is greyed out now
	next.Rows = append(next.Rows, Row{Label: "new", Options: []string{"a", "b"}})
	next.Rows[0].Options = []string{"allow-all", "deny-all"} // the row offers something else now
	next.Adopt(prev)

	if next.Rows[0].Selected != 0 {
		t.Errorf("a row whose options changed took the old answer: %d", next.Rows[0].Selected)
	}
	if on := next.Rows[1].On; on[0] || !on[1] {
		t.Errorf("the checkbox answers were not carried: %v", on)
	}
	if next.Rows[2].Selected != 0 {
		t.Errorf("an answer the new form greys out was carried: %d", next.Rows[2].Selected)
	}
	if next.Rows[3].Selected != 0 {
		t.Errorf("a locked row took the old answer: %d", next.Rows[3].Selected)
	}
	if next.Rows[4].Value != "typed" {
		t.Errorf("the field's text was not carried: %q", next.Rows[4].Value)
	}
	if on := next.Rows[5].More.On; on[0] || !on[1] {
		t.Errorf("the list's ticks were not carried: %v", on)
	}
	next.Adopt(nil)
	(&Form{}).Adopt(prev)
}
