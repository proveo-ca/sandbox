// SPEC: _spec/internal/choiceui/tui-stack.puml, _spec/internal/choiceui/viewport.puml
package choiceui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frameCase is one form in one state; it is drawn at each of its sizes.
type frameCase struct {
	name   string
	form   func() *Form
	cursor int
	tick   int
	sizes  [][2]int
}

var (
	wideSizes  = [][2]int{{160, 45}, {150, 40}, {140, 50}, {120, 40}}
	usualSizes = [][2]int{{120, 40}, {100, 30}, {80, 24}}
	tightSizes = [][2]int{{80, 20}, {80, 16}, {80, 14}, {80, 12}, {64, 12}, {60, 9}}
)

// sizes joins size lists, each size once.
func sizes(lists ...[][2]int) [][2]int {
	seen := map[[2]int]bool{}
	var out [][2]int
	for _, l := range lists {
		for _, sz := range l {
			if !seen[sz] {
				seen[sz] = true
				out = append(out, sz)
			}
		}
	}
	return out
}

func withFrame(f *Form, fr Frame) *Form {
	f.Topology = func(*Form, int) *Frame { return &fr }
	return f
}

func withGlyphs(f *Form, t GlyphTier) *Form {
	f.Glyphs = t
	return f
}

// listsForm opens a read-only list under the cursor's cell.
func listsForm() *Form {
	items := make([]string, 12)
	for i := range items {
		items[i] = fmt.Sprintf("credential-%02d", i)
	}
	m := &More{Cell: 1, Options: items, On: []bool{true, false, true}, ReadOnly: true, At: 3}
	return &Form{Title: "credentials in this run", Rows: []Row{
		{Label: "credentials", Multi: true, Locked: true, Hover: 1, Options: []string{"API keys", "OAuth", "SSH"},
			On: []bool{true, true, false}, Cols: []int{0, 1, 2}, Lists: map[int]*More{1: m}},
		{Label: "egress", Options: []string{"allow-all", "balanced", "deny-all"}, Selected: 1},
	}}
}

// typedForm is the credentials page with one value typed and one row warned.
func typedForm() *Form {
	f := fieldForm()
	f.Rows[1].Value = "sk-typed-value"
	return f
}

// frameCases is every form state the renderer is held to.
func frameCases() []frameCase {
	hopless := paneFrame()
	hopless.Hop, hopless.Key = "", KeyInSquare
	direct := Frame{Host: "pluvo", HostOS: "(macOS)", Direct: true, Interface: "tui", Lane: LaneWatched, Open: 3, Caption: "open — nothing stands between the agent and the network"}
	local := paneFrame()
	local.HostRight, local.HostModel, local.HostLit, local.Key = "Ollama", "qwen3.8", true, KeyNone
	cases := []frameCase{
		{"budget", budgetForm, 0, 0, sizes(wideSizes, usualSizes, tightSizes)},
		{"budget-help", budgetForm, 2, 0, usualSizes},
		{"budget-ascii", func() *Form { return withGlyphs(budgetForm(), GlyphsASCII) }, 1, 0, [][2]int{{150, 40}, {100, 30}}},
		{"budget-off", func() *Form { return withGlyphs(budgetForm(), GlyphsOff) }, 1, 0, [][2]int{{150, 40}, {100, 30}}},
		{"tall-top", tallForm, 0, 0, sizes(wideSizes, tightSizes)},
		{"tall-mid", tallForm, 3, 0, sizes([][2]int{{150, 40}, {100, 30}}, tightSizes)},
		{"tall-last", tallForm, 5, 0, sizes([][2]int{{150, 40}, {100, 30}}, tightSizes)},
		{"tall-tick", tallForm, 1, 7, [][2]int{{150, 40}, {100, 45}}},
		{"scroll", scrollForm, 2, 0, [][2]int{{80, 24}, {80, 10}}},
		{"fields", fieldForm, 0, 0, [][2]int{{100, 30}, {80, 24}}},
		{"fields-typed", typedForm, 1, 0, [][2]int{{100, 30}}},
		{"fields-warn", fieldForm, 2, 0, [][2]int{{80, 24}}},
		{"locked-hover", lockedForm, 0, 0, usualSizes},
		{"locked", lockedForm, 1, 0, [][2]int{{100, 30}}},
		{"step-first", func() *Form { return stepForm(1, 3) }, 0, 0, [][2]int{{80, 24}}},
		{"step-second", func() *Form { return stepForm(2, 3) }, 1, 0, [][2]int{{80, 24}, {64, 12}}},
		{"lists-open", listsForm, 0, 0, [][2]int{{120, 24}, {80, 24}, {80, 10}}},
		{"env-list", envForm, 0, 0, [][2]int{{120, 36}, {80, 24}}},
		{"env-filter", func() *Form { return filtering("mod") }, 0, 0, [][2]int{{120, 36}, {80, 24}}},
		{"env-nomatch", func() *Form { return filtering("zzq") }, 0, 0, [][2]int{{120, 36}, {80, 24}}},
		{"unframed-tall", tallForm, 0, 0, [][2]int{{36, 24}}},
		{"unframed-step", func() *Form { return stepForm(2, 3) }, 1, 0, [][2]int{{36, 12}}},
	}
	for name, fr := range map[string]Frame{"pane": paneFrame(), "hopless": hopless, "direct": direct, "local-model": local} {
		for _, tier := range []GlyphTier{GlyphsNerd, GlyphsSafe, GlyphsASCII} {
			fr, tier := fr, tier
			cases = append(cases, frameCase{
				fmt.Sprintf("figure-%s-tier%d", name, tier),
				func() *Form { return withGlyphs(withFrame(budgetForm(), fr), tier) },
				1, 3, [][2]int{{160, 45}, {100, 45}, {80, 24}},
			})
		}
	}
	for key := KeyAtHost; key <= KeyNone; key++ {
		for lane := LaneScreened; lane <= LaneAsked; lane++ {
			fr := paneFrame()
			fr.Key, fr.Lane = key, lane
			cases = append(cases, frameCase{
				fmt.Sprintf("figure-key%d-lane%d", key, lane),
				func() *Form { return withFrame(tallForm(), fr) },
				2, 5, [][2]int{{160, 45}, {100, 45}},
			})
		}
	}
	for focus := FocusNone; focus <= FocusSay; focus++ {
		fr := paneFrame()
		fr.Focus = focus
		cases = append(cases, frameCase{
			fmt.Sprintf("figure-focus%d", focus),
			func() *Form { return withFrame(tallForm(), fr) },
			0, 0, [][2]int{{160, 45}},
		})
	}
	return cases
}

// frameLines draws f at tick and returns the screen's rows.
func frameLines(t *testing.T, f *Form, cursor, w, h, tick int) []string {
	t.Helper()
	s := simScreen(t, w, h)
	defer s.Fini()
	f.draw(s, cursor, tick)
	return screenLines(s)
}

// frameText is the frame as plain text: one line per row, trailing blanks and blank rows at the end cut.
func frameText(lines []string) string {
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestFramesMatchTheGoldenFiles holds the renderer to frames recorded from the
// tcell renderer. PROVEO_UPDATE_FRAMES=1 records them.
func TestFramesMatchTheGoldenFiles(t *testing.T) {
	dir := filepath.Join("testdata", "frames")
	update := os.Getenv("PROVEO_UPDATE_FRAMES") != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, c := range frameCases() {
		for _, size := range c.sizes {
			name := fmt.Sprintf("%s_%dx%d.txt", c.name, size[0], size[1])
			if seen[name] {
				t.Fatalf("two cases write %s", name)
			}
			seen[name] = true
			got := frameText(frameLines(t, c.form(), c.cursor, size[0], size[1], c.tick))
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
