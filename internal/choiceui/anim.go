// SPEC: _spec/internal/choiceui/topology-strip.puml
//
// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	animFrame  = 120 * time.Millisecond  // ~8 fps: motion, not a strobe
	animWindow = 1200 * time.Millisecond // how long a change stays animated
)

type ticker struct {
	base  time.Time
	since atomic.Int64 // ns from base, or 0 at rest

	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newTicker(post func(tcell.Event) error) *ticker {
	t := &ticker{base: time.Now(), done: make(chan struct{}), stopped: make(chan struct{})}
	go t.run(post)
	return t
}

func (t *ticker) run(post func(tcell.Event) error) {
	defer close(t.stopped)
	tk := time.NewTicker(animFrame)
	defer tk.Stop()
	moving := false
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			switch running := t.frame() != 0; {
			case running:
				moving = true
			case moving:
				moving = false
			default:
				continue // at rest, and already still: post nothing
			}
			_ = post(tcell.NewEventInterrupt(nil))
		}
	}
}

func (t *ticker) bump() { t.since.Store(int64(time.Since(t.base))) }

func (t *ticker) frame() int {
	at := t.since.Load()
	if at == 0 {
		return 0
	}
	elapsed := time.Since(t.base) - time.Duration(at)
	if elapsed < 0 || elapsed >= animWindow {
		return 0
	}
	return int(elapsed/animFrame) + 1
}

func (t *ticker) stop() {
	t.once.Do(func() { close(t.done) })
	<-t.stopped
}
