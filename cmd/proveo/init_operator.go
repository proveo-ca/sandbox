// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"cmp"
	"os"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/operator"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/ui"
)

const operatorQuestion = "Greetings, how may I address you?"

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

// operatorStage is `proveo init`'s last screen: the name agents address the operator by. The
// name it sets, or reads when it cannot ask, becomes the operator block's fact.
func operatorStage(o initOptions, at choiceui.Step, rep *initReport) (stepResult, error) {
	root := proveohome.Root(os.Getenv)
	current, err := operator.Load(root)
	if err != nil {
		rep.operatorItem(sandbox.Item{Warn: true, Text: ui.Tilde(operator.Path(root)) + " — unreadable", Detail: errLines(err)})
	}
	if o.printOnly || o.yes || !interactiveTerminal() {
		rep.setOperator(cmp.Or(current, operator.Default), rep.howTo("`proveo init` from a terminal changes it"))
		return stepNone, nil
	}
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  operatorQuestion,
		Header: []string{"House rules tell every agent how to address you, in every sandbox this host runs."},
		Glyphs: posture.GlyphModeFrom(os.Getenv),
		NoAxis: true,
		Step:   at,
		Skip:   "Skip",
		Rows:   []choiceui.Row{operatorRow(current)},
	}
	out, err := form.RunStep()
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
