// SPEC: _spec/internal/promptkit/prompt-kit.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

// nerdSample is a Nerd Font glyph (the branch mark); a terminal without the font draws a box.
const nerdSample = ""

// AskDrawsGlyphs shows one Nerd Font glyph and asks what the terminal drew. asked is
// false when no terminal opens; the safe answer is that it drew a box.
func AskDrawsGlyphs() (icon, asked bool) {
	answer, asked := askDialog(drawsGlyphsDialog())
	return asked && answer == 1, asked
}

func drawsGlyphsDialog() dialog {
	const title = "what does your terminal draw here?"
	return newDialog(title, []string{
		title,
		"",
		"      " + nerdSample + "  main",
		"",
		"A branch icon means the terminal uses a Nerd Font.",
		"An empty box or a question mark means it does not.",
		"A font being installed is not the terminal using it.",
	}, 0,
		dialogButton{Label: "Empty box", Key: 'e'},
		dialogButton{Label: "Icon", Key: 'i'},
	)
}
