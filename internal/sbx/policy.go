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
