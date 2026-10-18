// SPEC: _spec/internal/envreg/env-registry.puml, _spec/cmd/proveo/usage.puml
package main

import (
	"io"

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
		var vars []envreg.Var
		col := helpDescCol
		for _, v := range envreg.Vars {
			if v.Scope == scope {
				vars = append(vars, v)
				col = max(col, helpIndent+runewidth.StringWidth(v.Name)+3)
			}
		}
		h.heading(scope.String())
		for _, v := range vars {
			text := v.Doc
			if v.Default != "" {
				text += " (default " + v.Default + ")"
			}
			h.row(v.Name, text, col)
		}
		h.line("")
	}
}
