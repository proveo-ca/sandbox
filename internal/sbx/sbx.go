// SPEC: _spec/_experiments/docker-sandbox.puml
//
// Backend adapter for Docker Sandboxes ("sbx", free-tier local policy). The CLI
// surface below is pinned centrally so drift against the pre-GA tool is cheap:
// only RunArgs/RemoveArgs/SecretSetArgs/WriteKit encode flags; callers stay
// declarative. Docs referenced by the experiment spec:
//
//	https://docs.docker.com/ai/sandboxes/usage/
//	https://docs.docker.com/ai/sandboxes/customize/kits/
package sbx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Binary is the Docker Sandboxes CLI this package drives.
const Binary = "sbx"

// Test seams; production leaves these at their defaults.
var (
	lookPath  = exec.LookPath
	goos      = runtime.GOOS
	goarch    = runtime.GOARCH
	kvmDevice = "/dev/kvm"
	stat      = os.Stat
)

// Available reports whether the host can run the sbx backend, and when it
// cannot, why (the reason is user-facing). Platform gate follows the experiment
// spec: macOS Apple Silicon or Linux with KVM.
func Available() (bool, string) {
	if _, err := lookPath(Binary); err != nil {
		return false, fmt.Sprintf("%s CLI not found on PATH", Binary)
	}
	switch {
	case goos == "darwin" && goarch == "arm64":
		return true, ""
	case goos == "linux":
		if _, err := stat(kvmDevice); err != nil {
			return false, fmt.Sprintf("linux requires KVM (%s unavailable)", kvmDevice)
		}
		return true, ""
	default:
		return false, fmt.Sprintf("unsupported platform %s/%s (want darwin/arm64 or linux+KVM)", goos, goarch)
	}
}

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// RunConfig describes one agent run on the sbx backend. Credentials never ride
// Env — they are injected host-side via SecretSet before RunArgs executes.
type RunConfig struct {
	Name    string   // sandbox/session name; empty lets sbx assign one
	KitDir  string   // directory holding the rendered Kit spec.yaml
	Image   string   // agent image/template reference
	Mounts  []Mount  // workspace + home binds
	Env     []string // non-secret KEY=VALUE passthrough
	Workdir string   // working directory inside the sandbox
	Command []string // trailing agent command (after "--")
}

// RunArgs builds the sbx invocation for cfg.
func RunArgs(cfg RunConfig) []string {
	args := []string{"run"}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.KitDir != "" {
		args = append(args, "--kit", cfg.KitDir)
	}
	for _, m := range cfg.Mounts {
		spec := m.Host + ":" + m.Container
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.Workdir != "" {
		args = append(args, "-w", cfg.Workdir)
	}
	if cfg.Image != "" {
		args = append(args, cfg.Image)
	}
	if len(cfg.Command) > 0 {
		args = append(args, "--")
		args = append(args, cfg.Command...)
	}
	return args
}

// RemoveArgs builds the ephemeral teardown invocation (VM + images + volumes).
func RemoveArgs(name string) []string {
	return []string{"rm", name}
}

// SecretSetArgs builds the credential-injection argv; the value travels on
// stdin, never on the command line.
func SecretSetArgs(name string) []string {
	return []string{"secret", "set", name}
}

// secretSet is overridable in tests; production pipes value into the CLI stdin.
var secretSet = func(name, value string) error {
	cmd := exec.Command(Binary, SecretSetArgs(name)...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SecretSet injects one credential host-side; the value never enters the VM's
// filesystem or the sandbox argv.
func SecretSet(name, value string) error {
	return secretSet(name, value)
}

// Kit is the declarative posture rendered as a Kit spec.yaml: deny-by-default
// network with an explicit allowlist, plus the credentials the harness reads.
type Kit struct {
	Name           string   `yaml:"name"`
	Image          string   `yaml:"image"`
	Network        KitNet   `yaml:"network"`
	CredentialsEnv []string `yaml:"credentialsEnv"`
}

// KitNet carries the allowlist; everything not listed is denied (deny wins).
type KitNet struct {
	AllowedDomains []string `yaml:"allowedDomains,omitempty"`
}

// WriteKit renders k into dir/spec.yaml and returns the path.
func WriteKit(dir string, k Kit) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(k); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sbx kit dir: %w", err)
	}
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("sbx kit write: %w", err)
	}
	return path, nil
}
