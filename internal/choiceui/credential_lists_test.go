// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestDisabledCredentialListsNavigateWithoutChangingCredentials(t *testing.T) {
	r := Row{Label: "credentials", Multi: true, Locked: true, Options: []string{"API keys", "OAuth", "SSH"}, On: []bool{true, true, true}, Off: []bool{true, true, true}, Cols: []int{0, 1, 2}, Lists: map[int]*More{}}
	for i := range r.Options {
		r.Lists[i] = &More{Cell: i, Options: []string{fmt.Sprintf("credential-%d-one", i), fmt.Sprintf("credential-%d-two", i)}, On: []bool{true, true}, ReadOnly: true}
	}
	f := &Form{Title: "test", Rows: []Row{r}}
	for i := range r.Options {
		if got := f.Rows[0].activeMore(); got != r.Lists[i] {
			t.Fatalf("cell %d opened wrong list", i)
		}
		f.moreKey(0, named("down"))
		f.moreKey(0, key(' '))
		f.toggle(0)
		if !r.Lists[i].On[1] || !r.On[i] {
			t.Fatalf("read-only cell %d changed", i)
		}
		f.cycle(0, 1)
	}
	if got := f.Selections("credentials"); len(got) != 0 {
		t.Fatalf("inventory became run choices: %v", got)
	}
}

func TestCredentialListsDrawAtTheirOwnColumnAndScroll(t *testing.T) {
	s := simScreen(t, 120, 24)
	items := make([]string, 40)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	m := &More{Cell: 1, Options: items, ReadOnly: true, At: 39}
	f := &Form{Title: "test", Rows: []Row{{Label: "credentials", Multi: true, Locked: true, Hover: 1, Options: []string{"API keys", "OAuth", "SSH"}, Cols: []int{0, 1, 2}, Lists: map[int]*More{1: m}}}}
	f.draw(s, 0, 0)
	var out strings.Builder
	for y := 0; y < 24; y++ {
		for x := 0; x < 120; x++ {
			r, _, _, _ := s.GetContent(x, y)
			out.WriteRune(r)
		}
		out.WriteByte('\n')
	}
	if !strings.Contains(out.String(), "item-39") || strings.Contains(out.String(), "item-00") {
		t.Fatalf("list did not scroll to selection:\n%s", out.String())
	}
	entry, corner := -1, -1
	for y, line := range strings.Split(out.String(), "\n") {
		rs := []rune(line)
		if col := slices.Index(rs, '╭'); col >= 0 && y > 0 {
			corner = col
		}
		if i := strings.Index(line, "[ ] item-39"); i >= 0 {
			entry = len([]rune(line[:i]))
		}
	}
	if entry != optOrigin+colPitch || corner != entry-4 {
		t.Fatalf("OAuth list did not open at its cell: entry at %d, corner at %d, want %d and %d:\n%s",
			entry, corner, optOrigin+colPitch, optOrigin+colPitch-4, out.String())
	}
}

func TestCredentialDropdownArrowMatchesPortsAndRespectsGlyphMode(t *testing.T) {
	s := simScreen(t, 120, 24)
	for _, tc := range []struct {
		glyphs GlyphTier
		arrow  string
	}{{GlyphsSafe, " ▾"}, {GlyphsASCII, " v"}} {
		for _, inventory := range []bool{false, true} {
			s.Clear()
			r := Row{Label: "ports", Multi: true, Options: []string{"...More"}}
			m := &More{Options: []string{"item"}}
			if inventory {
				r.Label, r.Locked, r.Off, r.Options = "credentials", true, []bool{true}, []string{"API keys"}
				m.ReadOnly, r.Lists = true, map[int]*More{0: m}
			} else {
				r.More = m
			}
			f := &Form{Glyphs: tc.glyphs, Rows: []Row{r}}
			f.drawBodyLine(newCanvas(s), styles(), newCard(120, f.Glyphs, styles()), bodyLine{kind: lineRow, row: 0}, 0, colPitch, false, false)
			var line strings.Builder
			for x := 0; x < 120; x++ {
				ch, _, _, _ := s.GetContent(x, 0)
				line.WriteRune(ch)
			}
			if !strings.Contains(line.String(), r.Options[0]+tc.arrow) {
				t.Fatalf("dropdown arrow missing: %s", line.String())
			}
		}
	}
}
