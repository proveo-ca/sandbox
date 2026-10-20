// SPEC: _spec/internal/envreg/env-registry.puml, _spec/cmd/proveo/usage.puml
package main

import (
	"io"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/envreg"
)

func envHelpCmd() *cobra.Command {
	c := &cobra.Command{Use: "env", Short: "The PROVEO_* environment variables, their defaults and effects"}
	c.SetHelpFunc(func(cmd *cobra.Command, _ []string) { renderEnvHelp(cmd.OutOrStdout()) })
	return c
}

// renderEnvHelp draws the operator and maintainer rows of envreg.Vars, one heading per scope.
func renderEnvHelp(w io.Writer) {
	h := newHelpPen(w)
	h.line(h.title("proveo help env — " + envHelpCmd().Short))
	h.line("")
	for _, scope := range []envreg.Scope{envreg.Operator, envreg.Maintainer} {
		vars := envreg.ByScope(scope)
		col := helpDescCol
		for _, v := range vars {
			col = max(col, helpIndent+runewidth.StringWidth(v.Name)+3)
		}
		h.heading(scope.String())
		for _, v := range vars {
			text := v.Doc + readerNote(v.Where)
			if v.Default != "" {
				text += " (default " + v.Default + ")"
			}
			h.row(v.Name, text, col)
		}
		h.line("")
	}
}

// readerNote names the readers of a variable other than the proveo binaries, e.g. " · read in the sandbox".
func readerNote(w envreg.Where) string {
	var at []string
	if w&envreg.Image != 0 {
		at = append(at, "in the sandbox")
	}
	if w&envreg.Installer != 0 {
		at = append(at, "by the install scripts")
	}
	if len(at) == 0 {
		return ""
	}
	verb := " · read "
	if w&envreg.Binary != 0 {
		verb = " · also read "
	}
	return verb + strings.Join(at, " and ")
}
