// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runWord is what Enter does when n rows of the page will run.
func runWord(n int) string {
	switch n {
	case 0:
		return "next"
	case 1:
		return "run 1 step"
	}
	return fmt.Sprintf("run %d steps", n)
}

// aheadRows are the ahead section from page from on: one row per page not reached, then sbx setup.
func aheadRows(from int) []Row {
	pages := []Row{
		{Kind: RowAhead, Mark: MarkAhead, Label: "git, shell", Outcome: "page 2/4 · git identity, GitHub, prompt kit"},
		{Kind: RowAhead, Mark: MarkAhead, Label: "credentials", Outcome: "page 3/4 · agent sign-ins and API keys sbx holds"},
		{Kind: RowAhead, Mark: MarkAhead, Label: "operator", Outcome: "page 4/4 · the name agents address you by"},
	}
	rows := append([]Row(nil), pages[from-2:]...)
	rows = append(rows, Row{Kind: RowStatus, Mark: MarkAhead, Label: "sbx setup", Outcome: "after init, while sbx's store lacks an env key"})
	rows[0].Divider, rows[0].Heading = true, "ahead"
	return rows
}

// doneRows are the done rows of pages 1 to n-1 as page n draws them.
func doneRows(n int) []Row {
	rows := []Row{
		{Kind: RowDone, Mark: MarkNeeds, Label: "sbx", Outcome: "kept 0.50.0 · Docker signed in · 2 checks need you"},
		{Kind: RowDone, Mark: MarkDone, Label: "git, shell",
			Outcome: "Ada Example <ada@example.invalid> · git's global config · GitHub later · prompt kit later",
			Brief:   "Ada Example <ada@example.invalid> · 2 later"},
		{Kind: RowDone, Mark: MarkDone, Label: "credentials", Outcome: "3 held · 4 not held"},
	}
	return append([]Row(nil), rows[:n-1]...)
}

// pageOneChecks are page 1's checks section on the recorded host: two passed, two that need you.
func pageOneChecks() []Row {
	return []Row{
		{Kind: RowCheck, Mark: MarkDone, Label: "Apple silicon", Outcome: "macOS Sonoma 14 or later", Divider: true, Heading: "checks"},
		{Kind: RowCheck, Mark: MarkDone, Label: "virtualisation", Outcome: "supported"},
		{Kind: RowCheck, Mark: MarkNeeds, Label: "Binary version", Outcome: "update available: v9.9.9",
			Fix: "Download the latest release: https://example.invalid/sbx/v9.9.9"},
		{Kind: RowCheck, Mark: MarkNeeds, Label: "SSH client config",
			Outcome:  "an unmanaged sandbox Host stanza can shadow the managed configuration",
			Evidence: []string{"/home/op/.ssh/config:5: Host"},
			Fix:      "Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh",
			Note:     "a run does not need this check; it affects IDE attach and Hermes Desktop, which connect with `ssh <name>.sbx`"},
	}
}

const (
	notOffered = "distro package: not offered — proveo cannot pin a package-manager install"
	keepAsIs   = "keep what is installed"
)

// installRows are page 1's sbx section: sbx install with location and PATH opening under the
// tarball, then Docker login.
func installRows(installed bool, picked string) []Row {
	opts := []string{"rootless tarball", "distro package"}
	help := map[string]string{
		"rootless tarball": "DockerSandboxes-darwin.tar.gz · no digest published",
		"distro package":   "a package manager installs sbx",
	}
	if installed {
		opts = append(opts, keepAsIs)
		help[keepAsIs] = "sbx 0.50.0 stays; Enter signs in to Docker, then checks again"
	}
	login := Row{Label: "Docker login", Options: []string{"now", "later"}, Cols: []int{0, 1}, CellRunes: 20,
		Help: map[string]string{"now": "sbx login signs in while sbx diagnose says Docker is signed out", "later": "the first sbx run signs in"}}
	if !installed {
		login.Paint = map[string]string{"now": "if signed out"}
	}
	under := &When{Row: "sbx install", Option: "rootless tarball"}
	return []Row{
		{Label: "sbx install", Divider: true, Heading: "sbx", Options: opts, Selected: slices.Index(opts, picked),
			Cols: []int{0, 1, 2}, CellRunes: 20, Paint: map[string]string{keepAsIs: "keep as is"},
			Off: []bool{false, true, false}, Reason: notOffered, Help: help},
		{Label: "location", Indent: 2, When: under, Options: []string{"/usr/local", "~/.docker/sbx"}, Selected: 1,
			Cols: []int{0, 1}, CellRunes: 20, Help: map[string]string{"/usr/local": "needs sudo", "~/.docker/sbx": "no sudo"}},
		{Label: "PATH", Indent: 2, When: under, Options: []string{"add to shell rc", "print only"}, Cols: []int{0, 1}, CellRunes: 20,
			Help: map[string]string{"add to shell rc": "~/.zshrc gains the bin directory", "print only": "init prints the line to add"}},
		login,
	}
}

// pageOneWord counts what Enter runs on page 1: sbx install and its PATH line, the checks again
// after an install, and Docker login while it reads now.
func pageOneWord(f *Form) string {
	n := 0
	if pick := f.Selection("sbx install"); pick != "" && pick != keepAsIs {
		n += 2
		if f.Selection("PATH") == "add to shell rc" {
			n++
		}
	}
	if f.Selection("Docker login") == "now" {
		n++
	}
	return runWord(n)
}

// pageOne is page 1 on the recorded host: sbx 0.50.0 kept, Docker signed out.
func pageOne(picked string) *Form {
	rows := append(pageOneChecks(), installRows(true, picked)...)
	return &Form{
		Border: "proveo init", Checklist: true, Step: Step{At: 1, Of: 4}, EnterWord: pageOneWord,
		Title:  "Prepare sbx on this darwin/arm64 host",
		Header: []string{"found: sbx 0.50.0", "network: listed hosts only"},
		Rows:   append(rows, aheadRows(2)...),
	}
}

// pageOneFresh is page 1 on a host with no sbx yet.
func pageOneFresh() *Form {
	rows := append(pageOneChecks()[:2], installRows(false, "rootless tarball")...)
	return &Form{
		Border: "proveo init", Checklist: true, Step: Step{At: 1, Of: 4}, EnterWord: pageOneWord,
		Title:  "Prepare sbx on this darwin/arm64 host",
		Header: []string{"found: no sbx on this host"},
		Rows:   append(rows, aheadRows(2)...),
	}
}

// pageOneRunning is page 1 after Enter: sbx install kept, Docker login about to yield.
func pageOneRunning() *Form {
	f := pageOne(keepAsIs)
	f.Rows = append(pageOneChecks(),
		Row{Kind: RowStatus, Mark: MarkLater, Label: "sbx install", Outcome: "kept 0.50.0", Divider: true, Heading: "sbx"},
		Row{Kind: RowStatus, Mark: MarkRunning, Label: "Docker login", Outcome: "sbx login · Docker not signed in"})
	f.Rows = append(f.Rows, aheadRows(2)...)
	f.Progress = &Progress{Plan: []string{"Docker login"},
		Help: "sbx login takes the terminal for Docker's browser sign-in; page 1/4 returns when it exits"}
	return f
}

// pageOneRerun is page 1 on a host where init already ran and Docker is signed in.
func pageOneRerun() *Form {
	f := pageOne(keepAsIs)
	f.Rows[4].Help[keepAsIs] = "sbx 0.50.0 stays; Enter opens page 2/4"
	f.Rows[7] = Row{Kind: RowStatus, Mark: MarkDone, Label: "Docker login", Outcome: "signed in"}
	return f
}

// pageTwo is page 2 with git identity's fields open.
func pageTwo() *Form {
	identity := &When{Row: "git identity", Option: "now"}
	why := "git's global config on this host gets user.name and user.email; each run passes both into its sandbox"
	rows := append(doneRows(2),
		Row{Label: "git identity", Divider: true, Heading: "git", Options: []string{"now", "later"}, Cols: []int{0, 1}, CellRunes: 20,
			Help: map[string]string{"now": "Enter writes both fields to git's global config", "later": "the git identity waits"}},
		Row{Label: "user.name", Indent: 2, When: identity, Field: true, Value: "Ada Example", Reason: why},
		Row{Label: "user.email", Indent: 2, When: identity, Field: true, Value: "ada@example.invalid", Reason: why},
		Row{Label: "GitHub", Options: []string{"now", "later"}, Selected: 1, Cols: []int{0, 1}, CellRunes: 20,
			Help: map[string]string{"now": "gh signs in, then sandbox Git opens", "later": "GitHub waits"}},
		Row{Label: "prompt kit", Divider: true, Heading: "shell", Options: []string{"now", "later"}, Selected: 1, Cols: []int{0, 1}, CellRunes: 20,
			Help: map[string]string{"now": "starship, a Nerd Font and the proveo segment install", "later": "the prompt kit (starship, a Nerd Font, the proveo segment) waits"}},
	)
	return &Form{
		Border: "proveo init", Checklist: true, Step: Step{At: 2, Of: 4},
		EnterWord: func(f *Form) string {
			n := 0
			for _, label := range []string{"git identity", "GitHub", "prompt kit"} {
				if f.Selection(label) == "now" {
					n++
				}
			}
			return runWord(n)
		},
		Title: "Set up git and the shell",
		Rows:  append(rows, aheadRows(3)...),
	}
}

// pageTwoRerun is page 2 on a host where git identity and GitHub already hold.
func pageTwoRerun() *Form {
	f := pageTwo()
	f.Rows[0] = Row{Kind: RowDone, Mark: MarkDone, Label: "sbx", Outcome: "kept 0.50.0 · Docker signed in"}
	f.Rows[1] = Row{Kind: RowStatus, Mark: MarkDone, Label: "git identity", Outcome: "Ada Example <ada@example.invalid>", Divider: true, Heading: "git"}
	f.Rows[4] = Row{Kind: RowStatus, Mark: MarkDone, Label: "GitHub", Outcome: "signed in as ada · sandbox Git: SSH agent"}
	return f
}

// pageThree is page 3 with one key that only this host's env holds.
func pageThree() *Form {
	signIn := Row{Options: []string{"not signed in", "sign in"}, Cols: []int{0, 1}, CellRunes: 20,
		Help: map[string]string{"sign in": "sbx runs the agent's own login once"}}
	claude, cursor := signIn, signIn
	claude.Label, claude.Divider, claude.Heading = "claudecode", true, "subscriptions"
	claude.Help = map[string]string{"not signed in": "leave it; claudecode shows its own login at its next run, and sbx keeps what it signs in",
		"sign in": "sbx runs claudecode's login once"}
	cursor.Label = "cursor"
	rows := append(doneRows(3),
		claude,
		Row{Label: "codex", Options: []string{"signed in", "sign in again", "sign out"}, Cols: []int{0, 1, 2}, CellRunes: 20,
			Help: map[string]string{"signed in": "sbx holds codex's login as openai"}},
		cursor,
		Row{Label: "opencode", Field: true, Masked: true, Held: true, Placeholder: "stored — type to replace", Reason: "sbx holds a token as opencode"},
		Row{Label: "anthropic", Divider: true, Heading: "API keys", Field: true, Masked: true, Warn: true,
			Placeholder: "this host's env only — Enter to import", Reason: "ANTHROPIC_API_KEY is set here; Enter stores it in sbx"},
		Row{Label: "openai", Field: true, Masked: true, Placeholder: "empty — type a key", Reason: "a key typed here goes to sbx"},
		Row{Label: "cursor", Field: true, Masked: true, Placeholder: "empty — type a key", Reason: "a key typed here goes to sbx"},
	)
	return &Form{
		Border: "proveo init", Checklist: true,
		EnterWord: func(*Form) string { return runWord(1) },
		Title:     "Store agent credentials",
		Header:    []string{"Secrets live in sbx's host-wide store; agents never hold them."},
		Rows:      append(rows, aheadRows(4)...),
		Step:      Step{At: 3, Of: 4, Leave: Leave{Title: "Quit init?", Body: "Answers on page 3/4 are not saved.\nSteps that ran stay done."}},
	}
}

// pageFour is page 4 after the probe found two env keys sbx's store lacks.
func pageFour() *Form {
	rows := append(doneRows(4),
		Row{Label: "name", Divider: true, Heading: "operator", Field: true, Placeholder: "Executor",
			Reason: "empty keeps Executor; the first 16 characters are kept"},
		Row{Label: "sbx setup", Divider: true, Heading: "ahead", Options: []string{"after init", "later"}, Cols: []int{0, 1}, CellRunes: 20,
			Help: map[string]string{"after init": "sbx's own setup screen opens after the result: 2 env keys sbx's store lacks",
				"later": "sbx setup waits; the result names it"}},
	)
	return &Form{
		Border: "proveo init", Checklist: true, Step: Step{At: 4, Of: 4},
		EnterWord: func(*Form) string { return "finish" },
		Title:     "Name the operator",
		Header:    []string{"Agents in every sandbox on this host address you by this name."},
		Rows:      rows,
	}
}

// checklistCase is one checklist moment drawn at its sizes; quit draws it under the quit dialog.
type checklistCase struct {
	name   string
	form   func() *Form
	cursor int
	quit   bool
	sizes  [][2]int
}

var momentSizes = [][2]int{{80, 24}, {120, 36}}

// checklistCases are the moments the spec draws, plus the body scrolling at its last rung.
func checklistCases() []checklistCase {
	return []checklistCase{
		{"p1-rest", func() *Form { return pageOne(keepAsIs) }, 4, false, momentSizes},
		{"p1-fresh", pageOneFresh, 2, false, momentSizes},
		{"p1-tarball", func() *Form { return pageOne("rootless tarball") }, 4, false, momentSizes},
		{"p1-running", pageOneRunning, -1, false, momentSizes},
		{"p1-rerun", pageOneRerun, 4, false, momentSizes},
		{"p2", pageTwo, 3, false, momentSizes},
		{"p2-rerun", pageTwoRerun, 5, false, momentSizes},
		{"p3", pageThree, 2, false, momentSizes},
		{"p4", pageFour, 3, false, momentSizes},
		{"quit", pageThree, 2, true, momentSizes},
		{"tight", func() *Form { return pageOne("rootless tarball") }, 4, false, [][2]int{{80, 18}}},
		{"ascii", func() *Form { return withGlyphs(pageOneRunning(), GlyphsASCII) }, -1, false, [][2]int{{80, 24}}},
	}
}

// checklistFrame draws the case at w by h as the terminal shows it.
func checklistFrame(t *testing.T, c checklistCase, w, h int) []string {
	t.Helper()
	f := c.form()
	if !c.quit {
		return frameLines(t, f, c.cursor, w, h, 0)
	}
	m, _ := formModel{f: f, cursor: c.cursor, tick: 0}.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m, _ = m.Update(teaCtrlC)
	lines := strings.Split(plain(m.View().Content), "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// TestAChecklistGoldenFrames holds the checklist card to frames recorded from the renderer, one per
// moment the spec draws at 80x24 and 120x36. PROVEO_UPDATE_FRAMES=1 records them.
func TestAChecklistGoldenFrames(t *testing.T) {
	dir := filepath.Join("testdata", "checklist")
	update := os.Getenv("PROVEO_UPDATE_FRAMES") != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, c := range checklistCases() {
		for _, size := range c.sizes {
			name := fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1])
			seen[name] = true
			got := frameText(checklistFrame(t, c, size[0], size[1]))
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

// col is the column of the first rune of want in line, or -1.
func col(line, want string) int {
	at := strings.Index(line, want)
	if at < 0 {
		return -1
	}
	return len([]rune(line[:at]))
}

// lineWith is the first line holding want, or "".
func lineWith(lines []string, want string) string {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return l
		}
	}
	return ""
}

// The cursor stops only on answer rows that draw: status, check, done and ahead rows, and a row
// whose answer has not opened it, are never under it.
func TestAStatusRowIsNeverACursorStop(t *testing.T) {
	t.Parallel()
	for _, c := range checklistCases() {
		f := c.form()
		stops := map[int]bool{}
		for at, i := f.firstSelectable(), 0; i < 3*len(f.Rows); i++ {
			if at < len(f.Rows) {
				stops[at] = true
			}
			at = f.move(at, +1)
		}
		for i := range f.Rows {
			if stops[i] && (f.Rows[i].Kind != RowAnswer || !f.shown(i)) {
				t.Errorf("%s: the cursor stops on row %d %q, a %v row shown %v", c.name, i, f.Rows[i].Label, f.Rows[i].Kind, f.shown(i))
			}
		}
	}
	f := pageOne(keepAsIs)
	if got := f.firstSelectable(); got != 4 {
		t.Errorf("page 1 opens on row %d, want sbx install (4)", got)
	}
	if got := f.move(4, +1); got != 7 {
		t.Errorf("down from sbx install lands on row %d, want Docker login (7): location and PATH are closed", got)
	}
	if got := f.move(4, -1); got != 4 {
		t.Errorf("up from sbx install lands on row %d; the checks above are no stops", got)
	}
	if got := f.lastSelectable(); got != 7 {
		t.Errorf("end lands on row %d, want Docker login (7); the ahead rows are no stops", got)
	}
	running := pageOneRunning()
	if got := running.firstSelectable(); got != len(running.Rows) {
		t.Errorf("a page of status rows opens the cursor on row %d, want none (%d)", got, len(running.Rows))
	}
	for _, l := range frameLines(t, running, running.startCursor(), 80, 24, 0) {
		if r := []rune(l); len(r) > 2 && r[2] == '›' {
			t.Errorf("a page with no stop draws a cursor:\n%s", l)
		}
	}
}

// A row with When draws only while its parent reads that answer; while closed it is no stop, and
// its value survives closing and reopening.
func TestARowOpensItsFieldsOnlyWhileItsAnswerHolds(t *testing.T) {
	t.Parallel()
	f := pageTwo()
	open := frameText(frameLines(t, f, 1, 80, 24, 0))
	for _, want := range []string{"user.name", "user.email", "[Ada Example"} {
		if !strings.Contains(open, want) {
			t.Fatalf("git identity = now must open %q:\n%s", want, open)
		}
	}
	f.cycle(1, +1)
	closed := frameText(frameLines(t, f, 1, 80, 24, 0))
	if strings.Contains(closed, "user.name") || strings.Contains(closed, "user.email") {
		t.Errorf("git identity = later must close its fields:\n%s", closed)
	}
	if got := f.move(1, +1); got != 4 {
		t.Errorf("down from git identity lands on row %d, want GitHub (4) past the closed fields", got)
	}
	if got := f.FieldValue("user.email"); got != "ada@example.invalid" {
		t.Errorf("a closed field reads %q, want its value kept", got)
	}
	f.cycle(1, -1)
	if again := frameText(frameLines(t, f, 1, 80, 24, 0)); again != open {
		t.Errorf("reopening the fields must draw them as before:\n%s", again)
	}

	one := pageOne(keepAsIs)
	if strings.Contains(frameText(frameLines(t, one, 4, 80, 24, 0)), "location") {
		t.Error("keep as is must leave location and PATH closed")
	}
	one.cycle(4, +1)
	tarball := frameLines(t, one, 4, 120, 36, 0)
	for _, want := range []string{"location", "PATH"} {
		if c := col(lineWith(tarball, want+" "), want); c != 6 {
			t.Errorf("rootless tarball opens %q at column %d, want 6:\n%s", want, c, strings.Join(tarball, "\n"))
		}
	}
	if got := one.Selection("location"); got != "~/.docker/sbx" {
		t.Errorf("location reads %q; the answer under a sub-row is the row's own", got)
	}
}

// The top border carries the label at column 4, and every row of the checklist card ends on a wall
// at both edges at every width a frame draws.
func TestTheTopBorderCarriesItsLabel(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	left := g.wallL + g.frameTL + g.frameBL + g.joinL
	right := g.wallR + g.frameTR + g.frameBR + g.joinR + "▲▼░"
	for w := cardMin; w <= 160; w++ {
		for _, h := range []int{18, 24, 40} {
			f := pageOne("rootless tarball")
			lines := frameLines(t, f, 4, w, h, 0)
			cw := cardWidth(w)
			top := []rune(lines[0])
			if len(top) != cw || string(top[:4]) != g.frameTL+g.rule+g.rule+" " || string(top[cw-3:]) != g.rule+g.rule+g.frameTR {
				t.Fatalf("%dx%d: the top border is not \"╭── <label> ──…╮\": %q", w, h, lines[0])
			}
			if label := strings.TrimSpace(strings.TrimRight(string(top[4:]), g.rule+g.frameTR)); !strings.HasPrefix("proveo init", strings.TrimSuffix(label, "…")) {
				t.Errorf("%dx%d: the top border reads %q, want the label proveo init", w, h, label)
			}
			bottom := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, g.frameBL) })
			if bottom < 0 {
				t.Fatalf("%dx%d: no bottom border:\n%s", w, h, strings.Join(lines, "\n"))
			}
			for y := 0; y <= bottom; y++ {
				r := []rune(lines[y])
				if len(r) != cw || !strings.ContainsRune(left, r[0]) || !strings.ContainsRune(right, r[cw-1]) {
					t.Errorf("%dx%d row %d: not walled at 0 and %d: %q", w, h, y, cw-1, lines[y])
				}
			}
		}
	}
}

// ladderOrder is the cut as the rungs it took, in ladder order.
func ladderOrder(c cut) []int {
	b := func(on bool) int {
		if on {
			return 1
		}
		return 0
	}
	return []int{c.blanks, b(c.under), b(c.prose), b(c.passed), c.details, b(c.done), b(c.head), b(c.ahead)}
}

// The ladder takes its rungs in order, one at a time, and stops at the first fit: at 80x24 each
// moment takes exactly the rungs the spec lists, and as the terminal shrinks no rung is taken
// before every rung above it is spent. The help never drops, and the body scrolls only past the
// last rung.
func TestTheChecklistLadderDropsInOrder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		form *Form
		want cut
	}{
		{"p1-rest", pageOne(keepAsIs), cut{blanks: 3, under: true, passed: true, details: 2}},
		{"p1-fresh", pageOneFresh(), cut{blanks: 1}},
		{"p1-tarball", pageOne("rootless tarball"), cut{blanks: 3, under: true, passed: true, details: 2, head: true, ahead: true}},
		{"p1-running", pageOneRunning(), cut{blanks: 3, under: true, passed: true, details: 2}},
		{"p2", pageTwo(), cut{}},
		{"p2-rerun", pageTwoRerun(), cut{}},
		{"p3", pageThree(), cut{blanks: 3}},
		{"p4", pageFour(), cut{}},
	} {
		if got := c.form.layout(80, 24).cut; got != c.want {
			t.Errorf("%s at 80x24 took %+v, want %+v", c.name, got, c.want)
		}
		if got := c.form.layout(120, 36).cut; got != (cut{}) {
			t.Errorf("%s at 120x36 took %+v, want no rung", c.name, got)
		}
	}
	for _, mk := range []func() *Form{func() *Form { return pageOne("rootless tarball") }, pageThree, pageFour} {
		f := mk()
		full := f.checklistLines(80, cut{})
		blanks, help := countLines(full, lineBlank), f.maxHelpLines(helpWidth(80))
		var prev []int
		for h := 60; h >= 8; h-- {
			lay := f.layout(80, h)
			if lay.tooSmall {
				break
			}
			order := ladderOrder(lay.cut)
			if prev != nil && slices.Compare(order, prev) < 0 {
				t.Errorf("%s at 80x%d took %v after %v: a rung came back", f.Title, h, order, prev)
			}
			prev = order
			c := lay.cut
			entries := detailEntries(f.checklistLines(80, cut{blanks: blanks, under: true, prose: true, passed: true}))
			later := []bool{c.under || c.prose || c.passed || c.details > 0 || c.done || c.head || c.ahead,
				c.details > 0 || c.done || c.head || c.ahead, c.done || c.head || c.ahead}
			switch {
			case later[0] && c.blanks != blanks:
				t.Errorf("%s at 80x%d took a later rung with %d of %d blanks dropped", f.Title, h, c.blanks, blanks)
			case later[2] && c.details != entries:
				t.Errorf("%s at 80x%d folded with %d of %d detail entries dropped", f.Title, h, c.details, entries)
			}
			if lay.helpSlot != help {
				t.Errorf("%s at 80x%d holds %d help rows, want %d: the help never drops", f.Title, h, lay.helpSlot, help)
			}
			lines := len(f.checklistLines(80, c))
			if lay.body < lines && (!c.head || (f.countShown(RowAhead) > 1 && !c.ahead)) {
				t.Errorf("%s at 80x%d scrolls before the last rung: %+v", f.Title, h, c)
			}
			if rows := len(strings.Split(strings.TrimRight(frameText(frameLines(t, f, f.firstSelectable(), 80, h, 0)), "\n"), "\n")); rows > h {
				t.Errorf("%s at 80x%d draws %d rows", f.Title, h, rows)
			}
		}
	}
	tight := frameLines(t, pageOne("rootless tarball"), 4, 80, 18, 0)
	if !strings.ContainsAny(strings.Join(tight, ""), "▼░") || !strings.Contains(strings.Join(tight, "\n"), "pgup/pgdn scroll") {
		t.Errorf("past the last rung the body scrolls with marks on the wall and pgup on the key line:\n%s", strings.Join(tight, "\n"))
	}
}

// An outcome too long for its row draws its count form; one still too long cuts its first item.
func TestAnOutcomeTooLongCountsItsLaterSteps(t *testing.T) {
	t.Parallel()
	narrow := lineWith(frameLines(t, pageThree(), 2, 80, 24, 0), "git, shell")
	if !strings.Contains(narrow, "✓ git, shell    Ada Example <ada@example.invalid> · 2 later ") {
		t.Errorf("at 80 the done row must count its later steps: %q", narrow)
	}
	wide := lineWith(frameLines(t, pageThree(), 2, 120, 36, 0), "git, shell")
	if !strings.Contains(wide, "git's global config · GitHub later · prompt kit later") {
		t.Errorf("at 120 the done row must list every step: %q", wide)
	}
	ahead := lineWith(frameLines(t, pageThree(), 2, 60, 40, 0), "· operator")
	if !strings.Contains(ahead, "page 4/4 · the name") || !strings.HasSuffix(strings.TrimRight(ahead, " │"), "…") {
		t.Errorf("at 60 an ahead row keeps its page and cuts its end: %q", ahead)
	}
	for _, c := range []struct {
		outcome, brief string
		room           int
		want           string
	}{
		{"kept 0.50.0", "", 60, "kept 0.50.0"},
		{"Ada Example <ada@example.invalid> · git's global config · GitHub later", "Ada Example <ada@example.invalid> · 2 later", 60,
			"Ada Example <ada@example.invalid> · 2 later"},
		{"Ada Example <ada@example.invalid> · git's global config · GitHub later", "Ada Example <ada@example.invalid> · 2 later", 30,
			"Ada Example <ada@ex… · 2 later"},
		{"one very long outcome with no count form at all", "", 20, "one very long outco…"},
	} {
		got := fitOutcome(c.outcome, c.brief, c.room)
		if got != c.want || textWidth(got) > c.room {
			t.Errorf("fitOutcome(%q, %q, %d) = %q, want %q", c.outcome, c.brief, c.room, got, c.want)
		}
	}
}

// The key line says what Enter does in every state, and fits the card.
func TestTheChecklistKeyLineNamesWhatEnterDoes(t *testing.T) {
	t.Parallel()
	blocked := pageOne(keepAsIs)
	blocked.Blocked = "virtualisation is off; turn on Hypervisor.framework, then run proveo init again"
	stopping := pageOneRunning()
	stopping.Progress.Stopping = true
	for _, c := range []struct {
		name   string
		form   *Form
		cursor int
		w      int
		scroll bool
		want   string
	}{
		{"rest, page 1, an answer", pageOneFresh(), 2, 80, false, "↑↓ row · ←→ choose · enter run 4 steps · esc quit · ctrl+c quit"},
		{"rest, page 1, one step", pageOne(keepAsIs), 4, 80, false, "↑↓ row · ←→ choose · enter run 1 step · esc quit · ctrl+c quit"},
		{"rest, ASCII", withGlyphs(pageOneFresh(), GlyphsASCII), 2, 80, false, "up/down row · left/right choose · enter run 4 steps · esc quit · ctrl+c quit"},
		{"rest, a field", pageTwo(), 3, 80, false, "↑↓ row · type to edit · enter run 1 step · esc back · ctrl+c quit"},
		{"rest, nothing will run", pageTwoRerun(), 5, 80, false, "↑↓ row · ←→ choose · enter next · esc back · ctrl+c quit"},
		{"rest, page 4", pageFour(), 3, 80, false, "↑↓ row · type to edit · enter finish · esc back · ctrl+c quit"},
		{"the body scrolls, 80", pageOne("rootless tarball"), 4, 80, true, "↑↓ row · ←→ choose · pgup/pgdn scroll · enter run 4 steps · esc quit"},
		{"the body scrolls, 120", pageOne("rootless tarball"), 4, 120, true, "↑↓ row · ←→ choose · pgup/pgdn scroll · enter run 4 steps · esc quit · ctrl+c quit"},
		{"a blocking prereq", blocked, 4, 80, false, "esc quit · ctrl+c quit"},
		{"running", pageOneRunning(), -1, 80, false, "running 1 of 1 — Docker login · ctrl+c stop"},
		{"stopping", stopping, -1, 80, false, "stopping after Docker login …"},
	} {
		hidden := 0
		if c.scroll {
			hidden = 1
		}
		got := c.form.hint(c.w, hidden, c.cursor)
		if got != c.want {
			t.Errorf("%s: key line %q, want %q", c.name, got, c.want)
		}
		if textWidth(got) > keyRoom(c.w) || strings.Contains(got, "more below") || strings.Contains(got, "page ") {
			t.Errorf("%s: %q must fit %d columns and leave the count to the chip", c.name, got, keyRoom(c.w))
		}
	}
	if _, done := blocked.leave(press{key: "enter"}); done {
		t.Error("Enter on a blocked page must do nothing")
	}
	at := 4
	for _, k := range []press{{key: "right"}, {key: "down"}, {key: "tab"}, {text: "l"}, {text: " "}, {text: "/"}} {
		if _, done := blocked.key(&at, k, 80, 24); done || at != 4 || blocked.Selection("sbx install") != keepAsIs {
			t.Errorf("a blocked page took %+v: cursor %d, sbx install reads %q", k, at, blocked.Selection("sbx install"))
		}
	}
	if out, done := blocked.key(&at, press{key: "esc"}, 80, 24); !done || out != Quit {
		t.Errorf("Esc on a blocked page 1 gave %v, %v; want the quit question", out, done)
	}
	if got := blocked.checklistHelp(4, helpWidth(80)); len(got) == 0 || !strings.Contains(got[0].text, "virtualisation is off") {
		t.Errorf("the help on a blocked page names the blocker's fix: %v", got)
	}
	f := pageOne(keepAsIs)
	drawn := frameLines(t, f, 4, 80, 24, 0)
	if want := f.hint(80, 0, 4); lineWith(drawn, want) == "" {
		t.Errorf("the drawn key line differs from %q:\n%s", want, strings.Join(drawn, "\n"))
	}
	q := quitGate{}.open(80, 24, pageThree().Step.Leave, GlyphsNerd)
	if got := joinHint(q.ask.keyParts()); got != "←→ choose · enter press · esc stay · ctrl+c quit" {
		t.Errorf("the quit dialog's key line reads %q", got)
	}
}

// Each mark draws its tier's glyph in its colour role; the ASCII and Off tiers draw no Unicode
// mark, cursor or running frame inside the card.
func TestMarksDrawPerGlyphTier(t *testing.T) {
	t.Parallel()
	marks := []Mark{MarkDone, MarkNeeds, MarkFailed, MarkLater, MarkAhead}
	for _, c := range []struct {
		tier   GlyphTier
		want   string
		spin   string
		cursor string
		ascii  bool
	}{
		{GlyphsNerd, "✓!×–·", "◐◓◑◒", "›", false},
		{GlyphsSafe, "✓!×–·", "◐◓◑◒", "›", false},
		{GlyphsASCII, "+!x-.", `|/-\`, ">", true},
		{GlyphsOff, "+!x-.", `|/-\`, ">", true},
	} {
		var got, spin strings.Builder
		for _, m := range marks {
			got.WriteString(m.Glyph(c.tier, 0))
		}
		for tick := range 4 {
			spin.WriteString(MarkRunning.Glyph(c.tier, tick))
		}
		if got.String() != c.want || spin.String() != c.spin || cursorGlyph(c.tier) != c.cursor {
			t.Errorf("tier %d draws %q, spins %q, points %q; want %q, %q, %q",
				c.tier, got.String(), spin.String(), cursorGlyph(c.tier), c.want, c.spin, c.cursor)
		}
		if MarkRunning.Glyph(c.tier, 4) != MarkRunning.Glyph(c.tier, 0) {
			t.Errorf("tier %d: the running mark must cycle every four frames", c.tier)
		}
		if !c.ascii {
			continue
		}
		for _, f := range []*Form{withGlyphs(pageOneRunning(), c.tier), withGlyphs(pageThree(), c.tier)} {
			lines := frameLines(t, f, 2, 80, 24, 0)
			foot := slices.IndexFunc(lines, func(l string) bool { return l != "" && strings.Trim(l, "+-") == "" })
			for _, l := range append(lines[:foot:foot], lines[foot+3:]...) {
				if r := []rune(l); len(r) > 2 && r[2] > 0x7f {
					t.Errorf("tier %d draws %q at column 2: %q", c.tier, r[2], l)
				}
				if strings.ContainsAny(l, "✓◐◓◑◒›╭│") {
					t.Errorf("tier %d draws a Unicode mark: %q", c.tier, l)
				}
			}
		}
	}
	p := styles()
	for m, want := range map[Mark]string{MarkDone: "ok", MarkNeeds: "warn", MarkFailed: "fail", MarkRunning: "brand", MarkLater: "idle", MarkAhead: "idle"} {
		roles := map[string]any{"ok": p.ok, "warn": p.warn, "fail": p.fail, "brand": p.brand, "idle": p.idle}
		if fmt.Sprint(m.style(p)) != fmt.Sprint(roles[want]) {
			t.Errorf("mark %d draws in the wrong role, want %s", m, want)
		}
	}
	for m, want := range map[Mark]string{MarkDone: "ok: ", MarkNeeds: "warn: ", MarkFailed: "error: ", MarkLater: "", MarkAhead: "", MarkRunning: ""} {
		if got := m.Tag(); got != want {
			t.Errorf("mark %d tags plain output %q, want %q", m, got, want)
		}
	}
	if got := Worst(MarkLater, MarkDone, MarkNeeds); got != MarkNeeds {
		t.Errorf("Worst(–, ✓, !) = %d, want !", got)
	}
	if got := Worst(MarkDone, MarkFailed, MarkNeeds); got != MarkFailed {
		t.Errorf("Worst(✓, ×, !) = %d, want ×", got)
	}
	if got := Worst(MarkLater, MarkLater); got != MarkLater {
		t.Errorf("Worst(–, –) = %d, want –", got)
	}
}

// Marks sit at 2, labels at 4, sub-row labels and check details at 6, outcomes at 18, a field's ✓
// at 18 and its [ at 20; the chip ends at cw-3.
func TestTheChecklistColumns(t *testing.T) {
	t.Parallel()
	one := frameLines(t, pageOne("rootless tarball"), 4, 120, 36, 0)
	three := frameLines(t, pageThree(), 2, 120, 36, 0)
	for _, c := range []struct {
		lines      []string
		row, token string
		want       int
	}{
		{one, "Binary version", "!", 2},
		{one, "Binary version", "Binary version", 4},
		{one, "Download the latest", "Download", 6},
		{one, "managed configuration", "/home/op", -1},
		{one, "/home/op/.ssh", "/home/op", 6},
		{one, "sbx install", "›", 2},
		{one, "sbx install", "sbx install", 4},
		{one, "sbx install", "(•) rootless", 18},
		{one, "sbx install", "( ) distro", 40},
		{one, "sbx install", "( ) keep as is", 62},
		{one, "location", "location", 6},
		{one, "PATH", "PATH", 6},
		{one, "git, shell", "·", 2},
		{one, "git, shell", "page 2/4", 18},
		{one, "· sbx setup", "after init", 18},
		{one, "Prepare sbx", "page 1/4", 120 - 3 - len("page 1/4")},
		{three, "sbx  ", "kept 0.50.0", 18},
		{three, "opencode", "✓", 18},
		{three, "opencode", "[", 20},
		{three, "anthropic", "!", 18},
	} {
		if got := col(lineWith(c.lines, c.row), c.token); got != c.want {
			t.Errorf("%q on the %q row sits at column %d, want %d", c.token, c.row, got, c.want)
		}
	}
}

// The spacing rules: a blank after the head and after the done rows, a blank before each stub
// that does not already follow one, a blank after a check with details when a check follows; none
// after a stub, inside a section's rows, or before the foot rule; one blank under the card.
func TestTheChecklistSpacing(t *testing.T) {
	t.Parallel()
	blank := func(l string) bool { return strings.Trim(l, "│ ") == "" && l != "" }
	for _, c := range []struct {
		name  string
		form  *Form
		after []string // the rows a blank follows
		none  []string // the rows no blank follows
	}{
		{"p1", pageOne(keepAsIs), []string{"network:", "Download the latest", "Hermes Desktop", "Docker login"},
			[]string{"Prepare sbx", "├── checks", "Apple silicon", "virtualisation", "├── sbx", "sbx install", "├── ahead", "sbx setup"}},
		{"p2", pageTwo(), []string{"Set up git", "! sbx", "GitHub", "prompt kit"},
			[]string{"├── git", "git identity", "user.name", "user.email", "credentials", "sbx setup"}},
		{"p4", pageFour(), []string{"Agents in every", "credentials", "[Executor"},
			[]string{"Name the operator", "! sbx", "git, shell", "├── operator", "├── ahead"}},
	} {
		lines := frameLines(t, c.form, c.form.firstSelectable(), 120, 36, 0)
		at := func(want string) int {
			return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, want) })
		}
		for _, want := range c.after {
			if i := at(want); i < 0 || i+1 >= len(lines) || !blank(lines[i+1]) {
				t.Errorf("%s: a blank row must follow %q:\n%s", c.name, want, strings.Join(lines, "\n"))
			}
		}
		for _, want := range c.none {
			if i := at(want); i < 0 || i+1 >= len(lines) || blank(lines[i+1]) {
				t.Errorf("%s: no blank row may follow %q:\n%s", c.name, want, strings.Join(lines, "\n"))
			}
		}
		foot := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "├───") })
		bottom := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "╰") })
		if foot < 1 || blank(lines[foot-1]) {
			t.Errorf("%s: no blank row may sit over the foot rule", c.name)
		}
		if lines[bottom+1] != "" || col(lines[bottom+2], "›") != 2 {
			t.Errorf("%s: under the card one blank row, then the help at column 2:\n%s", c.name, strings.Join(lines[bottom:], "\n"))
		}
	}
}

// Esc on a checklist's first page asks before it leaves, as Ctrl+C does; past it Esc steps back,
// and a first page that is no checklist still cancels at once.
func TestEscOnPageOneAsksWhenTheFormIsAChecklist(t *testing.T) {
	t.Parallel()
	m, cmd := pressKeys(formAt(pageOne(keepAsIs), 80, 24), teaEsc)
	if quits(cmd) || !m.(formModel).quit.up() {
		t.Fatal("Esc on page 1 of a checklist must open the quit dialog")
	}
	if view := plain(m.(formModel).View().Content); !strings.Contains(view, "leave the wizard?") || !strings.Contains(view, "Prepare sbx") {
		t.Errorf("the dialog draws over the page it asks about:\n%s", view)
	}
	m, cmd = pressKeys(m, teaEsc)
	if quits(cmd) || m.(formModel).quit.up() {
		t.Error("Esc in the dialog stays on the page")
	}
	m, cmd = pressKeys(m, teaEsc, teaCtrlC)
	if !quits(cmd) || m.(formModel).out != Quit {
		t.Error("Esc then a confirming Ctrl+C must quit")
	}
	m, cmd = pressKeys(formAt(pageTwo(), 80, 24), teaEsc)
	if !quits(cmd) || m.(formModel).out != Back {
		t.Error("Esc past page 1 steps back")
	}
	plainPage := stepForm(1, 3)
	m, cmd = pressKeys(formAt(plainPage, 80, 24), teaEsc)
	if !quits(cmd) || m.(formModel).out != Cancelled {
		t.Error("Esc on page 1 of a form that is no checklist still cancels")
	}
}

// The quit dialog draws over the dimmed form: centred on the card's columns, under the head row,
// its band cleared between the walls.
func TestTheQuitDialogDrawsOverTheDimmedForm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		w, h, x0, y0 int
	}{
		{80, 24, 13, 6},
		{120, 36, 33, 7},
	} {
		lines := checklistFrame(t, checklistCase{form: pageThree, cursor: 2, quit: true}, c.w, c.h)
		cw := cardWidth(c.w)
		if col(lines[c.y0], "╭") != c.x0 || col(lines[c.y0], "╮") != c.x0+53 {
			t.Errorf("%dx%d: the box's top row is %q, want ╭ at %d and ╮ at %d", c.w, c.h, lines[c.y0], c.x0, c.x0+53)
		}
		if left, right := c.x0-1, cw-2-(c.x0+53); left != right {
			t.Errorf("%dx%d: the box sits %d columns from the left wall and %d from the right", c.w, c.h, left, right)
		}
		if !strings.Contains(lines[1], "Store agent credentials") || !strings.Contains(lines[1], "page 3/4") {
			t.Errorf("%dx%d: the box must leave the head row whole: %q", c.w, c.h, lines[1])
		}
		bottom := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "╰") })
		if col(lines[bottom-1], "esc back") < 0 {
			t.Errorf("%dx%d: the page's key line stays under the box", c.w, c.h)
		}
		for y := c.y0; y < c.y0+10; y++ {
			r := []rune(lines[y])
			if len(r) != cw || r[0] != '│' || r[cw-1] != '│' {
				t.Errorf("%dx%d row %d: the band keeps the walls: %q", c.w, c.h, y, lines[y])
				continue
			}
			if strings.TrimSpace(string(r[1:c.x0])) != "" || strings.TrimSpace(string(r[c.x0+54:cw-1])) != "" {
				t.Errorf("%dx%d row %d: the band must clear beside the box: %q", c.w, c.h, y, lines[y])
			}
		}
	}
	f := pageThree()
	s := newScreen(80, 24)
	f.draw(s, 2, 0)
	under := len(s.runs)
	q := quitGate{}.open(80, 24, f.Step.Leave, f.Glyphs)
	if !q.overlay(s, f.at) {
		t.Fatal("the dialog must fit over page 3 at 80x24")
	}
	for i, r := range s.runs[:under] {
		if !r.st.GetFaint() {
			t.Errorf("run %d %q under the dialog is not faint", i, r.text)
		}
	}
	for _, w := range []int{55, 80} {
		for h := 10; h <= 24; h++ {
			short := pageThree()
			s := newScreen(w, h)
			short.draw(s, 2, 0)
			if !(quitGate{}).open(w, h, short.Step.Leave, short.Glyphs).overlay(s, short.at) {
				continue
			}
			if bottom := screenLines(s)[short.at.bottom]; strings.Trim(bottom, "╰─╯") != "" {
				t.Errorf("%dx%d: the box crosses the card's bottom border: %q", w, h, bottom)
			}
		}
	}
	short := pageThree()
	s = newScreen(55, 12)
	short.draw(s, 2, 0)
	if (quitGate{}).open(55, 12, short.Step.Leave, short.Glyphs).overlay(s, short.at) {
		t.Error("a card too short for the box must leave the dialog to the whole screen")
	}
}

// A checklist too small to draw names the rows it needs, its facts counted, and the keys it still
// takes: no arrow, and the run's own line while steps run.
func TestAChecklistTooSmallNamesItsRowsAndKeys(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		form   *Form
		cursor int
		keys   string
	}{
		{"rest", pageOne(keepAsIs), 4, "enter run 1 step · esc quit · ctrl+c quit"},
		{"running", pageOneRunning(), -1, "running 1 of 1 — Docker login · ctrl+c stop"},
		{"page 2", pageTwo(), 3, "enter run 1 step · esc back · ctrl+c quit"},
	} {
		for _, w := range []int{40, 80, 120} {
			need := c.form.minHeight(w)
			if c.form.layout(w, need).tooSmall || !c.form.layout(w, need-1).tooSmall {
				t.Errorf("%s at %d columns says it needs %d rows; tooSmall at %d: %v, at %d: %v", c.name, w, need,
					need, c.form.layout(w, need).tooSmall, need-1, c.form.layout(w, need-1).tooSmall)
			}
		}
		lines := frameLines(t, c.form, c.cursor, 80, 9, 0)
		if want := fmt.Sprintf("terminal too small: 80x9, need %d rows", c.form.minHeight(80)); lines[0] != want || lines[1] != c.keys {
			t.Errorf("%s at 80x9 reads\n%s\n%s\nwant\n%s\n%s", c.name, lines[0], lines[1], want, c.keys)
		}
	}
}

// A word too long for its line keeps its head and tail around …, so a check's URL still ends on
// its version.
func TestALongWordInACheckKeepsItsTail(t *testing.T) {
	t.Parallel()
	fix := lineWith(frameLines(t, pageOne(keepAsIs), 4, 40, 40, 0), "https://")
	if !strings.Contains(fix, "…") || !strings.Contains(fix, "/sbx/v9.9.9 │") {
		t.Errorf("at 40 the fix's URL must keep its tail: %q", fix)
	}
	if got := clipMiddle("https://example.invalid/sbx/v9.9.9", 20); got != "https://ex…bx/v9.9.9" {
		t.Errorf("clipMiddle cut to %q", got)
	}
}

// Render draws the form at rest, the cursor where it opens, as the plain text cmd goldens read.
func TestRenderIsTheFormAtRest(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile(filepath.Join("testdata", "checklist", "p1-rest_80x24.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pageOne(keepAsIs).Render(80, 24); got != string(want) {
		t.Errorf("Render differs from the p1-rest golden:\n%s", got)
	}
}
