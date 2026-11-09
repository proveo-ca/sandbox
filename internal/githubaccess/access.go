// SPEC: _spec/cmd/proveo/github-access.puml
package githubaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SSH   = "ssh"
	HTTPS = "https"
	Later = "later"
	Env   = "PROVEO_GITHUB_TRANSPORT"
)

type Config struct {
	Transport string `json:"transport"`
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "proveo", "github.json"), err
}

func Load(path string) (Config, error) {
	c := Config{Transport: HTTPS}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.Transport != SSH && c.Transport != HTTPS && c.Transport != Later {
		return Config{}, fmt.Errorf("invalid GitHub transport %q", c.Transport)
	}
	return c, nil
}

func Save(path, transport string) error {
	if transport != SSH && transport != HTTPS && transport != Later {
		return fmt.Errorf("invalid GitHub transport %q", transport)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(Config{Transport: transport})
	f, err := os.CreateTemp(filepath.Dir(path), ".github-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type Host struct {
	Protocol, Login, Socket, AgentError string
	Keys                                []string
	Forwarding, ForwardingKnown         bool
}

type Command func(env []string, name string, args ...string) ([]byte, error)

func Output(env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.WaitDelay = time.Second
	if env != nil {
		c.Env = env
	}
	return c.Output()
}

func Inspect(bin string, command Command, getenv func(string) string) Host {
	h := InspectSSH(bin, command, getenv)
	if out, err := command(nil, "gh", "config", "get", "git_protocol", "--host", "github.com"); err == nil {
		h.Protocol = strings.TrimSpace(string(out))
	}
	if out, err := command(nil, "gh", "api", "--hostname", "github.com", "user", "--jq", ".login"); err == nil {
		h.Login = strings.TrimSpace(string(out))
	}
	return h
}

func InspectSSH(bin string, command Command, getenv func(string) string) Host {
	h := Host{Socket: getenv("SSH_AUTH_SOCK")}
	read := func(name string, args ...string) string {
		out, err := command(nil, name, args...)
		if err != nil {
			return ""
		}
		return strings.Trim(strings.TrimSpace(string(out)), "\"")
	}
	v := read(bin, "settings", "get", "ssh.agentForwardingEnabled")
	forwarding, err := strconv.ParseBool(v)
	h.Forwarding, h.ForwardingKnown = forwarding, err == nil
	if socket := read(bin, "settings", "get", "ssh.agentSocketPath"); socket != "" {
		h.Socket = socket
	}
	if h.Socket == "" {
		h.AgentError = "no host SSH agent socket; start or select a host agent"
		return h
	}
	env := append(os.Environ(), "SSH_AUTH_SOCK="+h.Socket)
	out, err := command(env, "ssh-add", "-l")
	if err != nil {
		h.AgentError = "host agent unavailable or empty; load a GitHub authentication key with ssh-add"
		return h
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && strings.HasPrefix(fields[1], "SHA256:") {
			h.Keys = append(h.Keys, strings.Join(fields[1:], " "))
		}
	}
	if len(h.Keys) == 0 {
		h.AgentError = "host agent lists no usable key fingerprints"
	}
	return h
}

func (h Host) SSHProblem() string {
	if !h.ForwardingKnown {
		return "cannot read sbx SSH forwarding; run sbx setup on the host"
	}
	if !h.Forwarding {
		return "sbx SSH-agent forwarding is disabled"
	}
	return h.AgentError
}
