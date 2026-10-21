// SPEC: _spec/internal/sbx/clone-workspace.puml
package choiceui

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
)

// FetchPushedCopy is the post-exit question for branches a run pushed.
func FetchPushedCopy(branches []string, wait time.Duration) []string {
	shown := branches
	extra := ""
	if len(shown) > 8 {
		extra = fmt.Sprintf("+%d more", len(shown)-8)
		shown = shown[:8]
	}
	lines := []string{"the agent pushed these branches", ""}
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

func fetchPushedChoices() []modalChoice {
	return []modalChoice{
		{Key: "y/N", Text: "git fetch them into this repository"},
		{Key: "esc", Text: "leave them on the remote"},
	}
}

// ConfirmFetchPushed waits for permission to fetch. Silence returns false.
func ConfirmFetchPushed(s tcell.Screen, branches []string, wait time.Duration) bool {
	if wait <= 0 {
		wait = 15 * time.Second
	}
	var closed atomic.Bool
	timer := time.AfterFunc(wait, func() {
		if !closed.Load() {
			_ = s.PostEvent(tcell.NewEventInterrupt(nil))
		}
	})
	defer func() {
		closed.Store(true)
		timer.Stop()
	}()
	lines := FetchPushedCopy(branches, wait)
	title := "the agent pushed these branches"
	input, sig := readModal(s, lines, title, fetchPushedChoices())
	return sig == modalEnter && modalYes(input)
}
