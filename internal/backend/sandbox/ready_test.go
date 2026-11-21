package sandbox

import "testing"

// stubBackend swaps the two probes Ready reads; the Ready tests are not parallel: they hold package state.
func stubBackend(available func() (bool, string), ensure func(func(string) bool) (bool, string)) (restore func()) {
	avail, ensured := sbxAvailable, ensureBackend
	sbxAvailable, ensureBackend = available, ensure
	return func() { sbxAvailable, ensureBackend = avail, ensured }
}

func TestPrintReadyWithoutTheBackend(t *testing.T) {
	defer stubBackend(
		func() (bool, string) { return false, "sbx CLI not found on PATH" },
		func(func(string) bool) (bool, string) { return false, "Ensure must not run under --print" },
	)()
	ok, why := Ready(true, nil)
	if !ok || why != "sbx CLI not found on PATH" {
		t.Errorf("Ready(--print) = (%v, %q); want (true, the availability reason)", ok, why)
	}
}

func TestPrintReadyWithTheBackendStillPasses(t *testing.T) {
	defer stubBackend(
		func() (bool, string) { return true, "" },
		func(func(string) bool) (bool, string) { return false, "Ensure must not run under --print" },
	)()
	if ok, _ := Ready(true, nil); !ok {
		t.Error("Ready(--print) with sbx present = false; want true")
	}
}

func TestALiveRunStillRefusesWithoutTheBackend(t *testing.T) {
	defer stubBackend(
		func() (bool, string) { return false, "sbx CLI not found on PATH" },
		func(func(string) bool) (bool, string) { return false, "sbx CLI not found on PATH" },
	)()
	if ok, why := Ready(false, nil); ok || why != "sbx CLI not found on PATH" {
		t.Errorf("Ready(live) = (%v, %q); want (false, the availability reason)", ok, why)
	}
}
