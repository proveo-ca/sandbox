package choiceui

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// The clock is silent until something changes, and silent again once the window
// closes: an idle prompt must cost nothing, or the animation is a tax on every
// operator who is only reading the form.
func TestTickerIsSilentAtRest(t *testing.T) {
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

	if got := tk.frame(); got != 0 {
		t.Errorf("a ticker nobody bumped must rest, got frame %d", got)
	}
	time.Sleep(3 * animFrame)
	mu.Lock()
	idle := posted
	mu.Unlock()
	if idle != 0 {
		t.Errorf("an idle ticker posted %d events; it must post none", idle)
	}

	tk.bump()
	if got := tk.frame(); got == 0 {
		t.Error("a bumped ticker must be running")
	}
	time.Sleep(3 * animFrame)
	mu.Lock()
	moving := posted
	mu.Unlock()
	if moving == 0 {
		t.Error("a bumped ticker posted nothing; the strip would never redraw")
	}
}

// Motion is bound to change, not to time: the window closes on its own.
func TestTickerWindowCloses(t *testing.T) {
	t.Parallel()
	tk := newTicker(func(tcell.Event) error { return nil })
	defer tk.stop()
	tk.bump()
	if tk.frame() == 0 {
		t.Fatal("bump must open the window")
	}
	time.Sleep(animWindow + 2*animFrame)
	if got := tk.frame(); got != 0 {
		t.Errorf("the window must close on its own, still at frame %d", got)
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
	tk.bump()
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
		t.Error("a failing post must not close the window")
	}
}

// The clock is touched from two goroutines by construction — the draw loop reads
// frame() and bumps, the ticker goroutine posts — so hammer both against stop()
// from several at once. Run this under -race; without it the test only proves
// nothing panics or deadlocks.
func TestTickerUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	tk := newTicker(func(tcell.Event) error { return nil })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				tk.bump()
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

// A ticker that is stopped must take its goroutine with it: the prompt is run
// once per process today, but a leaked goroutine posting into a dead screen is
// exactly the failure the stop ordering exists to prevent.
func TestTickerLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		tk := newTicker(func(tcell.Event) error { return nil })
		tk.bump()
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
