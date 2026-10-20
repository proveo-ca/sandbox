// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	animFrame  = 120 * time.Millisecond // ~8 fps: motion, not a strobe
	flockFrame = time.Second / 60
	// flockStep is one flock frame measured in animFrame units.
	flockStep = float64(flockFrame) / float64(animFrame)
)

type ticker struct {
	base  time.Time
	every time.Duration

	pending atomic.Bool
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newTicker(post func(tcell.Event) error) *ticker {
	return newTickerEvery(post, animFrame)
}

func newTickerEvery(post func(tcell.Event) error, every time.Duration) *ticker {
	if every <= 0 {
		every = animFrame
	}
	t := &ticker{base: time.Now(), every: every, done: make(chan struct{}), stopped: make(chan struct{})}
	go t.run(post)
	return t
}

func (t *ticker) run(post func(tcell.Event) error) {
	defer close(t.stopped)
	tk := time.NewTicker(t.every)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			if !t.pending.CompareAndSwap(false, true) {
				continue
			}
			if post(tcell.NewEventInterrupt(nil)) != nil {
				t.pending.Store(false)
			}
		}
	}
}

// frame advances for as long as the prompt is open.
// Reading it releases the queued interrupt.
func (t *ticker) frame() int {
	t.pending.Store(false)
	return int(time.Since(t.base)/animFrame) + 1
}

// flockTick counts flock frames since the prompt opened.
func (t *ticker) flockTick() int {
	return int(time.Since(t.base)/flockFrame) + 1
}

func (t *ticker) stop() {
	t.once.Do(func() { close(t.done) })
	<-t.stopped
}
