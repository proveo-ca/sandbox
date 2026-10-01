// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"os"

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

// operatorStage is `proveo init`'s last stage: the name agents address the operator by.
func operatorStage(o initOptions) error {
	root := proveohome.Root(os.Getenv)
	current, err := operator.Load(root)
	if err != nil {
		ui.Warnf("could not read %s: %v", operator.Path(root), err)
	}
	ui.Section(ui.SectionOperator)
	if o.printOnly || o.yes || !interactiveTerminal() {
		name := current
		if name == "" {
			name = operator.Default
		}
		ui.Notef("agents address you as %q — run `proveo init` from a shell to change it", name)
		return nil
	}
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  operatorQuestion,
		Header: []string{"House rules tell every agent how to address you, in every sandbox this host runs."},
		Glyphs: posture.GlyphModeFrom(os.Getenv),
		NoAxis: true,
		Rows:   []choiceui.Row{operatorRow(current)},
	}
	ok, err := form.Run()
	if err != nil || !ok {
		return err
	}
	name, err := applyOperatorAnswer(root, current, form.FieldValue("name"))
	if err != nil {
		return err
	}
	ui.Okf("agents will address you as %q", name)
	return nil
}
