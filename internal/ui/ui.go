package ui

import (
	"fmt"
	"io"
	"os"
)

// Printer writes prefixed status lines to W. Plain swaps the emoji prefixes
// for text tags; New sets it from the writer and environment.
type Printer struct {
	W     io.Writer
	Plain bool
}

// New returns a Printer for w. Plain mode is on unless w is a terminal and
// neither TERM=dumb nor NO_COLOR is set.
func New(w io.Writer) *Printer {
	return &Printer{W: w, Plain: !isFancy(w)}
}

// Default is the process-wide status printer (stderr).
var Default = New(os.Stderr)

func TeeTo(w io.Writer) {
	if w == nil {
		return
	}
	term := Default
	Default = &Printer{W: multi{term: term.W, log: w}, Plain: term.Plain}
	logOnly = &Printer{W: w, Plain: true}
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

// multi writes to the terminal with its styling intact and to the log stripped of
// escape sequences.
type multi struct{ term, log io.Writer }

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

func (p *Printer) line(icon, tag, format string, a ...any) {
	prefix := icon
	if p.Plain {
		prefix = tag
	}
	fmt.Fprintf(p.W, prefix+format+"\n", a...)
}

// Okf reports a success ("✓ ", plain "ok: ").
func (p *Printer) Okf(format string, a ...any) { p.line("✓ ", "ok: ", format, a...) }

// Warnf reports a non-fatal problem ("⚠️  ", plain "warn: ").
func (p *Printer) Warnf(format string, a ...any) { p.line("⚠️  ", "warn: ", format, a...) }

// Failf reports an error ("❌ ", plain "error: ").
func (p *Printer) Failf(format string, a ...any) { p.line("❌ ", "error: ", format, a...) }

// Notef writes an informational line with no prefix in either mode.
func (p *Printer) Notef(format string, a ...any) { p.line("", "", format, a...) }

// Iconf writes a line decorated with a caller-chosen icon (e.g. 📂, 🔑, 🚀);
// in plain mode the icon is dropped, not replaced.
func (p *Printer) Iconf(icon, format string, a ...any) { p.line(icon+" ", "", format, a...) }

// Package-level helpers write via Default (stderr).

// Okf reports a success on Default.
func Okf(format string, a ...any) { Default.Okf(format, a...) }

// Warnf reports a non-fatal problem on Default.
func Warnf(format string, a ...any) { Default.Warnf(format, a...) }

// Failf reports an error on Default.
func Failf(format string, a ...any) { Default.Failf(format, a...) }

// Notef writes an informational line on Default.
func Notef(format string, a ...any) { Default.Notef(format, a...) }

// Iconf writes an icon-decorated line on Default.
func Iconf(icon, format string, a ...any) { Default.Iconf(icon, format, a...) }

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
