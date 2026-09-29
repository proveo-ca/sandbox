// SPEC: _spec/cmd/proveo/usage.puml
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	helpIndent  = 2
	helpDescCol = helpIndent + 22
	helpRuleCol = 72
)

// helpPen paints help text in the choice prompt's palette, or leaves it plain.
type helpPen struct {
	w     io.Writer
	plain bool
	rule  string
	width int
}

func (h helpPen) paint(color int, bold, italic bool, s string) string {
	if h.plain || s == "" {
		return s
	}
	pre := ui.ANSI(color)
	if bold {
		pre += ui.ANSIBold
	}
	if italic {
		pre += "\033[3m"
	}
	return pre + s + ui.ANSIReset
}

func (h helpPen) title(s string) string  { return h.bold(s) }
func (h helpPen) accent(s string) string { return h.paint(ui.ColorAccent, true, false, s) }
func (h helpPen) body(s string) string   { return h.paint(ui.ColorSecondary, false, false, s) }
func (h helpPen) aside(s string) string  { return h.paint(ui.ColorSecondary, false, true, s) }
func (h helpPen) brand(s string) string  { return h.paint(ui.ColorBrand, false, false, s) }

func (h helpPen) bold(s string) string {
	if h.plain {
		return s
	}
	return ui.ANSIBold + s + ui.ANSIReset
}

func (h helpPen) line(s string) { fmt.Fprintln(h.w, s) }

func (h helpPen) heading(label string) {
	label = " " + label + " "
	pad := max(0, (helpRuleCol-runewidth.StringWidth(label))/2)
	rule := strings.Repeat(h.rule, 6)
	h.line(strings.Repeat(" ", helpIndent+pad) + h.body(rule+label+rule))
}

// row draws a label at the body indent and its text from col, wrapped under col.
func (h helpPen) row(label, text string, col int) {
	pad := max(1, col-helpIndent-runewidth.StringWidth(label))
	lines := wrapHelp(text, h.width-col)
	first := ""
	if len(lines) > 0 {
		first = lines[0]
	}
	h.line(strings.Repeat(" ", helpIndent) + h.accent(label) + strings.Repeat(" ", pad) + h.describe(first))
	for _, l := range lines[min(1, len(lines)):] {
		h.line(strings.Repeat(" ", col) + h.describe(l))
	}
}

// describe paints body text with a trailing "(default …)" as an aside.
func (h helpPen) describe(s string) string {
	if i := strings.LastIndex(s, " (default "); i >= 0 && strings.HasSuffix(s, ")") {
		return h.body(s[:i]) + " " + h.aside(s[i+1:])
	}
	return h.body(s)
}

var (
	yamlKey    = regexp.MustCompile(`^(\s*(?:- )?)([\w.-]+)(:)(.*)$`)
	yamlScalar = regexp.MustCompile(`"[^"]*"|'[^']*'|[^\s\[\],{}]+`)
)

// yamlLine highlights one YAML line: key accent, scalars brand, punctuation body, comment aside.
func (h helpPen) yamlLine(l string) string {
	code, comment := l, ""
	if i := strings.Index(l, " #"); i >= 0 {
		code, comment = l[:i], l[i:]
	}
	var b strings.Builder
	rest := code
	if m := yamlKey.FindStringSubmatch(code); m != nil {
		b.WriteString(h.body(m[1]) + h.accent(m[2]) + h.body(m[3]))
		rest = m[4]
	}
	last := 0
	for _, loc := range yamlScalar.FindAllStringIndex(rest, -1) {
		b.WriteString(h.body(rest[last:loc[0]]) + h.brand(rest[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(h.body(rest[last:]))
	if comment != "" {
		pad := len(comment) - len(strings.TrimLeft(comment, " "))
		b.WriteString(comment[:pad] + h.aside(comment[pad:]))
	}
	return b.String()
}

func wrapHelp(text string, width int) []string {
	width = max(width, 20)
	var out []string
	cur := ""
	for w := range strings.FieldsSeq(text) {
		switch {
		case cur == "":
			cur = w
		case runewidth.StringWidth(cur+" "+w) > width:
			out = append(out, cur)
			cur = w
		default:
			cur += " " + w
		}
	}
	if cur != "" || len(out) == 0 {
		out = append(out, cur)
	}
	return out
}

func helpWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols >= 40 {
			return min(cols, 120)
		}
	}
	return 100
}

// renderHelp draws a command's help in the choice prompt's layout: title, header, headed bodies, hint.
func renderHelp(w io.Writer, cmd *cobra.Command) {
	p := ui.New(w)
	h := helpPen{w: w, plain: p.Plain, rule: "─", width: helpWidth(w)}
	if p.Plain || p.Tier != ui.GlyphsNerd {
		h.rule = "-"
	}
	if !cmd.HasParent() {
		ui.WriteBrandBanner(w)
	}

	if cmd.HasParent() {
		title := cmd.CommandPath()
		if cmd.Short != "" {
			title += " — " + cmd.Short
		}
		h.line(h.title(title))
		h.line("")
	}

	usage := []string{}
	if cmd.Runnable() {
		usage = append(usage, cmd.UseLine())
	}
	if cmd.HasAvailableSubCommands() {
		usage = append(usage, cmd.CommandPath()+" <command>")
	}
	header := [][2]string{}
	for i, u := range usage {
		label := ""
		if i == 0 {
			label = "usage:"
		}
		header = append(header, [2]string{label, u})
	}
	if len(cmd.Aliases) > 0 {
		header = append(header, [2]string{"aliases:", strings.Join(cmd.Aliases, ", ")})
	}
	for _, kv := range header {
		h.line(strings.Repeat(" ", helpIndent) + h.accent(fmt.Sprintf("%-9s", kv[0])) + " " + h.body(kv[1]))
	}
	h.line("")

	if long := strings.TrimSpace(cmd.Long); long != "" {
		yaml := false
		for l := range strings.SplitSeq(long, "\n") {
			if strings.TrimSpace(l) == "" {
				h.line("")
				yaml = false
				continue
			}
			indented := strings.HasPrefix(l, "  ")
			yaml = indented && (yaml || yamlKey.MatchString(l))
			if yaml {
				h.line(strings.Repeat(" ", helpIndent) + h.yamlLine(l))
				continue
			}
			h.line(strings.Repeat(" ", helpIndent) + h.body(l))
		}
		h.line("")
	}

	if ex := strings.TrimSpace(cmd.Example); ex != "" {
		h.heading("examples")
		for l := range strings.SplitSeq(ex, "\n") {
			h.line(strings.Repeat(" ", helpIndent+2) + h.body(strings.TrimSpace(l)))
		}
		h.line("")
	}

	var subs []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			subs = append(subs, c)
		}
	}
	if len(subs) > 0 {
		h.heading("commands")
		col := helpDescCol
		for _, c := range subs {
			col = max(col, helpIndent+runewidth.StringWidth(c.Name())+3)
		}
		for _, c := range subs {
			h.row(c.Name(), c.Short, col)
		}
		h.line("")
	}

	flagSection(h, "flags", cmd.LocalFlags())
	flagSection(h, "global flags", cmd.InheritedFlags())

	if len(subs) > 0 {
		h.line(h.body(fmt.Sprintf("%s <command> --help · more on one command", cmd.CommandPath())))
	}
}

func flagSection(h helpPen, heading string, fs *pflag.FlagSet) {
	type flagRow struct{ label, text string }
	var rows []flagRow
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		label := "    --" + f.Name
		if f.Shorthand != "" && f.ShorthandDeprecated == "" {
			label = "-" + f.Shorthand + ", --" + f.Name
		}
		name, usage := pflag.UnquoteUsage(f)
		if name != "" && f.Value.Type() != "bool" {
			label += " " + name
		}
		if d := f.DefValue; d != "" && d != "false" && d != "[]" && d != "0" && !strings.Contains(usage, "(default") {
			usage += fmt.Sprintf(" (default %s)", d)
		}
		rows = append(rows, flagRow{label, usage})
	})
	if len(rows) == 0 {
		return
	}
	h.heading(heading)
	col := helpDescCol
	for _, r := range rows {
		col = max(col, helpIndent+runewidth.StringWidth(r.label)+3)
	}
	for _, r := range rows {
		h.row(r.label, r.text, col)
	}
	h.line("")
}
