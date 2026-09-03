// Package dind provisions a sibling Docker-in-Docker sidecar for harnesses
// whose image ships a docker client (manifest docker: dind).
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

	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

// EnvEnabled reports whether PROVEO_DIND is on.
func EnvEnabled() bool {
	return truthy(os.Getenv("PROVEO_DIND"))
}

func ModeSupported(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "open")
}

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

func ScopeHasDockerfiles(scopeDir string) bool {
	res := wsscan.Scan(scopeDir, scopeDir, []wsscan.Marker{{
		Label: "docker",
		Names: []string{"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
	}}, 0)
	return res.Has("docker")
}

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
	fmt.Fprintln(out)
	ui.New(out).Appf("Dockerfiles or Compose configurations detected in the project scope.")
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

func (s *Sidecar) EnvArgs() []string {
	if s == nil || s.Name == "" {
		return nil
	}
	return []string{
		"-e", "DOCKER_HOST=tcp://docker:2375",
		"-e", "DOCKER_TLS_VERIFY=",
	}
}

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

// WaitReady blocks until the sidecar's daemon answers, or timeout elapses.
//
// `docker run -d` returns as soon as the CONTAINER is up; the daemon inside it
// listens seconds later. Without this wait proveo hands the agent a shell whose
// very first `docker` call fails with "Cannot connect to the Docker daemon at
// tcp://docker:2375" — a race that reads exactly like a broken posture, and the
// one an e2e probe caught by failing in under six seconds.
//
// The readiness question is asked THROUGH the sidecar (`docker exec … docker
// version`) rather than from the agent: the daemon's own socket is what has to be
// live, and asking from the host keeps the check independent of whether the agent
// container has started yet.
func (s *Sidecar) WaitReady(r Runner, timeout time.Duration, now func() time.Time, sleep func(time.Duration)) error {
	if s == nil || s.Name == "" {
		return nil
	}
	if r == nil {
		r = ExecRunner{}
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := now().Add(timeout)
	for {
		if err := r.Run("exec", s.Name, "docker", "version"); err == nil {
			return nil
		}
		if !now().Before(deadline) {
			return fmt.Errorf("dind: %s did not accept docker commands within %s", s.Name, timeout)
		}
		sleep(time.Second)
	}
}

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
	w := ui.New(warn)
	w.Appf("Starting sibling Docker-in-Docker (dind) container: %s", name)
	// Warnf, not Dangerf: this is ATTENTION, and only a severity keeps its
	// marker in plain mode. Dangerf is for a destructive act being performed —
	// `proveo clean` removing an image — where the sentence describes itself.
	w.Warnf("this dind sidecar runs with --privileged and shares the host kernel. Its " +
		"Docker daemon is exposed to the harness over an unauthenticated tcp://docker:2375 " +
		"socket, so any code the agent runs can launch further privileged containers and " +
		"may be able to escape to the host.")
	w.Notef("read-write access to the shared path: %s", scopeDir)
	w.Notef("Only enable it for project code you trust.")
	fmt.Fprintln(warn)

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
