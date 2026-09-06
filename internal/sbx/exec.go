package sbx

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CLI is every shell-out this package makes, named in one place.
type CLI struct {
	Version        func() ([]byte, error)
	DockerMemTotal func() ([]byte, error)
	TemplateList   func() ([]byte, error)
	TemplateLoad   func(image string) error
	TemplateRemove func(image string) error
	PolicyLog      func(sandbox string) ([]byte, error)
	InspectPolicy  func() ([]byte, error)
	PolicyCheck    func(host string) ([]byte, error)

	LocalImageID    func(image string) string
	ImageEntrypoint func(image string) []string

	SandboxList func() ([]byte, error)
	SecretList  func() ([]byte, error)
	SecretSet   func(name, value string) error
}

var sh = defaultCLI()

func bounded(name string, args ...string) ([]byte, error) {
	return boundedWith(dockerInfoTimeout, name, args...)
}

// boundedGrace is how long a killed command may go on holding the output pipe
// before the pipe is closed out from under it.
const boundedGrace = 2 * time.Second

// boundedWith runs a command under a deadline that bounds the CALL, not merely
// the process.
//
// The context alone does not do that. It kills the command we started; Output()
// waits on the stdout PIPE, and any grandchild that inherited the pipe keeps it
// open after its parent dies. Measured here: `sh -c "sleep 60"` under a 50ms
// context returns in 60s — the shell dies on schedule, `sleep` inherits the
// pipe and Output() blocks for the full minute. `sleep 60` started directly
// returns in 51ms, which is why the hazard is invisible until something
// forks.
//
// WaitDelay is the answer Go added for exactly this: once the context fires,
// the pipes are force-closed after the grace period and the call returns. The
// deadline was doing half a job — `docker info` on a wedged daemon is a
// fork-happy client, and MemoryLimit runs on EVERY sbx launch including
// `--print`, so half a job there is a proveo that hangs with nothing on screen.
// SPEC: _spec/internal/sbx/sandbox-backend.puml
func boundedWith(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = boundedGrace
	return cmd.Output()
}

// boundedCombined is bounded() for the calls that read stderr too. sbx prints
// its "is the daemon there" diagnostics on stderr, so those readers cannot use
// Output() and would otherwise be the only ones left without a deadline.
func boundedCombined(name string, args ...string) ([]byte, error) {
	return boundedCombinedWith(dockerInfoTimeout, name, args...)
}

func boundedCombinedWith(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = boundedGrace
	return cmd.CombinedOutput()
}

// defaultCLI splits on ONE question: can this call hang while the operator
// waits, or is it the operation the operator is waiting FOR?
//
// The read-only interrogations — version, ls, secret ls, template ls, info —
// are all "tell me the state so I can decide", and every one of them sits in
// front of a launch. Version in particular is reached by sbx.Available() on the
// same pre-launch path as MemoryLimit, so a wedged daemon hangs proveo there
// with the same blank screen.
//
// TemplateLoad, TemplateRemove and SecretSet are deliberately NOT bounded. An
// image load runs for minutes by design; a 10s deadline there would abort the
// work rather than protect the operator from waiting on it.
// SPEC: _spec/internal/sbx/sandbox-backend.puml
func defaultCLI() CLI {
	return CLI{
		Version: func() ([]byte, error) { return bounded(Binary, "version") },
		DockerMemTotal: func() ([]byte, error) {
			return bounded("docker", "info", "--format", "{{.MemTotal}}")
		},
		TemplateList:   func() ([]byte, error) { return bounded(Binary, TemplateListArgs()...) },
		TemplateRemove: func(image string) error { return exec.Command(Binary, TemplateRemoveArgs(image)...).Run() },
		TemplateLoad:   templateLoadViaTar,
		PolicyLog:      func(sandbox string) ([]byte, error) { return bounded(Binary, PolicyLogArgs(sandbox)...) },
		InspectPolicy:  func() ([]byte, error) { return bounded(Binary, InspectPolicyArgs()...) },
		PolicyCheck:    func(host string) ([]byte, error) { return bounded(Binary, CheckNetworkArgs(host)...) },

		LocalImageID:    dockerImageID,
		ImageEntrypoint: dockerImageEntrypoint,
		SandboxList:     func() ([]byte, error) { return boundedCombined(Binary, "ls") },
		SecretList:      func() ([]byte, error) { return boundedCombined(Binary, "secret", "ls") },
		SecretSet:       sbxSecretSet,
	}
}

func dockerImageID(image string) string {
	out, err := exec.Command("docker", "image", "inspect", image, "--format", "{{.Id}}").Output()
	if err != nil {
		return ""
	}
	id := strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func dockerImageEntrypoint(image string) []string {
	out, err := exec.Command("docker", "image", "inspect", image,
		"--format", "{{json .Config.Entrypoint}}").Output()
	if err != nil {
		return nil
	}
	var ep []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &ep); err != nil {
		return nil
	}
	return ep
}

func sbxSecretSet(name, value string) error {
	cmd := exec.Command(Binary, SecretSetArgs(name)...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
