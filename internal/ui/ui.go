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

// GlyphTier is how much decoration the terminal is trusted with. It is a
// capability of the SESSION, so it is read once and constant for the run: a
// terminal offers no way to ask whether its font carries a codepoint, so this
// is the operator's declaration rather than a probe.
//
// One type, three consumers: internal/posture and internal/choiceui alias it,
// which is what makes the tier one decision instead of three.
type GlyphTier int

const (
	GlyphsNerd GlyphTier = iota // default
	GlyphsASCII
	GlyphsOff
)

// GlyphTierFrom reads PROVEO_GLYPHS through lookup, so a project .env can set
// it once per repo. Unset means nerd; an unrecognised value also means nerd
// rather than off, so a typo degrades to the default rather than silently
// stripping every mark in the run.
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
// A closed set, and the identity's own six: a component drawn <<host>> in
// _spec/ prints host-cyan here. The call site names a role; it never names a
// glyph, which is the whole difference from the icon-per-call-site vocabulary
// this replaced.
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

// sev is how a line stands: fine, worth attention, or broken. Independent of
// Role — Okf answers this and says nothing about which layer spoke — so the two
// are not one enumeration.
type sev int

const (
	sevNone sev = iota
	sevOk
	sevWarn
	sevFail
)

// textCol is the column body text starts at. A mark is one column plus one
// space in EVERY tier, so the text column is fixed: continuations and wrapped
// lines indent to it, and one status line reads as one thing.
const textCol = 2

// mark is the rune a line is prefixed with, padded to textCol. Severity is the
// mark and role is the colour, so neither is load-bearing alone: a line read
// with NO_COLOR still distinguishes ok from broken, and a line read in colour
// still says which layer it came from without being parsed.
//
// Every rune here is one column and none carries a variation selector. The
// U+FE0F that rode the old warn mark — U+26A0 followed by it — is the reason
// this stream could not measure,
// and so could not wrap, itself.
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
	// The node glyph, not ASCII punctuation: the mark is dots interrupting
	// rules, and the topology figure already draws a run that way.
	return "● "
}

// tag is the plain rendering's prefix. Severity degrades to a WORD so a
// transcript stays greppable — "grep -c 'warn:'" counts what a human saw on
// screen. A role has no word: it classifies a layer, not an event, and
// "host: image: …" reads worse than the fact alone.
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

// Printer writes marked status lines to W. Plain swaps the marks for text tags
// and drops colour; Tier says which rune set the marks come from. New sets both
// from the writer and the environment.
type Printer struct {
	W     io.Writer
	Plain bool
	Tier  GlyphTier

	mu sync.Mutex
	// pending is a declared section whose heading has not been drawn yet, and
	// shown is the last heading actually drawn. Two fields rather than one
	// because the whole point is that declaring a section and DRAWING it are
	// different events: a stage that reports nothing must contribute no divider.
	pending, shown string
}

// Section names the concern the following lines belong to, drawn as the divider
// choiceui uses for a Divider row so the narration and the form name the same
// things the same way.
//
// The heading is LAZY. Nearly every line in the prelude is conditional — no gh
// session, no symlinks, no broker — so a stage that declares a section and then
// finds nothing to say would leave a heading with nothing under it. Declaring is
// cheap and drawing is deferred to the first line that actually arrives, which
// means a caller never has to ask whether it is about to print something.
//
// Re-declaring the section already on screen is a no-op, so a stage may announce
// itself without checking whether the previous one did.
func (p *Printer) Section(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if label == p.shown {
		p.pending = ""
		return
	}
	p.pending = label
}

// sectionRule is the divider's rule character. choiceui draws Divider rows with
// its own glyphSet, so this follows the tier the same way.
func sectionRule(t GlyphTier, plain bool) string {
	if plain || t != GlyphsNerd {
		return "-"
	}
	return "─"
}

// sectionHeading is choiceui.drawBodyLine's lineHeading case adapted to a
// left-aligned stream: the same construction — rules, the label spaced, rules —
// in the same secondary colour, but anchored at the text column with the
// trailing rule run out to the measured width.
//
// choiceui CENTRES its heading on 72 columns, and that is right there: the rows
// under it are a column grid, so a centred label sits over the middle of the
// options it names. This stream is left-aligned prose. Centring the heading here
// floated it at column 32 above text starting at column 2, which read as a
// caption for nothing. Same vocabulary, different anchor, because the thing
// being labelled has a different shape.
func sectionHeading(t GlyphTier, plain bool, label string, width int) string {
	rule := sectionRule(t, plain)
	spaced := " " + label + " "
	head := strings.Repeat(" ", textCol) + strings.Repeat(rule, sectionRules) + spaced
	// An unmeasurable writer — a log, a pipe, CI — gets choiceui's own short form
	// rather than a rule run out to a guess. A hundred dashes per section is noise
	// in a transcript, and the width would be a constant pretending to be a
	// measurement.
	fill := sectionRules
	if width > 0 {
		if room := width - textWidthOf(head); room > fill {
			fill = room
		}
	}
	return head + strings.Repeat(rule, fill)
}

// The section vocabulary, closed the same way the roles are: a call site names
// one of these, never a string of its own. Five are the CHOICE FORM's own labels
// — egress, credentials, execution, interface are its rows and dividers — so the
// narration groups a run under the same names the operator was asked about.
//
// The rest name what the form has no row for. Four of them are the LIVE phase:
// a run reports its resolved posture, then starts, and "credentials" appearing
// twice would read as a repeat rather than as two different moments. So the
// second pass has its own names — starting, secrets, results — and every divider
// in a run appears exactly once.
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

// sectionRules is the six rule characters choiceui puts either side of a divider
// label, taken from drawBodyLine rather than re-chosen.
const sectionRules = 6

func textWidthOf(s string) int { return displayWidth([]byte(s)) }

// width is the columns the heading may run to. Asked of the writer, so a
// resize is picked up; anything that is not a measuring writer answers with the
// fallback rather than with a guess.
func (p *Printer) width() int {
	if w, ok := p.W.(interface{ cols() int }); ok {
		return w.cols()
	}
	return 0 // not measurable: the caller decides what to do without a width
}

// flushSection draws a pending heading. Called with p.mu held, from line().
func (p *Printer) flushSection() {
	if p.pending == "" || p.pending == p.shown {
		return
	}
	head := sectionHeading(p.Tier, p.Plain, p.pending, p.width())
	if !p.Plain {
		head = ANSI(ColorSecondary) + head + ANSIReset
	}
	// A blank above the divider and none below: the lines it names follow it
	// immediately, which is what makes it read as their heading rather than as a
	// rule between two groups.
	fmt.Fprint(p.W, "\n"+head+"\n")
	p.shown, p.pending = p.pending, ""
}

// New returns a Printer for w. Plain mode is on unless w is a terminal and
// neither TERM=dumb nor NO_COLOR is set. A terminal writer is wrapped so its
// lines are measured and broken on spaces rather than by the terminal, mid-word.
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

// TeeTo sends every status line to w as well as to the terminal. Wrapping stays
// on the TERMINAL side: the transcript keeps its long lines, so a grep over it
// does not depend on how wide the operator's window happened to be.
func TeeTo(w io.Writer) {
	if w == nil {
		return
	}
	term := Default
	term.mu.Lock()
	pending, shown := term.pending, term.shown
	term.mu.Unlock()
	// The section state crosses with the writer. TeeTo is called from Do() after
	// the first lines are already out, so dropping it here would redraw a heading
	// the operator has just read.
	Default = &Printer{
		W:       multi{term: term.W, log: w},
		Plain:   term.Plain,
		Tier:    term.Tier,
		pending: pending,
		shown:   shown,
	}
	logOnly = &Printer{W: w, Plain: true, Tier: term.Tier}
}

// logOnly writes to the transcript without touching the terminal. Nil until TeeTo.
var logOnly *Printer

// Logf records a line in the transcript only — for facts worth having when
// troubleshooting but too noisy to print on every run.
func Logf(format string, a ...any) {
	if logOnly != nil {
		logOnly.Notef(format, a...)
	}
}

// multi writes to the terminal with its styling intact and to the log stripped
// of escape sequences.
type multi struct{ term, log io.Writer }

// cols forwards the terminal's width through the tee: the log copy is not
// wrapped, but the divider it records is the one the operator saw.
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

// line is the ONE writer every verb goes through.
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

// sevColor is the theme's own severity pair plus the attention colour between
// them: green, lime, red. The glyph carries the shape and this carries the
// meaning, which is the whole reason no mark needs to be an emoji.
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

// ── severity: fine · worth attention · broken ──────────────────────────────

// Okf reports a success ("✓ ", ascii "+ ", plain "ok: ").
func (p *Printer) Okf(format string, a ...any) { p.line(RoleNone, sevOk, format, a...) }

// Warnf reports a non-fatal problem ("⚠ ", ascii "! ", plain "warn: ").
func (p *Printer) Warnf(format string, a ...any) { p.line(RoleNone, sevWarn, format, a...) }

// Failf reports an error ("× ", ascii "x ", plain "error: ").
func (p *Printer) Failf(format string, a ...any) { p.line(RoleNone, sevFail, format, a...) }

// Notef writes a continuation line under a preceding verb: no marker in either
// rendering, indented to the text column so it reads as part of what it follows.
func (p *Printer) Notef(format string, a ...any) { p.line(RoleNone, sevNone, format, a...) }

// ── role: which layer is speaking ──────────────────────────────────────────

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
// failed — a removal, a credential crossing a boundary. Red because of what it
// is; the role dot rather than "×" because nothing went wrong.
func (p *Printer) Dangerf(format string, a ...any) { p.line(RoleError, sevNone, format, a...) }

// Package-level helpers write via Default (stderr).

// Okf reports a success on Default.
func Okf(format string, a ...any) { Default.Okf(format, a...) }

// Warnf reports a non-fatal problem on Default.
func Warnf(format string, a ...any) { Default.Warnf(format, a...) }

// Failf reports an error on Default.
func Failf(format string, a ...any) { Default.Failf(format, a...) }

// Notef writes a continuation line on Default.
func Notef(format string, a ...any) { Default.Notef(format, a...) }

// Rolef writes a line in an explicitly chosen role on Default.
func Rolef(r Role, format string, a ...any) { Default.Rolef(r, format, a...) }

// Section names the concern the following lines on Default belong to.
func Section(label string) { Default.Section(label) }

// Hostf writes a host-role line on Default.
func Hostf(format string, a ...any) { Default.Hostf(format, a...) }

// Appf writes an app-role line on Default.
func Appf(format string, a ...any) { Default.Appf(format, a...) }

// Asyncf writes an async-role line on Default.
func Asyncf(format string, a ...any) { Default.Asyncf(format, a...) }

// Cloudf writes a cloud-role line on Default.
func Cloudf(format string, a ...any) { Default.Cloudf(format, a...) }

// Storef writes a store-role line on Default.
func Storef(format string, a ...any) { Default.Storef(format, a...) }

// Dangerf writes a destructive/security-sensitive line on Default.
func Dangerf(format string, a ...any) { Default.Dangerf(format, a...) }

// ── measuring, so the stream can wrap itself ───────────────────────────────

// fallbackWidth is what a writer that cannot be asked is assumed to be. Wide
// enough for the sentences this stream carries, narrow enough to be safe.
const fallbackWidth = 100

// wrapFor puts a measuring writer in front of a terminal. Width is asked per
// write rather than cached, so a resize mid-run is picked up on the next line.
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

// wrapWriter breaks whole lines on spaces to the terminal's width, indenting
// every continuation to the text column. It sits on the TERMINAL side only:
// the log copy keeps its long lines, so a transcript stays greppable whatever
// width the operator's window happened to be.
type wrapWriter struct {
	w     io.Writer
	width func() int
}

// cols answers Printer.width so a divider's rule runs to the real edge.
func (t *wrapWriter) cols() int { return t.width() }

func (t *wrapWriter) Write(p []byte) (int, error) {
	if _, err := t.w.Write(wrapLines(p, t.width())); err != nil {
		return 0, err
	}
	// The caller wrote p, whatever we made of it; reporting the wrapped length
	// would make an io.Writer that lies about how much it consumed.
	return len(p), nil
}

// wrapLines rewraps already-rendered bytes. It measures DISPLAY width and skips
// ANSI sequences while doing so, because a colour run costs bytes and no columns.
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

// wrapOne breaks one rendered line, preserving its own leading indent and
// adding the hanging indent at the text column.
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
			// Leading indent: kept verbatim, and it is the line's own, not ours.
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

// displayWidth is the columns b occupies once painted: ANSI runs cost nothing,
// and every other rune is measured rather than counted.
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

// The identity's severity pair, which the six ROLE colours do not carry: a role
// says which layer spoke, and neither of these is a layer. Mirrored from
// proveo.puml's $SUCCESS/$ERROR, where they are exposed as successLabel() and
// errorLabel() — the same red and green the diagrams in _spec/ are drawn with.
//
// This green was missing here, and "✓" was teal for want of it. Reaching for
// Reaching for U+2705 or U+274C instead — the green tick and red cross — is the
// mistake the glyph ban exists to prevent: an emoji bakes its colour into the
// rune, which costs a column count to buy a colour the theme already has.
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

// WriteBrandBanner writes the branding banner (+ tagline) to w.
// When fancy is true and w is a TTY-capable fancy printer, the banner is cyan/bold.
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
