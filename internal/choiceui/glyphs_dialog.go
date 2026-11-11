// SPEC: _spec/internal/promptkit/prompt-kit.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

// The branch and the lock that starship's nerd-font-symbols preset draws.
const (
	nerdBranch = ""
	nerdLock   = "\U000f033e"
)

// AskDrawsGlyphs shows two Nerd Font glyphs and asks what the terminal drew. asked is
// false when no terminal opens; the safe answer is that it drew something else.
func AskDrawsGlyphs() (icon, asked bool) {
	answer, asked := askDialog(drawsGlyphsDialog())
	return asked && answer == 1, asked
}

func drawsGlyphsDialog() dialog {
	const title = "what does your terminal draw here?"
	return newDialog(title, []string{
		title,
		"",
		"      " + nerdBranch + "  main      " + nerdLock + "  read-only",
		"",
		"Two icons mean the terminal uses a Nerd Font.",
		"Anything else means it does not: a box, a question",
		"mark, a line, a blank, or one icon of the two.",
		"A font being installed is not the terminal using it.",
	}, 0,
		dialogButton{Label: "Something else", Key: 's'},
		dialogButton{Label: "Two icons", Key: 't'},
	)
}
