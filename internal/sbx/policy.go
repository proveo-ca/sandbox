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

// Baseline names sbx's GLOBAL network policy, which is the only lever that can
// make a Kit allowlist mean anything: a Kit adds allow rules ON TOP of this, and
// a per-sandbox deny cannot express "only the allowlist" because deny always
// beats allow. See _spec/internal/sbx/policy-baseline.puml.
func Baselines() []string { return []string{BaselineAllowAll, BaselineBalanced, BaselineDenyAll} }

// InspectPolicyArgs reads the global policy. `local-policy` is the id sbx gives
// the baseline; it is not a per-sandbox policy.
func InspectPolicyArgs() []string { return []string{"policy", "inspect", "local-policy"} }

// PolicyBaseline reports which baseline this host is on, read from sbx rather
// than probed: `sbx policy check` can only tell allow-all from "something
// stricter", because balanced and deny-all both deny an unallowlisted host.
//
// Classification is structural, from the network rules sbx prints:
//
//	allow **        -> allow-all   (rule id default-allow-all)
//	no network allow -> deny-all
//	specific allows  -> balanced
//
// known is false when sbx is absent or its output is unrecognisable, and callers
// must say "unreadable" rather than assume a posture in either direction.
func PolicyBaseline() (name string, known bool) {
	out, err := sh.InspectPolicy()
	if err != nil {
		return "", false
	}
	var allowAll, sawNetwork bool
	allows := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// DECISION RESOURCE TYPE ... — network rows only; filesystem rows are a
		// separate axis and sbx prints them in the same table.
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

// SecretSetArgs builds the credential-injection argv.
//
// --force is what makes the write non-interactive on a SECOND run. `sbx secret
// set` reads the value from stdin, which works once; when the secret already
// exists it first asks "Overwrite? (y/N)", and the piped value answers THAT
// prompt instead — so the write is cancelled and the agent silently starts with
// the credential it had before. Every run after the first hit this.
//
// The value still travels on stdin. --token would pass it as an argument, which
// the credential boundary forbids: a secret on an argv is visible in `ps` and in
// shell history (_spec/_paradigms/credential-boundary.puml).
