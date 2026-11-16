// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"cmp"
	"os"
	"slices"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/ui"
)

// operatorRow is the single field the operator stage asks.
func operatorRow(current string) choiceui.Row {
	shown := current
	if shown == "" {
		shown = proveohome.DefaultOperator
	}
	return choiceui.Row{
		Label:       "name",
		Field:       true,
		Held:        current != "",
		Placeholder: shown,
		Reason:      "empty keeps " + shown + "; the first 16 characters are kept",
	}
}

// applyOperatorAnswer stores a typed answer; an empty one keeps what is stored, else the default.
func applyOperatorAnswer(root, current, typed string) (string, error) {
	if proveohome.SanitizeOperator(typed) == "" {
		if current != "" {
			return current, nil
		}
		return proveohome.DefaultOperator, nil
	}
	return proveohome.SaveOperator(root, typed)
}

// operatorStage is `proveo init`'s last page: the name agents address the operator by, then sbx
// setup's row as the probe left it. The name it sets, or reads when it cannot ask, becomes the
// result's operator row; the setup row's answer is whether sbx setup opens after the result.
func operatorStage(o initOptions, at choiceui.Step, rep *initReport, page cardPage, setup choiceui.Row) (stepResult, error) {
	root := proveohome.Root(os.Getenv)
	current, err := proveohome.LoadOperator(root)
	if err != nil {
		rep.operatorItem(sandbox.Item{Warn: true, Text: ui.Tilde(proveohome.OperatorPath(root)) + " — unreadable", Detail: errLines(err)})
	}
	if o.printOnly || o.yes || !viewOr(page.view).card() {
		rep.setOperator(operatorSaid{name: cmp.Or(current, proveohome.DefaultOperator), defaulted: current == "",
			howTo: rep.howTo("`proveo init` from a terminal changes it")})
		return stepNone, nil
	}
	form := operatorForm(at, current, page.done, setup)
	out, err := viewOr(page.view).Ask(form)
	if err == nil && out != choiceui.Back && out != choiceui.Quit {
		rep.wizard = form.Selection(rowWizard) == setupAfter
	}
	switch {
	case err != nil || out == choiceui.Cancelled:
		return stepNext, stopAt(labelOperator, err)
	case out == choiceui.Quit:
		return stepQuit, nil
	}
	if out == choiceui.Back {
		return stepBack, nil
	}
	typed := form.FieldValue("name")
	name, err := applyOperatorAnswer(root, current, typed)
	if err != nil {
		return stepNext, stopAt(labelOperator, err)
	}
	rep.setOperator(operatorSaid{name: name, asked: true, defaulted: current == "" && proveohome.SanitizeOperator(typed) == ""})
	return stepNext, nil
}

// operatorForm is page 4, "Name the operator": the pages before it as done rows, the name field,
// and sbx setup as the last row ahead. Enter with the field empty keeps the name in use.
// SPEC: _spec/cmd/proveo/operator-name.puml
func operatorForm(at choiceui.Step, current string, done []choiceui.Row, setup choiceui.Row) *choiceui.Form {
	name := operatorRow(current)
	name.Divider, name.Heading = true, pageOperator
	setup.Divider, setup.Heading = true, aheadHeading
	f := initCard(at, "Name the operator", []string{"Agents in every sandbox on this host address you by this name."},
		slices.Concat(done, []choiceui.Row{name, setup}))
	f.EnterWord = func(*choiceui.Form) string { return "finish" }
	return f
}
