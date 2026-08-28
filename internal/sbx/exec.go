package sbx

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

// CLI is every shell-out this package makes, named in one place.
//
// These were eight separate package-level function variables, each declared
// beside the code that called it. That is a recognised Go test-seam idiom, but it
// left the package's entire outside surface — what it runs, and therefore what a
// test must stub to stay hermetic — scattered across five files with no way to
// enumerate it. A reader asking "what does this package actually execute?" had to
// grep for `= func(`.
//
// It is ONE package-level value rather than a parameter on every exported
// function, and that is a deliberate stopping point. Threading a CLI through
// Available, MemoryLimit, EnsureTemplate, PolicyBaseline and the rest would put
// plumbing at every call site in cmd/proveo and internal/backend/sandbox to serve
// a second implementation that does not exist and is not coming — the speculative
// abstraction the design rules warn about. What the struct buys is the part worth
// having: the surface is enumerable, documented, and swapped as a unit.
type CLI struct {
	Version        func() ([]byte, error)
	DockerMemTotal func() ([]byte, error)
	TemplateList   func() ([]byte, error)
	TemplateLoad   func(image string) error
	TemplateRemove func(image string) error
	PolicyLog      func(sandbox string) ([]byte, error)
	InspectPolicy  func() ([]byte, error)
	PolicyCheck    func(host string) ([]byte, error)

	// docker, not sbx: the two image stores are separate, so proveo has to ask
	// docker what it holds before deciding whether sbx needs a reload.
	LocalImageID    func(image string) string
	ImageEntrypoint func(image string) []string

	SandboxList func() ([]byte, error)
	SecretList  func() ([]byte, error)
	SecretSet   func(name, value string) error
}

// sh is the CLI production runs. Tests replace individual fields.
var sh = defaultCLI()

// bounded runs a command under dockerInfoTimeout. Every daemon call here is
// bounded for the reason MemoryLimit documents: a degraded daemon does not fail,
// it waits, and an unbounded call turns that into a proveo that hangs with
// nothing on screen.
func bounded(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerInfoTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func defaultCLI() CLI {
	return CLI{
		Version: func() ([]byte, error) { return exec.Command(Binary, "version").Output() },
		// What the daemon can actually hand a container: host memory on Linux, the
		// VM's share on macOS and Windows — the number that matters, and the one sbx
		// cannot see.
		DockerMemTotal: func() ([]byte, error) {
			return bounded("docker", "info", "--format", "{{.MemTotal}}")
		},
		TemplateList:   func() ([]byte, error) { return exec.Command(Binary, TemplateListArgs()...).Output() },
		TemplateRemove: func(image string) error { return exec.Command(Binary, TemplateRemoveArgs(image)...).Run() },
		TemplateLoad:   templateLoadViaTar,
		PolicyLog:      func(sandbox string) ([]byte, error) { return bounded(Binary, PolicyLogArgs(sandbox)...) },
		InspectPolicy:  func() ([]byte, error) { return bounded(Binary, InspectPolicyArgs()...) },
		PolicyCheck:    func(host string) ([]byte, error) { return bounded(Binary, CheckNetworkArgs(host)...) },

		LocalImageID:    dockerImageID,
		ImageEntrypoint: dockerImageEntrypoint,
		SandboxList:     func() ([]byte, error) { return exec.Command(Binary, "ls").CombinedOutput() },
		SecretList:      func() ([]byte, error) { return exec.Command(Binary, "secret", "ls").CombinedOutput() },
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
