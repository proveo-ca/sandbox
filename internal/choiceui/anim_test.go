package choiceui

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestTickerRunsContinuously(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	posted := 0
	tk := newTicker(func(tcell.Event) error {
		mu.Lock()
		posted++
		mu.Unlock()
		return nil
	})
	defer tk.stop()

	if got := tk.frame(); got == 0 {
		t.Error("a fresh ticker must already be running; nothing bumps it any more")
	}
	time.Sleep(4 * animFrame)
	mu.Lock()
	got := posted
	mu.Unlock()
	if got == 0 {
		t.Error("the ticker posted nothing; the strip would never redraw")
	}
}

// The frame number is what positions the mote on its path, so it has to keep
// advancing rather than settle or wrap to zero.
func TestTickerFrameKeepsAdvancing(t *testing.T) {
	t.Parallel()
	tk := newTicker(func(tcell.Event) error { return nil })
	defer tk.stop()

	first := tk.frame()
	time.Sleep(3 * animFrame)
	second := tk.frame()
	if second <= first {
		t.Errorf("frame went %d → %d; the mote would stand still", first, second)
	}
	if first == 0 || second == 0 {
		t.Errorf("frame must never be 0 while the prompt is open (%d, %d)", first, second)
	}
}

// stop is reached from the normal return AND from a panic unwinding through
// Run's defers, so it has to survive being called twice.
func TestTickerStopIsIdempotent(t *testing.T) {
	t.Parallel()
	tk := newTicker(func(tcell.Event) error { return nil })
	tk.stop()
	tk.stop()
}

// A post that fails — a full queue, or a screen already torn down — is dropped
// rather than retried or parked on. The next tick will do just as well.
func TestTickerSurvivesAFailingPost(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	tries := 0
	tk := newTicker(func(tcell.Event) error {
		mu.Lock()
		tries++
		mu.Unlock()
		return tcell.ErrEventQFull
	})
	defer tk.stop()
	time.Sleep(4 * animFrame)
	// frame() alone is arithmetic on a field and would pass even if the
	// goroutine had died; count the posts to prove it is still trying.
	mu.Lock()
	got := tries
	mu.Unlock()
	if got < 2 {
		t.Errorf("the ticker gave up after %d failed posts; it must keep going", got)
	}
	if tk.frame() == 0 {
		t.Error("a failing post must not stop the clock")
	}
}

func TestTickerUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	tk := newTicker(func(tcell.Event) error { return nil })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				_ = tk.frame()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); tk.stop() }()
	}
	wg.Wait()
	tk.stop()
}

func TestTickerLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		tk := newTicker(func(tcell.Event) error { return nil })
		tk.stop()
	}
	// The goroutines exit asynchronously, so give them a moment before counting.
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("20 stopped tickers left %d goroutines behind", after-before)
	}
}
