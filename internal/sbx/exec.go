// SPEC: _spec/internal/sbx/sandbox-backend.puml
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
	VersionJSON    func() ([]byte, error)
	DockerMemTotal func() ([]byte, error)
	PolicyLog      func(sandbox string) ([]byte, error)
	MemoryEvidence func(sandbox string) ([]byte, error)
	InspectPolicy  func() ([]byte, error)
	PolicyCheck    func(host string) ([]byte, error)

	LocalImageID    func(image string) string
	RepoDigests     func(image string) []string
	ImageEntrypoint func(image string) []string
	ImageCmd        func(image string) []string

	SandboxList   func() ([]byte, error)
	SecretList    func() ([]byte, error)
	SecretSet     func(name, value string) error
	SecretSetFrom func(args []string, stdin string) error
}

var sh = defaultCLI()

func bounded(name string, args ...string) ([]byte, error) {
	return boundedWith(dockerInfoTimeout, name, args...)
}

// boundedGrace is how long a killed command may go on holding the output pipe
// before the pipe is closed out from under it.
const boundedGrace = 2 * time.Second

func boundedWith(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = boundedGrace
	return cmd.Output()
}

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

func defaultCLI() CLI {
	return CLI{
		Version:     func() ([]byte, error) { return bounded(Binary, "version") },
		VersionJSON: func() ([]byte, error) { return bounded(Binary, VersionJSONArgs()...) },
		DockerMemTotal: func() ([]byte, error) {
			return bounded("docker", "info", "--format", "{{.MemTotal}}")
		},
		PolicyLog: func(sandbox string) ([]byte, error) { return bounded(Binary, PolicyLogArgs(sandbox)...) },
		MemoryEvidence: func(sandbox string) ([]byte, error) {
			return boundedCombined(Binary, MemoryEvidenceArgs(sandbox)...)
		},
		InspectPolicy: func() ([]byte, error) { return bounded(Binary, InspectPolicyArgs()...) },
		PolicyCheck:   func(host string) ([]byte, error) { return bounded(Binary, CheckNetworkArgs(host)...) },

		LocalImageID:    dockerImageID,
		RepoDigests:     dockerRepoDigests,
		ImageEntrypoint: func(image string) []string { return dockerImageList(image, "Entrypoint") },
		ImageCmd:        func(image string) []string { return dockerImageList(image, "Cmd") },
		SandboxList:     func() ([]byte, error) { return boundedCombined(Binary, "ls") },
		SecretList:      func() ([]byte, error) { return boundedCombined(Binary, "secret", "ls") },
		SecretSet:       sbxSecretSet,
		SecretSetFrom:   sbxSecretSetFrom,
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

// dockerImageList reads one list field of the image's config (Entrypoint, Cmd); nil when unreadable.
func dockerImageList(image, field string) []string {
	out, err := exec.Command("docker", "image", "inspect", image,
		"--format", "{{json .Config."+field+"}}").Output()
	if err != nil {
		return nil
	}
	var ep []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &ep); err != nil {
		return nil
	}
	return ep
}

func sbxSecretSetFrom(args []string, stdin string) error {
	cmd := exec.Command(Binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sbxSecretSet(name, value string) error {
	cmd := exec.Command(Binary, SecretSetArgs(name)...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
