// SPEC: _spec/internal/sbx/clone-workspace.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"time"
)

// FetchPushedCopy is the post-exit question for branches a run pushed.
func FetchPushedCopy(branches []string, wait time.Duration) []string {
	shown := branches
	extra := ""
	if len(shown) > 8 {
		extra = fmt.Sprintf("+%d more", len(shown)-8)
		shown = shown[:8]
	}
	lines := []string{fetchPushedTitle, ""}
	lines = append(lines, shown...)
	if extra != "" {
		lines = append(lines, extra)
	}
	secs := int(wait.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	lines = append(lines, "", fmt.Sprintf("%ds with no answer leaves them", secs))
	return lines
}

const fetchPushedTitle = "the agent pushed these branches"

// AskFetchPushed asks on the operator's terminal whether to fetch the branches. Silence for
// wait, Esc, and a terminal that does not open all answer no.
func AskFetchPushed(branches []string, wait time.Duration) bool {
	if wait <= 0 {
		wait = 15 * time.Second
	}
	answer, asked := askDialog(fetchPushedDialog(branches, wait))
	return asked && answer == 1
}

func fetchPushedDialog(branches []string, wait time.Duration) dialog {
	d := newDialog(fetchPushedTitle, FetchPushedCopy(branches, wait), 0,
		dialogButton{Label: "Leave", Key: 'l'},
		dialogButton{Label: "Fetch", Key: 'f'},
	)
	d.wait = wait
	return d
}
