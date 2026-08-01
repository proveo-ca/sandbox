// Package dind provisions a sibling Docker-in-Docker sidecar for harnesses
// whose image ships a docker client (manifest dind: true).
//
// SPEC: _spec/internal/dind/dind-sidecar.puml, _spec/components.puml, _spec/cmd/proveo/usage.puml
package dind

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/wsscan"
)

// EnvEnabled reports whether PROVEO_DIND is on.
func EnvEnabled() bool {
	return truthy(os.Getenv("PROVEO_DIND"))
}

// ModeSupported reports whether DinD can run under the given egress mode. Only
// broker mode (direct bridge egress) can expose a Docker daemon to the agent:
// under proxy/firewall the agent sits on an --internal network the daemon cannot
// be reached across (a legacy --link does not span networks), and attaching the
// internet-capable daemon to that network would defeat egress enforcement.
func ModeSupported(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "open", "broker":
		return true
	}
	return false
}

// CredentialsSupported reports whether the credential mode leaves an
// internet-capable agent network for a DinD attach. Brokered credentials put the
// agent behind the MITM on an --internal network; attaching a daemon there would
// either lose the internet or bypass the injector.
func CredentialsSupported(credentials string) bool {
	return strings.EqualFold(strings.TrimSpace(credentials), "forward")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ScopeHasDockerfiles reports whether the scope has Dockerfile/Compose files of
// its OWN. It delegates to internal/wsscan so the prune set is shared: an npm
// package's Dockerfile under node_modules must not offer a privileged sidecar for
// a repo that ships none.
func ScopeHasDockerfiles(scopeDir string) bool {
	res := wsscan.Scan(scopeDir, scopeDir, []wsscan.Marker{{
		Label: "docker",
		Names: []string{"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
	}}, 0)
	return res.Has("docker")
}

// ShouldStart reports whether DinD should be launched for a dind-capable
// harness given the scope and (optional) interactive answer.
// promptYes is only consulted when env is off and interactive is true;
// pass nil to treat interactive as "no".
func ShouldStart(capable bool, scopeDir string, interactive bool, promptYes func() bool) bool {
	if !capable || scopeDir == "" {
		return false
	}
	if !ScopeHasDockerfiles(scopeDir) {
		return false
	}
	if EnvEnabled() {
		return true
	}
	if interactive && promptYes != nil {
		return promptYes()
	}
	return false
}

// PromptYesNo prints the DinD question and returns true only on y/yes.
// Empty / timeout / other answers are false. in is typically os.Stdin.
func PromptYesNo(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "\n🐳 Dockerfiles or Compose configurations detected in the project scope.\n")
	fmt.Fprint(out, "Do you want to launch a sibling Docker-in-Docker (dind) container for local testing? [y/N] ")
	// Bounded read so non-interactive pipes don't hang forever.
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if buf[0] == '\n' {
					break
				}
				b.WriteByte(buf[0])
			}
			if err != nil {
				ch <- result{b.String(), err}
				return
			}
		}
		ch <- result{b.String(), nil}
	}()
	var line string
	select {
	case r := <-ch:
		line = r.line
	case <-time.After(10 * time.Second):
		line = "n"
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// Sidecar is a running docker:dind container linked into the agent.
type Sidecar struct {
	Name     string
	ScopeDir string
}

// EnvArgs are the docker-run env flags pointing the agent's docker client at the
// sidecar daemon. Always applied when DinD is attached, regardless of how the
// agent reaches the daemon.
func (s *Sidecar) EnvArgs() []string {
	if s == nil || s.Name == "" {
		return nil
	}
	return []string{
		"-e", "DOCKER_HOST=tcp://docker:2375",
		"-e", "DOCKER_TLS_VERIFY=",
	}
}

// LinkArgs wire the agent to the sidecar via a legacy --link. This resolves the
// `docker` hostname ONLY when both share the default bridge (broker mode without
// a local-model network). For a user-defined agent network use ConnectNetwork —
// --link does not span networks.
func (s *Sidecar) LinkArgs() []string {
	if s == nil || s.Name == "" {
		return nil
	}
	return []string{"--link", s.Name + ":docker"}
}

// AgentArgs is the default-bridge attachment: link + env, in one slice.
func (s *Sidecar) AgentArgs() []string {
	return append(s.LinkArgs(), s.EnvArgs()...)
}

// ConnectNetwork attaches the sidecar to a user-defined network with the alias
// `docker`, so an agent on that network resolves the daemon by name. Used for
// broker mode with a local-model sidecar, where the agent is on a user-defined
// bridge rather than the default bridge. No-op when name/network is empty.
func (s *Sidecar) ConnectNetwork(r Runner, network string) error {
	if s == nil || s.Name == "" || network == "" {
		return nil
	}
	if r == nil {
		r = ExecRunner{}
	}
	return r.Run("network", "connect", "--alias", "docker", network, s.Name)
}

// Runner executes docker commands (injectable for tests).
type Runner interface {
	Run(args ...string) error
}

// ExecRunner runs real docker via os/exec.
type ExecRunner struct{}

func (ExecRunner) Run(args ...string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Start launches a privileged docker:dind sidecar mounting scopeDir at /app.
func Start(r Runner, target, scopeDir string, warn io.Writer) (*Sidecar, error) {
	if r == nil {
		r = ExecRunner{}
	}
	if warn == nil {
		warn = os.Stderr
	}
	name := "proveo-dind-" + target
	fmt.Fprintf(warn, "ℹ️ Starting sibling Docker-in-Docker (dind) container: %s\n", name)
	fmt.Fprint(warn, "⚠️ Security warning: this dind sidecar runs with --privileged and shares the\n")
	fmt.Fprint(warn, " host kernel. Its Docker daemon is exposed to the harness over an\n")
	fmt.Fprint(warn, " unauthenticated tcp://docker:2375 socket, so any code the agent runs\n")
	fmt.Fprint(warn, " can launch further privileged containers and may be able to escape to\n")
	fmt.Fprint(warn, " the host. It also has read-write access to the shared path: ")
	fmt.Fprint(warn, scopeDir)
	fmt.Fprint(warn, "\n Only enable it for project code you trust.\n\n")

	_ = r.Run("rm", "-f", name)
	if err := r.Run("run", "--privileged", "-d",
		"--name", name,
		"-e", "DOCKER_TLS_CERTDIR=",
		"-v", scopeDir+":/app",
		"docker:dind"); err != nil {
		return nil, fmt.Errorf("start dind sidecar: %w", err)
	}
	return &Sidecar{Name: name, ScopeDir: scopeDir}, nil
}

// Cleanup removes the sidecar container.
func (s *Sidecar) Cleanup(r Runner) {
	if s == nil || s.Name == "" {
		return
	}
	if r == nil {
		r = ExecRunner{}
	}
	_ = r.Run("rm", "-f", s.Name)
	s.Name = ""
}
