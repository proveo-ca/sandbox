//go:build e2e

// SPEC: _spec/minimum_requirements.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func intEnv(t *testing.T, key string, def int) int {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s=%q is not a positive count", key, v)
	}
	return n
}

func hostMemory(t *testing.T) (free, swapUsed int64, ok bool) {
	t.Helper()
	page := int64(16384)
	if out, err := exec.Command("sysctl", "-n", "hw.pagesize").Output(); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil && n > 0 {
			page = n
		}
	}
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, 0, false
	}
	var pagesFree int64
	for _, line := range strings.Split(string(out), "\n") {
		name, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(val), "."), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Pages free", "Pages inactive", "Pages speculative":
			pagesFree += n
		}
	}
	if sw, err := exec.Command("sysctl", "-n", "vm.swapusage").Output(); err == nil {
		if _, rest, found := strings.Cut(string(sw), "used = "); found {
			if mb, err := strconv.ParseFloat(strings.TrimSuffix(strings.Fields(rest)[0], "M"), 64); err == nil {
				swapUsed = int64(mb * (1 << 20))
			}
		}
	}
	return pagesFree * page, swapUsed, true
}

func TestHostCapacityUpperBound(t *testing.T) {
	if os.Getenv("PROVEO_CAPACITY_TEST") != "1" {
		t.Skip("set PROVEO_CAPACITY_TEST=1 to run the capacity probe (it deliberately exhausts the host)")
	}
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx unavailable: %s", why)
	}
	image := harnessImage(t, "claudecode")

	ceiling := intEnv(t, "PROVEO_CAPACITY_MAX", 12)
	settle := durationEnv(t, "PROVEO_CAPACITY_SETTLE", 20*time.Second)
	work := t.TempDir()

	mem, cpus := sbx.MemoryLimit(), sbx.CPULimit()
	t.Logf("per-sandbox share: -m %q --cpus %d (ceiling %d)", mem, cpus, ceiling)

	var live []string
	t.Cleanup(func() {
		for i := len(live) - 1; i >= 0; i-- {
			_ = exec.Command(sbx.Binary, sbx.RemoveArgs(live[i])...).Run()
		}
	})

	baseFree, baseSwap, readable := hostMemory(t)
	if readable {
		t.Logf("baseline: %.1f GiB free, %.1f GiB swap used",
			float64(baseFree)/(1<<30), float64(baseSwap)/(1<<30))
	}

	for n := 1; n <= ceiling; n++ {
		name := fmt.Sprintf("proveo-capacity-%d-%d", os.Getpid(), n)
		cfg := sbx.RunConfig{
			Name: name, Agent: "shell", Image: image, Memory: mem, CPUs: cpus,
			Mounts: []sbx.Mount{{Host: work}},
		}
		start := time.Now()
		out, err := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...).CombinedOutput()
		if err != nil {
			t.Logf("host refused sandbox #%d after %s: %v\n%s", n, time.Since(start).Round(time.Second), err, lastLines(string(out), 15))
			t.Logf("UPPER BOUND: %d concurrent sandboxes at -m %q --cpus %d", n-1, mem, cpus)
			return
		}
		live = append(live, name)

		if !sbx.Running(name) {
			boot, err := exec.Command(sbx.Binary, "exec", name, "--", "true").CombinedOutput()
			if err != nil {
				t.Logf("sandbox #%d was created but would not start after %s: %v\n%s",
					n, time.Since(start).Round(time.Second), err, lastLines(string(boot), 15))
				t.Logf("UPPER BOUND: %d concurrent sandboxes at -m %q --cpus %d", n-1, mem, cpus)
				return
			}
		}
		if !sbx.Running(name) {
			t.Logf("sandbox #%d never reached running", n)
			t.Logf("UPPER BOUND: %d concurrent sandboxes at -m %q --cpus %d", n-1, mem, cpus)
			return
		}
		time.Sleep(settle)

		free, swap, ok := hostMemory(t)
		if !ok {
			t.Logf("#%d up (%s) — host memory unreadable", n, time.Since(start).Round(time.Second))
			continue
		}
		t.Logf("#%d up (%s) — %.1f GiB free, %.1f GiB swap used (+%.1f GiB swap since baseline)",
			n, time.Since(start).Round(time.Second),
			float64(free)/(1<<30), float64(swap)/(1<<30), float64(swap-baseSwap)/(1<<30))

		if swap-baseSwap > int64(2)<<30 {
			t.Logf("UPPER BOUND: %d concurrent sandboxes — the host began swapping at #%d, before sbx refused anything", n-1, n)
			return
		}
	}
	t.Logf("UPPER BOUND: not reached — %d sandboxes ran at -m %q --cpus %d without the host complaining; raise PROVEO_CAPACITY_MAX",
		ceiling, mem, cpus)
}
