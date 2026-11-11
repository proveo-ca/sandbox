// SPEC: _spec/internal/ui/output-vocabulary.puml
package ui

import (
	"bytes"
	"strings"
	"testing"
)

// sizedTerm is a terminal of a fixed width.
type sizedTerm struct {
	bytes.Buffer
	width int
}

func (s *sizedTerm) cols() int { return s.width }

// teedPrinter is a fancy printer on a terminal of the given width, teed to a log.
func teedPrinter(width int) (*Printer, *sizedTerm, *bytes.Buffer) {
	term, log := &sizedTerm{width: width}, &bytes.Buffer{}
	return &Printer{W: multi{term: term, log: log}, Tier: GlyphsSafe, term: term}, term, log
}

func TestALiveFrameReachesTheTerminalAndNeverTheLog(t *testing.T) {
	p, term, log := teedPrinter(80)
	p.Live(RoleAsync, "pulling %s: %5.1f%%", "qwen3", 12.5)
	p.Live(RoleAsync, "pulling %s: %5.1f%%", "qwen3", 50.0)
	p.EndLive()
	p.Okf("pulled qwen3")

	if got := log.String(); strings.Contains(got, "pulling") || strings.ContainsAny(got, "\r\x1b") {
		t.Errorf("the log received a live frame or a control byte: %q", got)
	}
	if !strings.Contains(log.String(), "pulled qwen3") {
		t.Errorf("the log lost the ordinary line: %q", log.String())
	}
	screen := term.String()
	for _, want := range []string{"pulling qwen3:  12.5%", "pulling qwen3:  50.0%", "pulled qwen3"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the terminal never showed %q: %q", want, screen)
		}
	}
	if n := strings.Count(screen, "\n"); n != 1 {
		t.Errorf("the terminal scrolled %d lines for two frames and one line; a frame must replace the one before it", n)
	}
}

func TestAnOrdinaryLineErasesTheLiveFrameAndRedrawsItBelow(t *testing.T) {
	p, term, _ := teedPrinter(80)
	p.Live(RoleAsync, "step 2 of 9")
	term.Reset()
	p.Notef("a line from the step")
	got := term.String()
	erase, line, redraw := strings.Index(got, "\r\x1b[K"), strings.Index(got, "a line from the step"), strings.LastIndex(got, "step 2 of 9")
	if erase < 0 || line < erase || redraw < line {
		t.Errorf("want erase, the line, then the frame again; got %q", got)
	}
	p.EndLive()
	term.Reset()
	p.Notef("after the live line ended")
	if strings.Contains(term.String(), "step 2 of 9") {
		t.Errorf("an ended live line was redrawn: %q", term.String())
	}
}

func TestALiveFrameIsCutToTheTerminalsWidth(t *testing.T) {
	p, term, _ := teedPrinter(30)
	p.Live(RoleAsync, "%s", strings.Repeat("x", 100))
	frame := term.String()
	if w := displayWidth([]byte(strings.TrimSuffix(strings.TrimPrefix(frame, "\r"), "\x1b[K"))); w >= 30 {
		t.Errorf("the frame is %d columns on a 30-column terminal; a wrapped frame cannot be erased in place", w)
	}
}

func TestAPlainPrinterDrawsNoLiveFrame(t *testing.T) {
	var out bytes.Buffer
	p := New(&out)
	p.Live(RoleAsync, "pulling")
	p.EndLive()
	p.Okf("done")
	if got := out.String(); strings.Contains(got, "pulling") || strings.ContainsAny(got, "\r\x1b") {
		t.Errorf("a plain printer drew a live frame: %q", got)
	}
}

func TestAQuestionEndsTheLiveLine(t *testing.T) {
	p, term, _ := teedPrinter(80)
	p.Live(RoleAsync, "waiting")
	p.Askf("continue?")
	term.Reset()
	p.Notef("answered")
	if strings.Contains(term.String(), "waiting") {
		t.Errorf("the live line came back after a question: %q", term.String())
	}
}
