// SPEC: _spec/internal/runner/hardened-run-argv.puml
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tier minimums: a host whose ceiling is below these cannot safely run that
// sandbox profile. EnsurePidsCapability failfasts before docker run.
const (
	MinPidsBase    = 512
	MinPidsBrowser = 1024
)

// Override floor: PROVEO_PIDS_LIMIT cannot go below this (still always capped).
const pidsOverrideFloor = 256

// Last-resort fallback when neither /proc nor a Docker engine probe yields
// kernel.pid_max (matches the historic Linux default).
const pidMaxFallback = 32768

const pidMaxDarwinFallback = 4194304

// goos is overridable in tests.
var goos = runtime.GOOS

// Max local images to try when probing Docker for pid_max (never pulls).
const dockerPidMaxImageTries = 8

const dockerPidMaxProbeTimeout = 8 * time.Second

// ErrInsufficientPidsCapability is returned when the host (or override) cannot
// meet the minimum pids budget for the selected tier.
var ErrInsufficientPidsCapability = errors.New("insufficient host pids capability")

// HostInfo is the host capacity used to scale the agent --pids-limit.
// Tests inject values; production uses DetectHost.
type HostInfo struct {
	CPUs   int // effective CPUs (affinity / cgroup-aware)
	PidMax int // kernel.pid_max, or pidMaxFallback
}

func DetectHost(preferImages ...string) HostInfo {
	cpus := runtime.NumCPU()
	if q := cgroupCPUQuota(); q > 0 && q < cpus {
		cpus = q
	}
	if cpus < 1 {
		cpus = 1
	}
	pidMax := readPidMax(preferImages...)
	if pidMax < 1 {
		pidMax = pidMaxFallback
	}
	return HostInfo{CPUs: cpus, PidMax: pidMax}
}

// HostCeiling is the max pids one sandbox may take on this host:
// min(cpus*1024, pidMax/64).
func HostCeiling(h HostInfo) int {
	cpus := h.CPUs
	if cpus < 1 {
		cpus = 1
	}
	pidMax := h.PidMax
	if pidMax < 1 {
		pidMax = pidMaxFallback
	}
	byCPU := cpus * 1024
	byPid := pidMax / 64
	if byCPU < byPid {
		return byCPU
	}
	return byPid
}

// IsBrowserImage reports whether the image ref is a *-browser variant.
func IsBrowserImage(image string) bool {
	return strings.Contains(image, "-browser")
}

// ParsePidsOverride parses PROVEO_PIDS_LIMIT. ok is false when unset or invalid.
func ParsePidsOverride(s string) (n int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// MinPidsLimit is the minimum --pids-limit for the tier (base or browser).
func MinPidsLimit(browser bool) int {
	if browser {
		return MinPidsBrowser
	}
	return MinPidsBase
}

// EnsurePidsCapability failfasts when the host ceiling (or a set override)
// cannot meet MinPidsLimit for the tier. Call before ResolvePidsLimit.
func EnsurePidsCapability(h HostInfo, browser bool, override int, overrideSet bool) error {
	min := MinPidsLimit(browser)
	tier := "base"
	if browser {
		tier = "browser"
	}
	ceiling := HostCeiling(h)
	if ceiling < min {
		return fmt.Errorf("%w: host ceiling %d < minimum %d for %s sandbox (cpus=%d pid_max=%d)",
			ErrInsufficientPidsCapability, ceiling, min, tier, h.CPUs, h.PidMax)
	}
	if overrideSet {
		resolved := clamp(override, pidsOverrideFloor, ceiling)
		if resolved < min {
			return fmt.Errorf("%w: PROVEO_PIDS_LIMIT=%d resolves to %d, below minimum %d for %s sandbox (ceiling %d)",
				ErrInsufficientPidsCapability, override, resolved, min, tier, ceiling)
		}
	}
	return nil
}

func ResolvePidsLimit(h HostInfo, browser bool, override int, overrideSet bool) int {
	ceiling := HostCeiling(h)
	if overrideSet {
		return clamp(override, pidsOverrideFloor, ceiling)
	}
	cpus := h.CPUs
	if cpus < 1 {
		cpus = 1
	}
	if browser {
		return clamp(cpus*512, MinPidsBrowser, ceiling)
	}
	return clamp(cpus*256, MinPidsBase, ceiling)
}

func clamp(n, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// cgroupCPUQuota returns the effective CPU count from cgroup quota, or 0 if
// unlimited / unavailable. Prefers cgroup v2 cpu.max, then v1 cfs quota/period.
func cgroupCPUQuota() int {
	if n := parseCPUMax(readFileTrim("/sys/fs/cgroup/cpu.max")); n > 0 {
		return n
	}
	quota := parseIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period := parseIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quota > 0 && period > 0 {
		return (quota + period - 1) / period // ceil
	}
	return 0
}

// parseCPUMax parses cgroup v2 "cpu.max" ("max 100000" or "200000 100000").
func parseCPUMax(s string) int {
	fields := strings.Fields(s)
	if len(fields) < 2 || fields[0] == "max" {
		return 0
	}
	quota, err1 := strconv.Atoi(fields[0])
	period, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || quota < 1 || period < 1 {
		return 0
	}
	return (quota + period - 1) / period
}

func readPidMax(preferImages ...string) int {
	if n := parseIntFile("/proc/sys/kernel/pid_max"); n > 0 {
		return n
	}
	if n := readPidMaxDocker(preferImages...); n > 0 {
		return n
	}
	if goos == "darwin" {
		return pidMaxDarwinFallback
	}
	return 0
}

// readPidMaxDocker is the non-/proc path; overridden in tests.
var readPidMaxDocker = cachedDockerPidMax

var (
	dockerPidMaxOnce sync.Once
	dockerPidMaxVal  int
)

func cachedDockerPidMax(preferImages ...string) int {
	dockerPidMaxOnce.Do(func() {
		dockerPidMaxVal = probeDockerPidMax(preferImages...)
	})
	return dockerPidMaxVal
}

func probeDockerPidMax(preferImages ...string) int {
	if _, err := exec.LookPath("docker"); err != nil {
		return 0
	}
	seen := make(map[string]struct{})
	var candidates []string
	for _, img := range preferImages {
		img = strings.TrimSpace(img)
		if img == "" {
			continue
		}
		if _, ok := seen[img]; ok {
			continue
		}
		seen[img] = struct{}{}
		candidates = append(candidates, img)
	}
	for _, id := range localDockerImageIDs(dockerPidMaxImageTries) {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}
	for _, ref := range candidates {
		if n := pidMaxFromDockerImage(ref); n > 0 {
			return n
		}
	}
	return 0
}

func localDockerImageIDs(limit int) []string {
	if limit < 1 {
		return nil
	}
	out, err := exec.Command("docker", "images", "-q").Output()
	if err != nil {
		return nil
	}
	return parseDockerImageIDs(string(out), limit)
}

func parseDockerImageIDs(out string, limit int) []string {
	if limit < 1 {
		return nil
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= limit {
			break
		}
	}
	return ids
}

func pidMaxFromDockerImage(image string) int {
	ctx, cancel := context.WithTimeout(context.Background(), dockerPidMaxProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never",
		"--network=none", "--entrypoint", "cat", image, "/proc/sys/kernel/pid_max")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	return parsePidMaxOutput(string(out))
}

func parsePidMaxOutput(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func parseIntFile(path string) int {
	s := readFileTrim(path)
	if s == "" {
		return 0
	}
	return parsePidMaxOutput(s)
}

func readFileTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
