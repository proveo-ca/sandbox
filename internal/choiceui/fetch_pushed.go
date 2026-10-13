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
	lines = append(lines,
		"",
		"y  git fetch them into this repository",
		"n / esc  leave them on the remote",
		fmt.Sprintf("%ds with no answer leaves them", secs),
	)
	return lines
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
	for {
		drawPrompt(s, lines, title)
		switch ev := s.PollEvent().(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventInterrupt:
			return false
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyEnter, tcell.KeyCtrlC:
				return false
			case tcell.KeyRune:
				switch ev.Rune() {
				case 'y', 'Y':
					return true
				case 'n', 'N':
					return false
				}
			}
		}
	}
}
