// SPEC: _spec/internal/proveohome/proveo-home-components.puml,
// _spec/internal/proveohome/proveo-home-lifecycle.puml
//
// SPEC: _spec/internal/proveohome/proveo-home-components.puml, _spec/internal/proveohome/proveo-home-lifecycle.puml
package proveohome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

// ContainerHome is the fixed HOME inside the agent when proveo home mounts are
// active.
const ContainerHome = "/proveo-home"

func Root(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := strings.TrimSpace(getenv("PROVEO_HOME")); v != "" {
		return v
	}
	return filepath.Join(UserHome(getenv), ".proveo")
}

func UserHome(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if h := strings.TrimSpace(getenv("HOME")); h != "" {
		return h
	}
	if h := strings.TrimSpace(getenv("USERPROFILE")); h != "" {
		return h
	}
	drive, path := strings.TrimSpace(getenv("HOMEDRIVE")), strings.TrimSpace(getenv("HOMEPATH"))
	if drive != "" && path != "" {
		return drive + path
	}
	return "."
}

// Plan is the resolved bind of PROVEO_HOME plus env to inject.
type Plan struct {
	Root   string
	Mounts []runner.Mount
	Env    []string // HOME=/proveo-home
}

func Prepare(h manifest.Home, getenv func(string) string) (Plan, error) {
	if !h.Active() {
		return Plan{}, nil
	}
	root := Root(getenv)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Plan{}, fmt.Errorf("proveo home: mkdir %s: %w", root, err)
	}
	for _, m := range h.Mounts {
		host := filepath.Join(root, filepath.FromSlash(m.Host))
		if err := os.MkdirAll(host, 0o700); err != nil {
			return Plan{}, fmt.Errorf("proveo home: mkdir %s: %w", host, err)
		}
		if err := scrubDeny(host, m.Deny); err != nil {
			return Plan{}, err
		}
	}
	return Plan{
		Root: root,
		Mounts: []runner.Mount{{
			Host:      root,
			Container: ContainerHome,
			ReadOnly:  false,
		}},
		Env: []string{"HOME=" + ContainerHome, "PROVEO_HOME=" + ContainerHome},
	}, nil
}

func scrubDeny(dir string, deny []string) error {
	for _, name := range deny {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, `\`) || name == "." || name == ".." {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("proveo home: scrub %s: %w", p, err)
		}
	}
	return nil
}

func ResumeArgs(target, resumeID string, cont, list bool) ([]string, error) {
	base := harnessFamily(target)
	switch {
	case list && cont:
		return nil, fmt.Errorf("--ls and --continue are mutually exclusive")
	case list && resumeID != "":
		return nil, fmt.Errorf("--ls and --resume are mutually exclusive")
	case cont && resumeID != "":
		return nil, fmt.Errorf("--continue and --resume are mutually exclusive")
	case !list && !cont && resumeID == "":
		return nil, nil
	}

	switch base {
	case "cursor":
		switch {
		case list:
			return []string{"ls"}, nil
		case cont:
			return []string{"--continue"}, nil
		default:
			return []string{"--resume", resumeID}, nil
		}
	case "claudecode":
		switch {
		case list:
			return []string{"--resume"}, nil
		case cont:
			return []string{"--continue"}, nil
		default:
			return []string{"--resume", resumeID}, nil
		}
	case "opencode":
		switch {
		case list:
			return nil, fmt.Errorf("opencode has no session list subcommand; use --resume <id>")
		case cont:
			return nil, fmt.Errorf("opencode has no --continue; use --resume <session-id>")
		default:
			return []string{"--session", resumeID}, nil
		}
	case "cecli":
		return nil, fmt.Errorf("cecli does not support --resume/--continue/--ls")
	default:
		return nil, fmt.Errorf("unknown harness for resume: %q", target)
	}
}

func harnessFamily(target string) string {
	t := strings.TrimSuffix(target, "-browser")
	switch {
	case t == "cursor":
		return "cursor"
	case t == "opencode":
		return "opencode"
	case t == "cecli":
		return "cecli"
	case strings.HasPrefix(t, "claudecode"):
		return "claudecode"
	default:
		return t
	}
}
