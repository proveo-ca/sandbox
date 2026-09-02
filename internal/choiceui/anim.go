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

// ticker is the strip's clock.
//
// Motion is bound to CHANGE rather than to time: a keystroke runs the filmstrip
// for animWindow and the strip then rests on its still frame. Any movement on
// screen therefore means something in the form actually moved, which is the only
// thing that makes the motion worth the distraction it costs.
//
// PollEvent blocks, so the clock cannot be a timer the draw loop reads; it has
// to be an event. PostEvent is non-blocking and reports a full queue rather than
// parking, so a tick that arrives against a screen nobody is reading is dropped
// instead of wedging the goroutine that sent it.
type ticker struct {
	// base is the monotonic origin, and since is the offset of the current
	// window from it. Both are durations off ONE reading, never wall-clock
	// instants: a wall clock stepped backwards by NTP makes an elapsed time
	// negative, and a negative elapsed never satisfies "the window has closed" —
	// so the strip would repaint every 120ms forever, with no pulse to show for it.
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
				// WITHOUT its pulse — otherwise the final frame painted is a
				// mid-animation one and the mote stays frozen on a lane forever.
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

// bump restarts the animation window. It is called once per key event rather
// than per mutation: the cursor changes the strip's emphasis and `move` does not
// report through OnChange, so hanging this off the individual mutations would
// leave the one case that matters uncovered.
//
// It posts nothing itself — the keystroke that triggered it already causes the
// draw loop to repaint.
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

// stop ends the clock and WAITS for its goroutine to leave, so that no post can
// still be in flight against a screen the caller is about to tear down. Run
// registers it after screen.Fini precisely so it runs first.
//
// Idempotent and safe from any goroutine, because it is reached both from the
// normal return and from a panic unwinding through Run's defers.
func (t *ticker) stop() {
	t.once.Do(func() { close(t.done) })
	<-t.stopped
}
