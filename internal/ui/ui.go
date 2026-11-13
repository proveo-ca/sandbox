// SPEC: _spec/internal/ui/output-vocabulary.puml, _spec/_conventions/spec-conventions.puml, _spec/internal/runlog/run-transcript.puml, _spec/_conventions/tui-design-language.puml
package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/termtext"
)

// GlyphTier is how much decoration the terminal is trusted with.
type GlyphTier int

const (
	GlyphsNerd GlyphTier = iota // declared: the operator's font carries private-use devicons
	GlyphsASCII
	GlyphsOff
	GlyphsSafe // undeclared default: standard Unicode only, no private-use runes
)

func GlyphTierFrom(lookup func(string) string) GlyphTier {
	switch v := strings.ToLower(strings.TrimSpace(lookup("PROVEO_GLYPHS"))); {
	case v == "ascii":
		return GlyphsASCII
	case v == "none" || envflag.Off(v):
		return GlyphsOff
	case v == "nerd" || v == "nerdfont":
		return GlyphsNerd
	case v == "safe":
		return GlyphsSafe
	}
	return GlyphsSafe
}

// Role is what a status line is ABOUT — which layer of the run is speaking.
type Role int

const (
	RoleNone  Role = iota // no mark — a continuation line under a preceding verb
	RoleHost              // host · platform · operator boundary — cyan
	RoleApp               // first-party app / runtime service — teal
	RoleAsync             // queue · scheduler · background · retry — lime
	RoleCloud             // external SaaS / vendor / registry — slate
	RoleStore             // persistence · state · paths on disk — light
	RoleError             // destructive · security-sensitive — red
)

func (r Role) color() (int, bool) {
	switch r {
	case RoleHost:
		return ColorHost, true
	case RoleApp:
		return ColorApp, true
	case RoleAsync:
		return ColorAsync, true
	case RoleCloud:
		return ColorCloud, true
	case RoleStore:
		return ColorDB, true
	case RoleError:
		return ColorError, true
	}
	return 0, false
}

type sev int

const (
	sevNone sev = iota
	sevOk
	sevWarn
	sevFail
)

const textCol = 2

func mark(t GlyphTier, s sev, r Role) string {
	if t == GlyphsOff {
		return strings.Repeat(" ", textCol)
	}
	ascii := t == GlyphsASCII
	switch s {
	case sevOk:
		if ascii {
			return "+ "
		}
		return "✓ "
	case sevWarn:
		if ascii {
			return "! "
		}
		return "⚠ "
	case sevFail:
		if ascii {
			return "x "
		}
		return "× "
	}
	if r == RoleNone {
		return strings.Repeat(" ", textCol)
	}
	if ascii {
		return "o "
	}
	return "● "
}

func tag(s sev, r Role) string {
	switch s {
	case sevOk:
		return "ok: "
	case sevWarn:
		return "warn: "
	case sevFail:
		return "error: "
	}
	if r == RoleNone {
		return strings.Repeat(" ", textCol)
	}
	return ""
}

// Printer writes marked status lines to W.
type Printer struct {
	W     io.Writer
	Plain bool
	Tier  GlyphTier

	mu             sync.Mutex
	pending, shown string

	term      io.Writer // the terminal alone; nil when W is not one
	live      string    // the live line as drawn; "" when there is none
	suspended int       // holders of SuspendLive; the live line stays off the terminal while any holds
}

// Section names the subject the following lines describe, drawn as the divider
// choiceui uses for a Divider row. Section("") withdraws a heading not yet drawn.
func (p *Printer) Section(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if label == p.shown {
		p.pending = ""
		return
	}
	p.pending = label
}

func sectionRule(t GlyphTier, plain bool) string {
	if plain || (t != GlyphsNerd && t != GlyphsSafe) {
		return "-"
	}
	return "─"
}

func sectionHeading(t GlyphTier, plain bool, label string, width int) string {
	rule := sectionRule(t, plain)
	spaced := " " + label + " "
	head := strings.Repeat(" ", textCol) + strings.Repeat(rule, sectionRules) + spaced
	fill := sectionRules
	if width > 0 {
		if room := width - textWidthOf(head); room > fill {
			fill = room
		}
	}
	return head + strings.Repeat(rule, fill)
}

const (
	SectionScope       = "scope"       // the numbered scope picker: the sub-tree of a monorepo
	SectionRun         = "run"         // the transcript path, the scope, the image pull
	SectionCredentials = "credentials" // form row: where the login rests; `proveo init`: what sbx's store holds
	SectionWorkspace   = "workspace"   // the checkout, the clone, links, what is not mounted
	SectionEgress      = "egress"      // form row: sbx's policy and the host services the sandbox reaches
	SectionInterface   = "interface"   // form divider: the ports, browser and IDE host the operator reaches
	SectionSbx         = "sbx"         // what proveo hands sbx, the stale check, sbx's own create block; `proveo init`: the host and its sbx
	SectionPrompt      = "prompt"      // `proveo init`: the shell-prompt kit
	SectionGit         = "git"         // `proveo init`: the sandbox git identity and GitHub access
	SectionResults     = "results"     // after the agent: how it ended, what came home, the sandbox's fate; `proveo init`: what waits and the verdict
	SectionIDE         = "ide"         // a kept sandbox: how an IDE attaches to it
	SectionTargets     = "targets"     // `proveo run` with no target and no terminal: the harness images, after running
	SectionRunning     = "running"     // `proveo run` with no target and no terminal: tasks whose session is up
	SectionOperator    = "operator"    // `proveo init`: the name agents address the operator by
)

const sectionRules = 6

func textWidthOf(s string) int { return displayWidth([]byte(s)) }

func (p *Printer) width() int {
	if w, ok := p.W.(interface{ cols() int }); ok {
		return w.cols()
	}
	return 0 // not measurable: the caller decides what to do without a width
}

// heading draws the pending section heading now.
func (p *Printer) heading() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	p.flushSection()
}

func (p *Printer) flushSection() {
	if p.pending == "" || p.pending == p.shown {
		return
	}
	head := sectionHeading(p.Tier, p.Plain, p.pending, p.width())
	if !p.Plain {
		spaced := " " + p.pending + " "
		before, after, _ := strings.Cut(head, spaced)
		head = ANSI(ColorRule) + before + ANSIReset + ANSIBold + ANSI(ColorSecondary) + spaced + ANSIReset + ANSI(ColorRule) + after + ANSIReset
	}
	fmt.Fprint(p.W, "\n"+head+"\n")
	p.shown, p.pending = p.pending, ""
}

func New(w io.Writer) *Printer {
	fancy := isFancy(w)
	p := &Printer{W: w, Plain: !fancy, Tier: GlyphTierFrom(os.Getenv)}
	if fancy {
		p.W, p.term = wrapFor(w), w
	}
	return p
}

// NewTerminal is a Printer that draws on w as on a terminal cols wide.
func NewTerminal(w io.Writer, cols int) *Printer {
	return &Printer{W: &wrapWriter{w: w, width: func() int { return cols }}, Tier: GlyphTierFrom(os.Getenv), term: w}
}

// Live draws one line that the next frame replaces in place. It reaches the
// terminal alone, never a tee; a plain printer draws nothing.
func (p *Printer) Live(r Role, format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Plain || p.term == nil {
		return
	}
	p.eraseLive()
	p.flushSection()
	p.live = p.liveFrame(r, fmt.Sprintf(format, a...))
	p.drawLive()
}

// EndLive erases the live line.
func (p *Printer) EndLive() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	p.live = ""
}

// liveFrame is one frame: the role's mark, then body cut to the terminal's width.
func (p *Printer) liveFrame(r Role, body string) string {
	prefix := mark(p.Tier, sevNone, r)
	if cols := p.width(); cols > 0 {
		body = cutToWidth(body, cols-textWidthOf(prefix)-1)
	}
	if c, ok := r.color(); ok {
		prefix = ANSI(c) + prefix + ANSIReset
	}
	return prefix + body
}

// eraseLive clears the live line off the terminal ahead of an ordinary line.
func (p *Printer) eraseLive() {
	if p.live != "" && p.term != nil && p.suspended == 0 {
		fmt.Fprint(p.term, "\r\033[K")
	}
}

// drawLive puts the live line back as the terminal's last line.
func (p *Printer) drawLive() {
	if p.live != "" && p.term != nil && p.suspended == 0 {
		fmt.Fprint(p.term, "\r"+p.live+"\033[K")
	}
}

// cutToWidth is s shortened to at most width columns.
func cutToWidth(s string, width int) string {
	w := 0
	for i, r := range s {
		if w += runewidth.RuneWidth(r); w > width {
			return s[:i]
		}
	}
	return s
}

// Default is the process-wide status printer (stderr).
var Default = New(os.Stderr)

// TeeTo copies every line to w as well, and Logf lines to w alone; restore puts back the printers it replaced.
func TeeTo(w io.Writer) (restore func()) {
	prev, prevLog := Default, logOnly
	restore = func() { Default, logOnly = prev, prevLog }
	if w == nil {
		return restore
	}
	term := Default
	term.mu.Lock()
	pending, shown := term.pending, term.shown
	term.mu.Unlock()
	Default = &Printer{
		W:       multi{term: term.W, log: w},
		Plain:   term.Plain,
		Tier:    term.Tier,
		pending: pending,
		shown:   shown,
		term:    term.term,
	}
	logOnly = &Printer{W: w, Plain: true, Tier: term.Tier}
	return restore
}

var logOnly *Printer

func Logf(format string, a ...any) {
	if logOnly != nil {
		logOnly.Notef(format, a...)
	}
}

// LogWriter is a writer whose every line reaches the run log alone; Close writes a last line that has no newline.
func LogWriter() io.WriteCloser { return &logLines{} }

type logLines struct {
	mu  sync.Mutex
	buf []byte
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := strings.TrimRight(string(l.buf[:i]), "\r"); strings.TrimSpace(line) != "" {
			Logf("%s", line)
		}
		l.buf = l.buf[i+1:]
	}
}

func (l *logLines) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.TrimSpace(string(l.buf)) != "" {
		Logf("%s", string(l.buf))
	}
	l.buf = nil
	return nil
}

type multi struct{ term, log io.Writer }

func (m multi) cols() int {
	if w, ok := m.term.(interface{ cols() int }); ok {
		return w.cols()
	}
	return 0
}

func (m multi) Write(p []byte) (int, error) { return m.writeHang(p, textCol) }

// writeHang wraps the terminal copy at column hang; the log copy stays one line per line.
func (m multi) writeHang(p []byte, hang int) (int, error) {
	var n int
	var err error
	if hw, ok := m.term.(hangWriter); ok {
		n, err = hw.writeHang(p, hang)
	} else {
		n, err = m.term.Write(p)
	}
	_, _ = m.log.Write([]byte(termtext.StripANSI(string(p))))
	return n, err
}

func isFancy(w io.Writer) bool {
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (p *Printer) line(r Role, s sev, format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	p.flushSection()
	prefix := mark(p.Tier, s, r)
	if p.Plain {
		fmt.Fprintf(p.W, tag(s, r)+format+"\n", a...)
		return
	}
	body := paintBody(s, fmt.Sprintf(format, a...))
	if c, ok := r.color(); ok && s == sevNone {
		prefix = ANSI(c) + prefix + ANSIReset
	}
	if c, ok := sevColor(s); ok {
		prefix = ANSI(c) + prefix + ANSIReset
	}
	fmt.Fprint(p.W, prefix+body+"\n")
}

const labelMax = 40

// splitDetail cuts a body at its first " — " or at a short "label: ".
func splitDetail(body string) (head, tail string, ok bool) {
	cut := -1
	if i := strings.Index(body, " — "); i > 0 {
		cut = i
	}
	if i := strings.Index(body, ": "); i > 0 && textWidthOf(body[:i]) <= labelMax && (cut < 0 || i < cut) {
		return body[:i+1], body[i+1:], true
	}
	if cut < 0 {
		return body, "", false
	}
	return body[:cut], body[cut:], true
}

func grey(s string) string {
	if s == "" {
		return s
	}
	on := ANSI(ColorMuted)
	return on + strings.ReplaceAll(s, ANSIReset, ANSIReset+on) + ANSIReset
}

// paintBody greys every line except a severity head.
func paintBody(s sev, body string) string {
	if s == sevNone {
		return grey(body)
	}
	head, tail, ok := splitDetail(body)
	if s == sevFail {
		head = ANSIBold + head + ANSIReset
	}
	if !ok {
		return head
	}
	return head + grey(tail)
}

func sevColor(s sev) (int, bool) {
	switch s {
	case sevOk:
		return ColorSuccess, true
	case sevWarn:
		return ColorWarn, true
	case sevFail:
		return ColorFailure, true
	}
	return 0, false
}

// Okf reports a success ("✓ ", ascii "+ ", plain "ok: ").
func (p *Printer) Okf(format string, a ...any) { p.line(RoleNone, sevOk, format, a...) }

// Warnf reports a non-fatal problem ("⚠ ", ascii "! ", plain "warn: ").
func (p *Printer) Warnf(format string, a ...any) { p.line(RoleNone, sevWarn, format, a...) }

// Failf reports an error ("× ", ascii "x ", plain "error: ").
func (p *Printer) Failf(format string, a ...any) { p.line(RoleNone, sevFail, format, a...) }

// Notef writes a continuation line under a preceding verb: no marker in
// either rendering, indented to the text column so it reads as part of what
// it follows.
func (p *Printer) Notef(format string, a ...any) { p.line(RoleNone, sevNone, format, a...) }

// Askf writes a question that waits for input on the same line ("› ", ascii "> ", plain none).
func (p *Printer) Askf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	p.live = ""
	p.flushSection()
	body := fmt.Sprintf(format, a...)
	if p.Plain {
		fmt.Fprint(p.W, body+" ")
		return
	}
	prefix := "› "
	switch p.Tier {
	case GlyphsASCII:
		prefix = "> "
	case GlyphsOff:
		prefix = strings.Repeat(" ", textCol)
	}
	fmt.Fprint(p.W, ANSI(ColorBrand)+prefix+ANSIReset+ANSIBold+body+ANSIReset+" ")
}

// Rolef writes a line in an explicitly chosen role, for the few callers that
// decide a role from data rather than at the call site.
func (p *Printer) Rolef(r Role, format string, a ...any) { p.line(r, sevNone, format, a...) }

// Hostf reports something about the host, the platform, or the operator's own
// boundary — a keychain, a loopback endpoint, a relay, a mounted session.
func (p *Printer) Hostf(format string, a ...any) { p.line(RoleHost, sevNone, format, a...) }

// Appf reports something about proveo's own services and images.
func (p *Printer) Appf(format string, a ...any) { p.line(RoleApp, sevNone, format, a...) }

// Asyncf reports background work: a retry, a queue, a scheduled step.
func (p *Printer) Asyncf(format string, a ...any) { p.line(RoleAsync, sevNone, format, a...) }

// Cloudf reports a crossing to an external system — a registry, a vendor.
func (p *Printer) Cloudf(format string, a ...any) { p.line(RoleCloud, sevNone, format, a...) }

// Storef reports persistence: paths on disk, transcripts, records, clones.
func (p *Printer) Storef(format string, a ...any) { p.line(RoleStore, sevNone, format, a...) }

func Okf(format string, a ...any) { Default.Okf(format, a...) }

func Warnf(format string, a ...any) { Default.Warnf(format, a...) }

func Failf(format string, a ...any) { Default.Failf(format, a...) }

func Notef(format string, a ...any) { Default.Notef(format, a...) }

func Askf(format string, a ...any) { Default.Askf(format, a...) }

func Rolef(r Role, format string, a ...any) { Default.Rolef(r, format, a...) }

func Section(label string) { Default.Section(label) }

// WithdrawSection withdraws Default's heading declared and not yet drawn.
func WithdrawSection() { Default.Section("") }

// Live draws Default's live line; see Printer.Live.
func Live(r Role, format string, a ...any) { Default.Live(r, format, a...) }

// EndLive erases Default's live line.
func EndLive() { Default.EndLive() }

// Fancy reports whether Default draws on a terminal: colour, glyphs, and a live line.
func Fancy() bool { return !Default.Plain }

func Hostf(format string, a ...any) { Default.Hostf(format, a...) }

func Appf(format string, a ...any) { Default.Appf(format, a...) }

func Asyncf(format string, a ...any) { Default.Asyncf(format, a...) }

func Cloudf(format string, a ...any) { Default.Cloudf(format, a...) }

func Storef(format string, a ...any) { Default.Storef(format, a...) }

const fallbackWidth = 100

func wrapFor(w io.Writer) io.Writer {
	f, ok := w.(*os.File)
	if !ok {
		return w
	}
	fd := int(f.Fd())
	return &wrapWriter{w: w, width: func() int {
		cols, _, err := term.GetSize(fd)
		if err != nil || cols <= 0 {
			return fallbackWidth
		}
		return cols
	}}
}

type wrapWriter struct {
	w     io.Writer
	width func() int
}

func (t *wrapWriter) cols() int { return t.width() }

func (t *wrapWriter) Write(p []byte) (int, error) { return t.writeHang(p, textCol) }

// writeHang writes p wrapped to the terminal, each continuation indented to column hang.
func (t *wrapWriter) writeHang(p []byte, hang int) (int, error) {
	if _, err := t.w.Write(wrapLines(p, t.width(), hang)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// hangWriter wraps a write at a chosen column; the terminal side of a Printer implements it.
type hangWriter interface {
	writeHang(p []byte, hang int) (int, error)
}

func wrapLines(p []byte, width, hang int) []byte {
	if width <= hang+8 {
		return p // too narrow for a hanging indent to help; leave it alone
	}
	var out bytes.Buffer
	lines := bytes.Split(p, []byte("\n"))
	for i, ln := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(wrapOne(ln, width, hang))
	}
	return out.Bytes()
}

func wrapOne(ln []byte, width, hang int) []byte {
	if displayWidth(ln) <= width {
		return ln
	}
	pad := strings.Repeat(" ", hang)
	var out bytes.Buffer
	col, wordStart, lineHasWord := 0, -1, false
	var gap []byte
	flushWord := func(end int) {
		word := ln[wordStart:end]
		w, sep := displayWidth(word), len(gap)
		switch {
		case !lineHasWord:
		case col+sep+w <= width, w > width-hang && col+sep < width:
			out.Write(gap)
			col += sep
		default:
			out.WriteString("\n" + pad + activeSGR(out.Bytes()))
			col = hang
		}
		if col+w > width {
			col = breakWord(&out, word, col, width, pad)
		} else {
			out.Write(word)
			col += w
		}
		lineHasWord, wordStart, gap = true, -1, nil
	}
	for i := 0; i < len(ln); i++ {
		if ln[i] == ' ' || ln[i] == '\t' {
			if wordStart >= 0 {
				flushWord(i)
			}
			if !lineHasWord {
				out.WriteByte(ln[i])
				col++
			} else {
				gap = append(gap, ln[i])
			}
			continue
		}
		if wordStart < 0 {
			wordStart = i
		}
	}
	if wordStart >= 0 {
		flushWord(len(ln))
	}
	return out.Bytes()
}

// breakWord writes a word wider than the line, breaking it at the width.
func breakWord(out *bytes.Buffer, word []byte, col, width int, pad string) int {
	s := string(word)
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			out.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if col+rw > width {
			out.WriteString("\n" + pad + activeSGR(out.Bytes()))
			col = len(pad)
		}
		out.WriteString(s[i : i+size])
		col += rw
		i += size
	}
	return col
}

// activeSGR is the colour state b leaves open: every SGR since its last reset, re-emitted after an inserted line break.
func activeSGR(b []byte) string {
	var active strings.Builder
	s := string(b)
	for i := strings.Index(s, "\x1b["); i >= 0; i = strings.Index(s, "\x1b[") {
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			break
		}
		if seq := s[i : i+j+1]; seq == "\x1b[0m" || seq == "\x1b[m" {
			active.Reset()
		} else {
			active.WriteString(seq)
		}
		s = s[i+j+1:]
	}
	return active.String()
}

func displayWidth(b []byte) int {
	w, s := 0, string(b)
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm' itself
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w += runewidth.RuneWidth(r)
		i += size
	}
	return w
}

const (
	ColorApp   = 0x005F7F // first-party app / runtime service — teal
	ColorAsync = 0xCBDB2A // queue · scheduler · background — lime
	ColorHost  = 0x00BAC6 // host / platform / operator boundary — cyan
	ColorCloud = 0x585858 // external SaaS / vendor — slate
	ColorDB    = 0xE5E4E4 // persistence / state store — light
	ColorError = 0xCB2000 // destructive · failure · security-sensitive — red
)

const (
	ColorSuccess = 0x009532 // passed · resolved · healthy — green
	ColorFailure = ColorError
)

const (
	ColorBrand     = ColorHost  // the mark and any selected/active element
	ColorAccent    = ColorApp   // first-party emphasis
	ColorWarn      = ColorAsync // attention, not yet a failure
	ColorFail      = ColorError
	ColorSecondary = ColorDB    // supporting text — light enough to read on dark terminals
	ColorMuted     = 0x8A8A8A   // stream detail — grey that reads on dark and light
	ColorRule      = ColorCloud // stream divider rules
	ColorSurface   = 0x0E3A48   // a filled face: chips and idle buttons — dark teal
)

func ANSI(rgb int) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", (rgb>>16)&0xFF, (rgb>>8)&0xFF, rgb&0xFF)
}

const ANSIReset = "\033[0m"

const (
	ANSIBold = "\033[1m"
	ANSIDim  = "\033[2m"
)

// BrandBanner is the Proveo Solutions box art from the legacy bash help.sh.
const BrandBanner = `        ┌───────●                        ───────┐
        │                                       │
        │      pr●veo                           │
        │                S O L U T I O N S      │
        │                                       │
        └───────                         ●──────┘`

// BrandTagline is the one-line product blurb under the banner.
const BrandTagline = "proveo/sandbox: portable, safe AI coding agents"

func WriteBrandBanner(w io.Writer) {
	p := New(w)
	fmt.Fprintln(w)
	if !p.Plain {
		fmt.Fprint(w, ANSIBold+ANSI(ColorBrand))
	}
	fmt.Fprintln(w, BrandBanner)
	if !p.Plain {
		fmt.Fprint(w, ANSIReset)
	}
	if p.Plain {
		fmt.Fprintf(w, "  %s\n\n", BrandTagline)
	} else {
		fmt.Fprintf(w, "  "+ANSI(ColorSecondary)+"%s"+ANSIReset+"\n\n", BrandTagline)
	}
}
