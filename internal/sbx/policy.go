package sbx

import (
	"encoding/json"
	"strings"
)

func PolicyLogArgs(sandbox string) []string {
	return []string{"policy", "log", sandbox, "--json"}
}

func CheckNetworkArgs(host string) []string {
	return []string{"policy", "check", "network", "--json", host}
}

func PolicyLog(sandbox string) ([]byte, error) { return sh.PolicyLog(sandbox) }

// MemoryEvidenceScript is what a teardown asks the guest about its own memory.
//
// A sandbox has NO SWAP (measured: SwapTotal 0; the guest kernel is static with
// CONFIG_ZRAM and CONFIG_ZSWAP off, docker/sbx-releases#447), so pressure does
// not degrade — it kills. The guest OOM killer names its victim in `dmesg` and
// nowhere else: there is no OOMKilled flag to read, and cgroup-v2 at the root
// reports nothing (memory.max, memory.current and memory.events all read
// empty). Repeated `drop_caches` from vminitd is the reclaim that precedes it.
//
// `docker ps` runs last and its failure is swallowed: a wedged daemon is itself
// a symptom, and must not cost us the meminfo and dmesg lines above it.
// SPEC: _spec/minimum_requirements.puml
const MemoryEvidenceScript = `echo "== meminfo =="
grep -E '^(MemTotal|MemFree|MemAvailable|SwapTotal|Committed_AS|Dirty)' /proc/meminfo 2>/dev/null
echo
echo "== oom kills and reclaim (dmesg) =="
dmesg 2>/dev/null | grep -iE 'out of memory|killed process|oom-kill|oom_reaper|drop_caches' | tail -40
echo
echo "== nested containers sharing this budget =="
timeout 5 docker ps --format '{{.Names}}  {{.Image}}' 2>/dev/null | head -20 || echo "(docker did not answer)"`

func MemoryEvidenceArgs(sandbox string) []string {
	return []string{"exec", "-w", "/", sandbox, "--", "bash", "-c", MemoryEvidenceScript}
}

// MemoryEvidence reads the guest's own account of its memory. It is BEST EFFORT
// by construction: at teardown the sandbox may already be gone, and a run that
// died of memory pressure is exactly the run least able to answer.
func MemoryEvidence(sandbox string) ([]byte, error) { return sh.MemoryEvidence(sandbox) }

// OOMEvidence reports whether the captured text names an actual kill, as
// opposed to the reclaim that merely precedes one. Only a kill is worth
// interrupting the operator for.
func OOMEvidence(out []byte) bool {
	s := strings.ToLower(string(out))
	for _, marker := range []string{"out of memory", "killed process", "oom-kill", "oom_reaper"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func Baselines() []string { return []string{BaselineAllowAll, BaselineBalanced, BaselineDenyAll} }

func InspectPolicyArgs() []string { return []string{"policy", "inspect", "local-policy"} }

func PolicyBaseline() (name string, known bool) {
	out, err := sh.InspectPolicy()
	if err != nil {
		return "", false
	}
	var allowAll, sawNetwork bool
	allows := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "network" {
			continue
		}
		sawNetwork = true
		if f[0] != "allow" {
			continue
		}
		allows++
		if f[1] == "**" {
			allowAll = true
		}
	}
	switch {
	case allowAll:
		return BaselineAllowAll, true
	case !sawNetwork:
		return "", false
	case allows == 0:
		return BaselineDenyAll, true
	default:
		return BaselineBalanced, true
	}
}

func NetworkAllowed(host string) (allowed, known bool) {
	out, err := sh.PolicyCheck(host)
	if err != nil {
		return false, false
	}
	var decision struct {
		Allowed *bool  `json:"allowed"`
		Result  string `json:"result"`
		Access  string `json:"access"`
	}
	if err := json.Unmarshal(out, &decision); err == nil && decision.Allowed != nil {
		return *decision.Allowed, true
	}
	s := strings.ToLower(string(out))
	switch {
	case strings.Contains(s, "\"allowed\""), strings.HasPrefix(s, "allowed:"), strings.Contains(s, "\nallowed:"):
		return true, true
	case strings.Contains(s, "denied"), strings.Contains(s, "blocked"):
		return false, true
	}
	return false, false
}
