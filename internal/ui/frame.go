// SPEC: _spec/internal/ui/step-lines.puml (THE HAND-OFF FRAME), _spec/internal/ui/output-vocabulary.puml
package ui

import (
	"fmt"
	"strings"
)

// The frame line's widest draw, and its width where nothing measures one.
const (
	frameMax      = 120
	frameFallback = 80
)

// frameRule is the frame line's rule rune; frameClose is how many close it at least.
const (
	frameRule  = "─"
	frameClose = 2
)

// frame is one frame line's pieces: the command as drawn, the parts as drawn, and the closing rules.
type frame struct {
	command, rest string
	fill          int
}

func (f frame) head() string {
	head := frameRule + frameRule + " " + f.command + " " + frameRule + frameRule
	if f.rest != "" {
		head += " " + f.rest
	}
	return head
}

func (f frame) String() string { return f.head() + " " + strings.Repeat(frameRule, f.fill) }

// FrameLine is the line printed before another program takes the terminal:
// "── <command> ── <parts joined by " · "> ──…", min(w, 120) wide. Parts before the last two
// are the reason. When the line does not fit it shortens, in order: "when it exits" reads
// "on exit"; the reason drops; the command keeps its head before "…"; the rest keeps its head.
func FrameLine(w int, command string, parts ...string) string {
	return frameFor(w, command, parts...).String()
}

func frameFor(w int, command string, parts ...string) frame {
	width := min(w, frameMax)
	if width <= 0 {
		width = frameFallback
	}
	onExit := make([]string, len(parts))
	for i, p := range parts {
		onExit[i] = strings.ReplaceAll(p, " when it exits", " on exit")
	}
	tries := [][]string{parts, onExit}
	if len(onExit) > 2 {
		tries = append(tries, onExit[len(onExit)-2:])
	}
	for _, ps := range tries {
		if f, ok := fit(width, command, strings.Join(ps, " · ")); ok {
			return f
		}
	}
	rest := strings.Join(tries[len(tries)-1], " · ")
	if room := width - textWidthOf(frame{rest: rest}.head()) - 1 - frameClose; room >= 2 {
		f, _ := fit(width, cutWithEllipsis(command, room), rest)
		return f
	}
	head := cutWithEllipsis(command, 2)
	room := width - textWidthOf(frame{command: head, rest: "…"}.head()) - 1 - frameClose
	f, _ := fit(width, head, cutWithEllipsis(rest, max(room+1, 1)))
	return f
}

// fit is the frame at width, and whether frameClose rules still close it.
func fit(width int, command, rest string) (frame, bool) {
	f := frame{command: command, rest: rest}
	f.fill = width - textWidthOf(f.head()) - 1
	if f.fill < frameClose {
		f.fill = frameClose
		return f, false
	}
	return f, true
}

// cutWithEllipsis is s cut to room columns, its head kept before "…".
func cutWithEllipsis(s string, room int) string {
	if textWidthOf(s) <= room {
		return s
	}
	return strings.TrimRight(cutToWidth(s, max(room-1, 0)), " ") + "…"
}

// Frame prints a blank row, then the frame line for the program about to take the terminal.
// Plain output carries the same characters with no escapes.
func (p *Printer) Frame(command string, parts ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	p.flushSection()
	f := frameFor(p.width(), command, parts...)
	line := f.String()
	if p.Tier == GlyphsASCII || p.Tier == GlyphsOff {
		line = strings.ReplaceAll(line, frameRule, "-")
	} else if !p.Plain {
		rules := tint(ColorRule, frameRule+frameRule)
		line = rules + " " + ANSIBold + f.command + ANSIReset + " " + rules
		if f.rest != "" {
			line += " " + grey(f.rest)
		}
		line += " " + tint(ColorRule, strings.Repeat(frameRule, f.fill))
	}
	fmt.Fprint(p.W, "\n"+line+"\n")
}

// Blank prints one blank row: under what a framed program printed, and before a verdict that follows it.
func (p *Printer) Blank() {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.W, "\n")
}

// Frame prints Default's frame line; see Printer.Frame.
func Frame(command string, parts ...string) { Default.Frame(command, parts...) }

// Blank prints Default's blank row; see Printer.Blank.
func Blank() { Default.Blank() }
