// SPEC: _spec/internal/ui/output-vocabulary.puml,
// _spec/_conventions/tui-design-language.puml
//
// SPEC: _spec/internal/ui/output-vocabulary.puml, _spec/_conventions/tui-design-language.puml
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
)

// GlyphTier is how much decoration the terminal is trusted with.
type GlyphTier int

const (
	GlyphsNerd GlyphTier = iota // default
	GlyphsASCII
	GlyphsOff
)

func GlyphTierFrom(lookup func(string) string) GlyphTier {
	switch strings.ToLower(strings.TrimSpace(lookup("PROVEO_GLYPHS"))) {
	case "ascii":
		return GlyphsASCII
	case "off", "0", "false", "no", "none":
		return GlyphsOff
	}
	return GlyphsNerd
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
}

// Section names the concern the following lines belong to, drawn as the divider
// choiceui uses for a Divider row so the narration and the form name the same
// things the same way.
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
	if plain || t != GlyphsNerd {
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
	SectionScope       = "scope"       // the sub-tree of a monorepo this run sees
	SectionRun         = "run"         // images, the variant, the transcript path
	SectionCredentials = "credentials" // form row: where the login rests
	SectionWorkspace   = "workspace"   // mounts, symlinks, the proveo home
	SectionEgress      = "egress"      // form row: the tier and its caveats
	SectionExecution   = "execution"   // form divider: where the agent runs
	SectionInterface   = "interface"   // form divider: what the agent can drive
	SectionStarting    = "starting"    // live: handing the image over, retries
	SectionSecrets     = "secrets"     // live: injection, and the store's prompt
	SectionResults     = "results"     // after the agent: clones, transcripts
)

const sectionRules = 6

func textWidthOf(s string) int { return displayWidth([]byte(s)) }

func (p *Printer) width() int {
	if w, ok := p.W.(interface{ cols() int }); ok {
		return w.cols()
	}
	return 0 // not measurable: the caller decides what to do without a width
}

func (p *Printer) flushSection() {
	if p.pending == "" || p.pending == p.shown {
		return
	}
	head := sectionHeading(p.Tier, p.Plain, p.pending, p.width())
	if !p.Plain {
		head = ANSI(ColorSecondary) + head + ANSIReset
	}
	fmt.Fprint(p.W, "\n"+head+"\n")
	p.shown, p.pending = p.pending, ""
}

func New(w io.Writer) *Printer {
	fancy := isFancy(w)
	p := &Printer{W: w, Plain: !fancy, Tier: GlyphTierFrom(os.Getenv)}
	if fancy {
		p.W = wrapFor(w)
	}
	return p
}

// Default is the process-wide status printer (stderr).
var Default = New(os.Stderr)

func TeeTo(w io.Writer) {
	if w == nil {
		return
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
	}
	logOnly = &Printer{W: w, Plain: true, Tier: term.Tier}
}

var logOnly *Printer

func Logf(format string, a ...any) {
	if logOnly != nil {
		logOnly.Notef(format, a...)
	}
}

type multi struct{ term, log io.Writer }

func (m multi) cols() int {
	if w, ok := m.term.(interface{ cols() int }); ok {
		return w.cols()
	}
	return 0
}

func (m multi) Write(p []byte) (int, error) {
	n, err := m.term.Write(p)
	_, _ = m.log.Write(stripANSI(p))
	return n, err
}

func stripANSI(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == 0x1b {
			for i < len(p) && p[i] != 'm' {
				i++
			}
			continue
		}
		out = append(out, p[i])
	}
	return out
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
	p.flushSection()
	prefix := mark(p.Tier, s, r)
	if p.Plain {
		fmt.Fprintf(p.W, tag(s, r)+format+"\n", a...)
		return
	}
	body := fmt.Sprintf(format, a...)
	if c, ok := r.color(); ok && s == sevNone {
		prefix = ANSI(c) + prefix + ANSIReset
	}
	if c, ok := sevColor(s); ok {
		prefix = ANSI(c) + prefix + ANSIReset
	}
	fmt.Fprint(p.W, prefix+body+"\n")
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

// Notef writes a continuation line under a preceding verb: no marker in either
// rendering, indented to the text column so it reads as part of what it
// follows.
func (p *Printer) Notef(format string, a ...any) { p.line(RoleNone, sevNone, format, a...) }

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

// Dangerf reports something destructive or security-sensitive that has NOT
// failed — a removal, a credential crossing a boundary.
func (p *Printer) Dangerf(format string, a ...any) { p.line(RoleError, sevNone, format, a...) }

func Okf(format string, a ...any) { Default.Okf(format, a...) }

func Warnf(format string, a ...any) { Default.Warnf(format, a...) }

func Failf(format string, a ...any) { Default.Failf(format, a...) }

func Notef(format string, a ...any) { Default.Notef(format, a...) }

func Rolef(r Role, format string, a ...any) { Default.Rolef(r, format, a...) }

func Section(label string) { Default.Section(label) }

func Hostf(format string, a ...any) { Default.Hostf(format, a...) }

func Appf(format string, a ...any) { Default.Appf(format, a...) }

func Asyncf(format string, a ...any) { Default.Asyncf(format, a...) }

func Cloudf(format string, a ...any) { Default.Cloudf(format, a...) }

func Storef(format string, a ...any) { Default.Storef(format, a...) }

func Dangerf(format string, a ...any) { Default.Dangerf(format, a...) }

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

func (t *wrapWriter) Write(p []byte) (int, error) {
	if _, err := t.w.Write(wrapLines(p, t.width())); err != nil {
		return 0, err
	}
	return len(p), nil
}

func wrapLines(p []byte, width int) []byte {
	if width <= textCol+8 {
		return p // too narrow for a hanging indent to help; leave it alone
	}
	var out bytes.Buffer
	lines := bytes.Split(p, []byte("\n"))
	for i, ln := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(wrapOne(ln, width))
	}
	return out.Bytes()
}

func wrapOne(ln []byte, width int) []byte {
	if displayWidth(ln) <= width {
		return ln
	}
	pad := strings.Repeat(" ", textCol)
	var out bytes.Buffer
	col, wordStart, lineHasWord := 0, -1, false
	flushWord := func(end int) {
		word := ln[wordStart:end]
		w := displayWidth(word)
		if lineHasWord && col+1+w > width {
			out.WriteString("\n" + pad)
			col = textCol
		} else if lineHasWord {
			out.WriteByte(' ')
			col++
		}
		out.Write(word)
		col += w
		lineHasWord = true
		wordStart = -1
	}
	for i := 0; i < len(ln); i++ {
		if ln[i] == ' ' || ln[i] == '\t' {
			if wordStart >= 0 {
				flushWord(i)
				continue
			}
			if !lineHasWord {
				out.WriteByte(ln[i])
				col++
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

// SPEC: _spec/_conventions/spec-conventions.puml, _spec/internal/runlog/run-transcript.puml
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
	ColorSecondary = ColorDB // supporting text — light enough to read on dark terminals
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
