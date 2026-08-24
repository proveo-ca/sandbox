package runner

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestHostCeiling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		h    HostInfo
		want int
	}{
		{
			name: "16 CPU linux pid_max",
			h:    HostInfo{CPUs: 16, PidMax: 4194304},
			want: 16384, // min(16*1024, 4194304/64)
		},
		{
			name: "pid_max binds ceiling",
			h:    HostInfo{CPUs: 64, PidMax: 32768},
			want: 512, // min(65536, 32768/64)
		},
		{
			name: "zero cpus floored via Resolve path still ceiling uses 1",
			h:    HostInfo{CPUs: 0, PidMax: 4194304},
			want: 1024, // min(1*1024, …)
		},
		{
			name: "tiny pid_max yields sub-minimum ceiling",
			h:    HostInfo{CPUs: 8, PidMax: 4096},
			want: 64, // 4096/64
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HostCeiling(tc.h); got != tc.want {
				t.Errorf("HostCeiling(%+v) = %d, want %d", tc.h, got, tc.want)
			}
		})
	}
}

func TestMinPidsLimit(t *testing.T) {
	t.Parallel()
	if got := MinPidsLimit(false); got != MinPidsBase {
		t.Errorf("MinPidsLimit(false) = %d, want %d", got, MinPidsBase)
	}
	if got := MinPidsLimit(true); got != MinPidsBrowser {
		t.Errorf("MinPidsLimit(true) = %d, want %d", got, MinPidsBrowser)
	}
	if MinPidsBase >= MinPidsBrowser {
		t.Errorf("MinPidsBase (%d) must be < MinPidsBrowser (%d)", MinPidsBase, MinPidsBrowser)
	}
	if MinPidsBase <= pidsOverrideFloor {
		t.Errorf("MinPidsBase (%d) must exceed override floor (%d)", MinPidsBase, pidsOverrideFloor)
	}
}

func TestEnsurePidsCapability(t *testing.T) {
	t.Parallel()
	okHost := HostInfo{CPUs: 4, PidMax: 4194304}       // ceiling 4096
	tightHost := HostInfo{CPUs: 1, PidMax: 8192}       // ceiling 128
	browserOk := HostInfo{CPUs: 2, PidMax: 4194304}    // ceiling 2048
	browserTight := HostInfo{CPUs: 1, PidMax: 4194304} // ceiling 1024 — exact browser min

	tests := []struct {
		name        string
		h           HostInfo
		browser     bool
		override    int
		overrideSet bool
		wantErr     bool
	}{
		{name: "base ok", h: okHost, wantErr: false},
		{name: "browser ok", h: browserOk, browser: true, wantErr: false},
		{name: "browser at exact ceiling min", h: browserTight, browser: true, wantErr: false},
		{name: "base host ceiling too low", h: tightHost, wantErr: true},
		{name: "browser host ceiling too low for browser", h: HostInfo{CPUs: 1, PidMax: 32768}, browser: true, wantErr: true}, // ceiling 512
		{
			name: "override below base min", h: okHost,
			override: 100, overrideSet: true, wantErr: true, // clamps to 256 < 512
		},
		{
			name: "override meets base min", h: okHost,
			override: 512, overrideSet: true, wantErr: false,
		},
		{
			name: "override below browser min", h: browserOk, browser: true,
			override: 800, overrideSet: true, wantErr: true,
		},
		{
			name: "override meets browser min", h: browserOk, browser: true,
			override: 1024, overrideSet: true, wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := EnsurePidsCapability(tc.h, tc.browser, tc.override, tc.overrideSet)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("EnsurePidsCapability(%+v, browser=%v) = nil, want %v", tc.h, tc.browser, ErrInsufficientPidsCapability)
				}
				if !errors.Is(err, ErrInsufficientPidsCapability) {
					t.Errorf("EnsurePidsCapability(...) error = %v, want errors.Is(..., ErrInsufficientPidsCapability)", err)
				}
				return
			}
			if err != nil {
				t.Errorf("EnsurePidsCapability(%+v, browser=%v) = %v, want nil", tc.h, tc.browser, err)
			}
		})
	}
}

func TestResolvePidsLimit(t *testing.T) {
	t.Parallel()
	h16 := HostInfo{CPUs: 16, PidMax: 4194304} // ceiling 16384
	h2 := HostInfo{CPUs: 2, PidMax: 4194304}   // ceiling 2048
	h4 := HostInfo{CPUs: 4, PidMax: 4194304}   // ceiling 4096
	hCgroup := HostInfo{CPUs: 2, PidMax: 4194304}

	tests := []struct {
		name        string
		h           HostInfo
		browser     bool
		override    int
		overrideSet bool
		want        int
	}{
		{name: "base 16 cpu", h: h16, want: 4096}, // clamp(16*256, 512, 16384)
		{name: "browser 16 cpu", h: h16, browser: true, want: 8192},
		{name: "base 2 cpu floor", h: h2, want: 512}, // clamp(512, 512, 2048)
		{name: "browser 2 cpu floor", h: h2, browser: true, want: 1024},
		{name: "base 4 cpu", h: h4, want: 1024},
		{name: "browser 4 cpu", h: h4, browser: true, want: 2048},
		{name: "cgroup-reduced cpus", h: hCgroup, want: 512},
		{
			name: "override within range", h: h16,
			override: 2048, overrideSet: true, want: 2048,
		},
		{
			name: "override clamped to floor", h: h16,
			override: 10, overrideSet: true, want: 256,
		},
		{
			name: "override clamped to ceiling", h: h2,
			override: 99999, overrideSet: true, want: 2048,
		},
		{
			name: "override wins over browser tier", h: h16, browser: true,
			override: 3000, overrideSet: true, want: 3000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolvePidsLimit(tc.h, tc.browser, tc.override, tc.overrideSet)
			if got != tc.want {
				t.Errorf("ResolvePidsLimit(%+v, browser=%v, ov=%d/%v) = %d, want %d",
					tc.h, tc.browser, tc.override, tc.overrideSet, got, tc.want)
			}
			// When capability check would pass, resolved limit must meet the tier floor
			// (override-below-min cases are rejected by Ensure, not Resolve).
			if err := EnsurePidsCapability(tc.h, tc.browser, tc.override, tc.overrideSet); err == nil {
				min := MinPidsLimit(tc.browser)
				if got < min {
					t.Errorf("ResolvePidsLimit(...) = %d, below MinPidsLimit(%v)=%d after Ensure passed", got, tc.browser, min)
				}
				if got > HostCeiling(tc.h) {
					t.Errorf("ResolvePidsLimit(...) = %d, exceeds HostCeiling=%d", got, HostCeiling(tc.h))
				}
			}
		})
	}
}

func TestResolvePidsLimitMeetsMinWhenCapable(t *testing.T) {
	t.Parallel()
	for _, browser := range []bool{false, true} {
		h := HostInfo{CPUs: 8, PidMax: 4194304}
		if err := EnsurePidsCapability(h, browser, 0, false); err != nil {
			t.Fatalf("EnsurePidsCapability(%+v, %v) = %v", h, browser, err)
		}
		got := ResolvePidsLimit(h, browser, 0, false)
		min := MinPidsLimit(browser)
		if got < min {
			t.Errorf("ResolvePidsLimit(browser=%v) = %d, want >= %d", browser, got, min)
		}
	}
}

func TestIsBrowserImage(t *testing.T) {
	t.Parallel()
	if !IsBrowserImage("proveo/opencode-browser:latest") {
		t.Error("expected browser image")
	}
	if !IsBrowserImage("proveo/cursor-browser") {
		t.Error("expected browser image without tag")
	}
	if IsBrowserImage("proveo/opencode:latest") {
		t.Error("base image must not match")
	}
	if IsBrowserImage("proveo/claudecode-solo:latest") {
		t.Error("solo is not browser")
	}
}

func TestParsePidsOverride(t *testing.T) {
	t.Parallel()
	n, ok := ParsePidsOverride("4096")
	if !ok || n != 4096 {
		t.Errorf("ParsePidsOverride(4096) = %d,%v", n, ok)
	}
	if _, ok := ParsePidsOverride(""); ok {
		t.Error("empty must be unset")
	}
	if _, ok := ParsePidsOverride("nope"); ok {
		t.Error("invalid must be unset")
	}
	if _, ok := ParsePidsOverride("0"); ok {
		t.Error("zero must be unset")
	}
	if _, ok := ParsePidsOverride("-1"); ok {
		t.Error("negative must be unset")
	}
}

func TestParseCPUMax(t *testing.T) {
	t.Parallel()
	if got := parseCPUMax("max 100000"); got != 0 {
		t.Errorf("unlimited = %d, want 0", got)
	}
	if got := parseCPUMax("200000 100000"); got != 2 {
		t.Errorf("2 CPUs = %d, want 2", got)
	}
	if got := parseCPUMax("100000 100000"); got != 1 {
		t.Errorf("1 CPU = %d, want 1", got)
	}
	if got := parseCPUMax("150000 100000"); got != 2 { // ceil
		t.Errorf("1.5 CPUs ceil = %d, want 2", got)
	}
}

func TestDetectHostSanity(t *testing.T) {
	t.Parallel()
	h := DetectHost()
	if h.CPUs < 1 {
		t.Errorf("DetectHost().CPUs = %d, want >= 1", h.CPUs)
	}
	if h.PidMax < 1 {
		t.Errorf("DetectHost().PidMax = %d, want >= 1", h.PidMax)
	}
	if ceiling := HostCeiling(h); ceiling < 1 {
		t.Errorf("HostCeiling(DetectHost()) = %d, want >= 1", ceiling)
	}
}

func TestParsePidMaxOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"4194304\n", 4194304},
		{" 32768 ", 32768},
		{"0", 0},
		{"-1", 0},
		{"", 0},
		{"nope", 0},
	}
	for _, tc := range tests {
		if got := parsePidMaxOutput(tc.in); got != tc.want {
			t.Errorf("parsePidMaxOutput(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseDockerImageIDs(t *testing.T) {
	t.Parallel()
	out := "abc\nabc\ndef\n\nghi\n"
	got := parseDockerImageIDs(out, 2)
	want := []string{"abc", "def"}
	if len(got) != len(want) {
		t.Fatalf("parseDockerImageIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseDockerImageIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := parseDockerImageIDs(out, 0); got != nil {
		t.Errorf("limit 0 = %v, want nil", got)
	}
}

func TestReadPidMaxDockerFallback(t *testing.T) {
	// Serial: swaps package var. On Linux /proc wins; assert docker path is wired.
	orig := readPidMaxDocker
	t.Cleanup(func() { readPidMaxDocker = orig })

	readPidMaxDocker = func(...string) int { return 4194304 }
	got := readPidMax()
	if proc := parseIntFile("/proc/sys/kernel/pid_max"); proc > 0 {
		if got != proc {
			t.Errorf("readPidMax() = %d, want /proc value %d", got, proc)
		}
		return
	}
	if got != 4194304 {
		t.Errorf("readPidMax() without /proc = %d, want docker probe 4194304", got)
	}

	readPidMaxDocker = func(...string) int { return 0 }
	// No /proc and a failed probe leaves the platform default, which readPidMax
	// documents as the darwin constant on darwin and 0 everywhere else. Asserting
	// 0 unconditionally made this case pass only on Linux.
	want := 0
	if goos == "darwin" {
		want = pidMaxDarwinFallback
	}
	if got := readPidMax(); got != want {
		t.Errorf("readPidMax() with failed probe = %d, want %d", got, want)
	}
}

func TestReadPidMaxDarwinFallback(t *testing.T) {
	origProbe, origGoos := readPidMaxDocker, goos
	t.Cleanup(func() { readPidMaxDocker, goos = origProbe, origGoos })

	readPidMaxDocker = func(...string) int { return 0 }
	if proc := parseIntFile("/proc/sys/kernel/pid_max"); proc > 0 {
		t.Skip("/proc readable here; darwin fallback only reachable without it")
	}

	goos = "linux"
	if got := readPidMax(); got != 0 {
		t.Errorf("readPidMax() failed-probe linux = %d, want 0", got)
	}
	goos = "darwin"
	if got := readPidMax(); got != pidMaxDarwinFallback {
		t.Errorf("readPidMax() failed-probe darwin = %d, want %d", got, pidMaxDarwinFallback)
	}
}

func TestDarwinFallbackUnlocksBrowserTier(t *testing.T) {
	t.Parallel()
	h := HostInfo{CPUs: 2, PidMax: pidMaxDarwinFallback} // clean OrbStack/Docker Desktop mac shape
	if err := EnsurePidsCapability(h, true, 0, false); err != nil {
		t.Errorf("clean-mac browser tier must pass with darwin fallback: %v", err)
	}
}

func TestDockerRunArgsPidsPositive(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{Image: "proveo/opencode:latest", PidsLimit: MinPidsBase},
		{Image: "proveo/opencode-browser:latest", PidsLimit: MinPidsBrowser},
	} {
		argv := DockerRunArgs(cfg)
		joined := strings.Join(argv, " ")
		var saw bool
		for _, a := range argv {
			if !strings.HasPrefix(a, "--pids-limit=") {
				continue
			}
			saw = true
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--pids-limit="))
			if err != nil {
				t.Errorf("DockerRunArgs(%+v) pids flag %q: %v", cfg, a, err)
				continue
			}
			if n < 1 {
				t.Errorf("DockerRunArgs(%+v) --pids-limit=%d, want > 0", cfg, n)
			}
			if n != cfg.PidsLimit {
				t.Errorf("DockerRunArgs(%+v) --pids-limit=%d, want %d", cfg, n, cfg.PidsLimit)
			}
		}
		if !saw {
			t.Errorf("DockerRunArgs(%+v) missing --pids-limit in %s", cfg, joined)
		}
		for _, flag := range []string{"--cap-drop=ALL", "--security-opt=no-new-privileges:true"} {
			if !strings.Contains(joined, flag) {
				t.Errorf("DockerRunArgs(%+v) missing %q in %s", cfg, flag, joined)
			}
		}
	}
}

func TestHardeningIncludesResolvedPids(t *testing.T) {
	t.Parallel()
	got := Hardening(MinPidsBase)
	want := fmt.Sprintf("--pids-limit=%d", MinPidsBase)
	found := false
	for _, f := range got {
		if f == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Hardening(%d) = %v, missing %q", MinPidsBase, got, want)
	}
}
