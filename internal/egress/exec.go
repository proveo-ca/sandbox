// SPEC: _spec/internal/egress/teardown-and-signals.puml, _spec/internal/egress/egress-tiers.puml
package egress

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a docker command (argv after `docker`) and returns stdout.
type Runner interface {
	Run(args ...string) (string, error)
}

// ExecRunner runs the real `docker` binary.
type ExecRunner struct{ Stderr bool }

func (e ExecRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	if e.Stderr {
		cmd.Stderr = os.Stderr
	}
	out, err := cmd.Output()
	return string(out), err
}

// Apply creates the networks, starts the sidecars, and wires the connects, in
// order. It stops at the first error (the caller should then Teardown).
func (p Plan) Apply(r Runner) error {
	for _, group := range [][]Command{p.Networks, p.Sidecars, p.Connects} {
		for _, c := range group {
			if _, err := r.Run(c...); err != nil {
				return fmt.Errorf("egress apply: docker %s: %w", strings.Join(c, " "), err)
			}
		}
	}
	return nil
}

// ollamaPollInterval is the WaitOllamaReady poll cadence (var so tests can shrink it).
var ollamaPollInterval = 500 * time.Millisecond

// WaitOllamaReady polls `docker exec <name> ollama list` until it succeeds (the
// server is accepting connections) or the timeout elapses. Model cold-load still
// happens on the agent's first inference call, not here.
func WaitOllamaReady(r Runner, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := r.Run("exec", name, "ollama", "list"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ollama sidecar %s not ready after %s", name, timeout)
		}
		time.Sleep(ollamaPollInterval)
	}
}

// squidPollInterval is the WaitSquidReady poll cadence (var so tests can shrink it).
var squidPollInterval = 300 * time.Millisecond

// WaitSquidReady polls until the Squid sidecar accepts a TCP connection on :3128
// (a bash /dev/tcp probe run inside the container — the ubuntu/squid image has
// bash but no nc/squidclient) or the timeout elapses. It closes the startup race
// where the agent (proxy mode) or the egress proxy (firewall mode) fires its
// first request into a not-yet-listening Squid; the CA-wait and Ollama-wait were
// the only gates before.
func WaitSquidReady(r Runner, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := r.Run("exec", name, "bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/3128"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("squid sidecar %s not accepting on :3128 after %s", name, timeout)
		}
		time.Sleep(squidPollInterval)
	}
}

// teardownNetRetries/Interval bound the `network rm` retry (vars so tests tune them).
var (
	teardownNetRetries  = 8
	teardownNetInterval = 250 * time.Millisecond
)

// Teardown removes the containers and networks, best-effort (errors ignored, as
// in the Bash cleanup — a run that half-built should still tear down the rest).
func (p Plan) Teardown(r Runner) {
	for _, c := range p.Cleanup {
		if len(c) >= 2 && c[0] == "network" && c[1] == "rm" {
			// `docker rm -f` returns before the container's endpoint is released,
			// so the network briefly still has active endpoints. Retry until drained.
			for i := 0; ; i++ {
				if _, err := r.Run(c...); err == nil || i >= teardownNetRetries {
					break
				}
				time.Sleep(teardownNetInterval)
			}
			continue
		}
		_, _ = r.Run(c...)
	}
}

// Render is a stable, human-readable dump of the plan for golden tests.
func (p Plan) Render() string {
	var b strings.Builder
	section := func(name string, cmds []Command) {
		fmt.Fprintf(&b, "# %s\n", name)
		for _, c := range cmds {
			fmt.Fprintf(&b, "docker %s\n", strings.Join(c, " "))
		}
	}
	section("networks", p.Networks)
	section("sidecars", p.Sidecars)
	section("connects", p.Connects)
	fmt.Fprintf(&b, "# agent-args\n%s\n", strings.Join(p.AgentArgs, " "))
	if p.CAWaitPath != "" {
		fmt.Fprintf(&b, "# ca-wait\n%s\n", p.CAWaitPath)
	}
	if p.SquidContainer != "" {
		fmt.Fprintf(&b, "# squid-wait\n%s\n", p.SquidContainer)
	}
	if p.OllamaContainer != "" {
		fmt.Fprintf(&b, "# ollama-wait\n%s\n", p.OllamaContainer)
	}
	section("cleanup", p.Cleanup)
	return b.String()
}
