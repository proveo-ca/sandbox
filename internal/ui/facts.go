// SPEC: _spec/internal/ui/output-vocabulary.puml, _spec/_plans/output-readability.puml
package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Fact is one row of a Facts block: a role dot, "key:", a value, and dim detail lines under the value.
type Fact struct {
	Role   Role
	Key    string
	Value  string
	Detail []string
}

// Facts prints rows as one block whose values start in one column. A row with
// no Key stacks its Value under the row above; a row with neither a Value nor
// Detail prints nothing.
func (p *Printer) Facts(rows ...Fact) {
	if !slices.ContainsFunc(rows, func(r Fact) bool { return r.Value != "" || len(r.Detail) > 0 }) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	p.flushSection()
	lead := textCol
	if p.Plain {
		lead = 0
	}
	keyW := 0
	for _, r := range rows {
		if r.Key != "" && (r.Value != "" || len(r.Detail) > 0) {
			keyW = max(keyW, textWidthOf(r.Key)+1)
		}
	}
	valCol := lead + keyW + 1
	for _, r := range rows {
		if r.Value == "" && len(r.Detail) == 0 {
			continue
		}
		key := ""
		if r.Key != "" {
			key = r.Key + ":"
		}
		gap := strings.Repeat(" ", valCol-lead-textWidthOf(key))
		under := strings.Repeat(" ", valCol)
		if p.Plain {
			fmt.Fprint(p.W, key+gap+r.Value+"\n")
			for _, d := range r.Detail {
				fmt.Fprint(p.W, under+d+"\n")
			}
			continue
		}
		prefix := strings.Repeat(" ", textCol)
		if r.Key != "" {
			prefix = mark(p.Tier, sevNone, r.Role)
			if c, ok := r.Role.color(); ok {
				prefix = ANSI(c) + prefix + ANSIReset
			}
		}
		if r.Value != "" {
			p.writeHang(prefix+grey(key+gap+r.Value)+"\n", valCol)
		}
		for _, d := range r.Detail {
			p.writeHang(under+tint(ColorRule, d)+"\n", valCol)
		}
	}
}

// Detailf writes a dim line under the item above it, two columns past the text column.
func (p *Printer) Detailf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	p.flushSection()
	in := strings.Repeat(" ", textCol+2)
	body := fmt.Sprintf(format, a...)
	if p.Plain {
		fmt.Fprint(p.W, in+body+"\n")
		return
	}
	p.writeHang(in+tint(ColorRule, body)+"\n", textCol+2)
}

func Facts(rows ...Fact) { Default.Facts(rows...) }

func Detailf(format string, a ...any) { Default.Detailf(format, a...) }

// writeHang writes s, wrapping each continuation at column hang on a terminal.
func (p *Printer) writeHang(s string, hang int) {
	if hw, ok := p.W.(hangWriter); ok {
		_, _ = hw.writeHang([]byte(s), hang)
		return
	}
	_, _ = io.WriteString(p.W, s)
}

// tint colours s with rgb, re-opening the colour after any reset inside it.
func tint(rgb int, s string) string {
	if s == "" {
		return s
	}
	on := ANSI(rgb)
	return on + strings.ReplaceAll(s, ANSIReset, ANSIReset+on) + ANSIReset
}

// Tilde writes a path under the home directory as "~/…"; any other path comes back as it was.
func Tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return path
}

// TildeIn writes s as Tilde does, and each path under the home directory that starts inside s, such as in an error's text, as "~/…".
func TildeIn(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	if s == home {
		return "~"
	}
	sep := regexp.QuoteMeta(string(os.PathSeparator))
	at := regexp.MustCompile(`(^|[^\w.~` + sep + `-])` + regexp.QuoteMeta(home) + sep)
	return at.ReplaceAllString(s, "${1}~"+string(os.PathSeparator))
}
