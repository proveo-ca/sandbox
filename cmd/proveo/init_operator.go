// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"cmp"
	"os"
	"slices"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/operator"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/ui"
)

// operatorRow is the single field the operator stage asks.
func operatorRow(current string) choiceui.Row {
	shown := current
	if shown == "" {
		shown = operator.Default
	}
	return choiceui.Row{
		Label:       "name",
		Field:       true,
		Held:        current != "",
		Placeholder: shown,
		Reason:      "agents address you this way; the first 16 characters are kept, empty keeps " + shown,
	}
}

// applyOperatorAnswer stores a typed answer; an empty one keeps what is stored, else the default.
func applyOperatorAnswer(root, current, typed string) (string, error) {
	if operator.Sanitize(typed) == "" {
		if current != "" {
			return current, nil
		}
		return operator.Default, nil
	}
	return operator.Save(root, typed)
}

// operatorStage is `proveo init`'s last page: the name agents address the operator by, then sbx
// setup's row as the probe left it. The name it sets, or reads when it cannot ask, becomes the
// operator block's fact; the row's answer is whether sbx setup opens after the result.
func operatorStage(o initOptions, at choiceui.Step, rep *initReport, page cardPage, setup choiceui.Row) (stepResult, error) {
	root := proveohome.Root(os.Getenv)
	current, err := operator.Load(root)
	if err != nil {
		rep.operatorItem(sandbox.Item{Warn: true, Text: ui.Tilde(operator.Path(root)) + " — unreadable", Detail: errLines(err)})
	}
	if o.printOnly || o.yes || !viewOr(page.view).card() {
		rep.setOperator(cmp.Or(current, operator.Default), rep.howTo("`proveo init` from a terminal changes it"))
		return stepNone, nil
	}
	form := operatorForm(at, current, page.done, setup)
	out, err := viewOr(page.view).Ask(form)
	if err == nil && out != choiceui.Back && out != choiceui.Quit {
		rep.wizard = form.Selection(rowWizard) == setupAfter
	}
	switch {
	case err != nil || out == choiceui.Cancelled || out == choiceui.Skipped:
		return stepNext, stopAt(labelOperator, err)
	case out == choiceui.Quit:
		return stepQuit, nil
	}
	if out == choiceui.Back {
		return stepBack, nil
	}
	name, err := applyOperatorAnswer(root, current, form.FieldValue("name"))
	if err != nil {
		return stepNext, stopAt(labelOperator, err)
	}
	rep.setOperator(name, nil)
	return stepNext, nil
}

// operatorForm is page 4, "Name the operator": the pages before it as done rows, the name field,
// the Skip button, and sbx setup as the last row ahead.
// SPEC: _spec/cmd/proveo/operator-name.puml
func operatorForm(at choiceui.Step, current string, done []choiceui.Row, setup choiceui.Row) *choiceui.Form {
	name := operatorRow(current)
	name.Divider, name.Heading = true, pageOperator
	setup.Divider, setup.Heading = true, aheadHeading
	f := initCard(at, "Name the operator", []string{"Agents in every sandbox on this host address you by this name."},
		slices.Concat(done, []choiceui.Row{name, setup}))
	f.Skip = "Skip"
	f.EnterWord = func(*choiceui.Form) string { return "finish" }
	return f
}
