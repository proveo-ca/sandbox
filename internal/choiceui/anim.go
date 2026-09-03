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

// ticker is the strip's clock: motion is bound to CHANGE rather than to time,
// and posted as an event because PollEvent blocks.
type ticker struct {
	// base is the monotonic origin and since the current window's offset from it.
	// Durations off ONE reading, never wall-clock instants.
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
				// The window just closed. One last post so the strip repaints
				// WITHOUT its pulse.
				moving = false
			default:
				continue // at rest, and already still: post nothing
			}
			// A failed post means a full queue or a screen already gone. Either
			// way the next tick will do just as well, so it is dropped.
			_ = post(tcell.NewEventInterrupt(nil))
		}
	}
}

// bump restarts the animation window, once per KEY EVENT rather than per
// mutation. It posts nothing itself.
func (t *ticker) bump() { t.since.Store(int64(time.Since(t.base))) }

// frame is how many frames into the current window we are, or 0 at rest.
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

// stop ends the clock and WAITS for its goroutine to leave. Idempotent and safe
// from any goroutine: it is reached from the normal return and from a panic.
func (t *ticker) stop() {
	t.once.Do(func() { close(t.done) })
	<-t.stopped
}
