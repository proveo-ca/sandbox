// SPEC: _spec/internal/choiceui/topology-strip.puml
package choiceui

import (
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

const animFrame = 120 * time.Millisecond // ~8 fps: motion, not a strobe

type ticker struct {
	base time.Time

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
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			_ = post(tcell.NewEventInterrupt(nil))
		}
	}
}

// frame advances for as long as the prompt is open.
// SPEC: _spec/internal/choiceui/topology-strip.puml
func (t *ticker) frame() int {
	return int(time.Since(t.base)/animFrame) + 1
}

func (t *ticker) stop() {
	t.once.Do(func() { close(t.done) })
	<-t.stopped
}
