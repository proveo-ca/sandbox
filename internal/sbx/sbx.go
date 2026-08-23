// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_experiments/docker-sandbox.puml
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

// Available reports whether the host can run the sbx backend, and if not, why.
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

// InstallHint is the platform's install line for the sbx CLI.
func InstallHint() string {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return ""
		}
		return "brew trust docker/tap && brew install docker/tap/sbx && sbx login"
	case "windows":
		return "winget install -h Docker.sbx && sbx login"
	case "linux":
		return "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx && sbx login"
	default:
		return ""
	}
}

// Mount is a workspace bind into the sandbox VM.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// RunConfig describes one agent run on the sbx backend.
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

// SecretSetArgs builds the credential-injection argv.
func SecretSetArgs(name string) []string {
	return []string{"secret", "set", name}
}

// secretSet is overridable in tests.
var secretSet = func(name, value string) error {
	cmd := exec.Command(Binary, SecretSetArgs(name)...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SecretSet injects one credential host-side.
func SecretSet(name, value string) error {
	return secretSet(name, value)
}

// Kit is the posture rendered as a Kit spec.yaml.
type Kit struct {
	Name           string   `yaml:"name"`
	Image          string   `yaml:"image"`
	Network        KitNet   `yaml:"network"`
	CredentialsEnv []string `yaml:"credentialsEnv"`
}

// KitNet carries the allowlist.
type KitNet struct {
	AllowedDomains []string `yaml:"allowedDomains,omitempty"`
}

// WriteKit renders k into dir/spec.yaml and returns dir.
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
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("sbx kit write: %w", err)
	}
	return dir, nil
}
