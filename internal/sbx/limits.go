package sbx

import (
	"os"
	"strconv"
	"strings"
)

// MemoryLimit is EMPTY unless the operator asked for a number, and empty means
// sbx sizes the sandbox itself — half the HOST, its own factory default.
//
// It used to derive one unconditionally, and the derivation halved the wrong
// number. `docker info MemTotal` reports the Docker VM's share, NOT the host's
// (23.5 GiB of a 48 GiB machine), so halving it again handed each sandbox about
// a QUARTER of the host where sbx's own default gives half. On a machine
// running nested containers inside the guest — every def with
// `com.docker.sandboxes.start-docker` — that ceiling is reached, and a guest
// with no swap does not slow down when it gets there: it OOM-kills.
//
// So proveo now declares a share only when the operator declares an instance
// count to divide by. Pre-diagnosing the number was worth less than the
// headroom it cost. CPULimit is deliberately unchanged; see its own note and
// _spec/minimum_requirements.puml on why the two knobs are not symmetrical.
// SPEC: _spec/minimum_requirements.puml
func MemoryLimit() string {
	if b, ok := parseMemorySize(os.Getenv(EnvMemory)); ok {
		return formatMemoryLimit(b)
	}
	n, ok := parseCount(os.Getenv(EnvInstances))
	if !ok {
		return ""
	}
	out, err := sh.DockerMemTotal()
	if err != nil {
		return ""
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || total <= 0 {
		return ""
	}
	return formatMemoryLimit(total / int64(n))
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
