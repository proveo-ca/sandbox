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

// SPEC: _spec/internal/sbx/sandbox-backend.puml
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

// SPEC: _spec/internal/sbx/sandbox-backend.puml
func defaultCLI() CLI {
	return CLI{
		Version:     func() ([]byte, error) { return bounded(Binary, "version") },
		VersionJSON: func() ([]byte, error) { return bounded(Binary, VersionJSONArgs()...) },
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
