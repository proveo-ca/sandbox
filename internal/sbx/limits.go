package sbx

import (
	"os"
	"strconv"
	"strings"
)

func MemoryLimit() string {
	if b, ok := parseMemorySize(os.Getenv(EnvMemory)); ok {
		return formatMemoryLimit(b)
	}
	out, err := sh.DockerMemTotal()
	if err != nil {
		return ""
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || total <= 0 {
		return ""
	}
	return formatMemoryLimit(total / int64(sandboxShare(os.Getenv(EnvInstances))))
}

func CPULimit() int {
	if n, ok := parseCount(os.Getenv(EnvCPUs)); ok {
		return clampCPUs(n)
	}
	n, ok := parseCount(os.Getenv(EnvInstances))
	if !ok || n < 2 {
		return 0
	}
	return clampCPUs(numCPU() / cpuBurstDivisor)
}

func clampCPUs(n int) int {
	if total := numCPU(); n > total {
		return total
	}
	if n < 1 {
		return 1
	}
	return n
}

func sandboxShare(v string) int {
	if n, ok := parseCount(v); ok {
		return n
	}
	return defaultMemoryShare
}

func parseCount(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > maxSandboxInstances {
		return 0, false
	}
	return n, true
}

func formatMemoryLimit(limit int64) string {
	if limit > maxSandboxMemory {
		limit = maxSandboxMemory
	}
	if limit < minSandboxMemory {
		return ""
	}
	return strconv.FormatInt(limit/(1<<20), 10) + "m"
}

func parseMemorySize(v string) (int64, bool) {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" {
		return 0, false
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "ib"), "b")
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			mult, s = 1<<10, s[:n-1]
		case 'm':
			mult, s = 1<<20, s[:n-1]
		case 'g':
			mult, s = 1<<30, s[:n-1]
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return 0, false
	}
	return n * mult, true
}

// Available reports whether the host can run the sbx backend, and if not, why.
// A too-old CLI is reported as unavailable rather than tried: falling back to
// docker+egress is a posture the operator can read, whereas a mid-run flag
// rejection is not.
