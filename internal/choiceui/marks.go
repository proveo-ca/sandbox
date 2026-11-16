// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import "charm.land/lipgloss/v2"

// Mark is a checklist row's state, drawn at column 2 in the card and in the stream.
type Mark int

const (
	MarkNone    Mark = iota
	MarkDone         // done
	MarkNeeds        // needs the operator
	MarkFailed       // failed
	MarkLater        // later, or left as is
	MarkAhead        // ahead, or waiting in a run
	MarkRunning      // running
)

// The running mark's frames, one per formTick.
var (
	spinFancy = []string{"◐", "◓", "◑", "◒"}
	spinPlain = []string{"|", "/", "-", `\`}
)

// Glyph is the mark as tier t draws it on animation frame tick.
func (m Mark) Glyph(t GlyphTier, tick int) string {
	plain := plainGlyphs(t)
	pick := func(fancy, ascii string) string {
		if plain {
			return ascii
		}
		return fancy
	}
	switch m {
	case MarkDone:
		return pick("✓", "+")
	case MarkNeeds:
		return "!"
	case MarkFailed:
		return pick("×", "x")
	case MarkLater:
		return pick("–", "-")
	case MarkAhead:
		return pick("·", ".")
	case MarkRunning:
		frames := spinFancy
		if plain {
			frames = spinPlain
		}
		return frames[(tick%len(frames)+len(frames))%len(frames)]
	}
	return ""
}

// Tag is the mark's head in plain output: "ok: ", "warn: ", "error: ", or "" for a mark plain
// output prints bare or not at all.
func (m Mark) Tag() string {
	switch m {
	case MarkDone:
		return "ok: "
	case MarkNeeds:
		return "warn: "
	case MarkFailed:
		return "error: "
	}
	return ""
}

// style is the mark's ink.
func (m Mark) style(p palette) lipgloss.Style {
	switch m {
	case MarkDone:
		return p.ok
	case MarkNeeds:
		return p.warn
	case MarkFailed:
		return p.fail
	case MarkRunning:
		return p.brand
	}
	return p.idle
}

// markRank orders the marks a done row folds: failed, needs, done, later.
var markRank = map[Mark]int{MarkFailed: 4, MarkNeeds: 3, MarkDone: 2, MarkLater: 1}

// Worst is the mark a row standing for every mark in marks draws: × over ! over ✓ over –.
func Worst(marks ...Mark) Mark {
	worst := MarkNone
	for _, m := range marks {
		if markRank[m] > markRank[worst] {
			worst = m
		}
	}
	return worst
}

// cursorGlyph is the cursor's mark in tier t.
func cursorGlyph(t GlyphTier) string {
	if plainGlyphs(t) {
		return ">"
	}
	return "›"
}
