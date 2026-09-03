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
// active. Tools write sessions under this tree; host uid remapping cannot move it.
const ContainerHome = "/proveo-home"

// Root returns PROVEO_HOME, or <user home>/.proveo when unset.
func Root(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := strings.TrimSpace(getenv("PROVEO_HOME")); v != "" {
		return v
	}
	return filepath.Join(UserHome(getenv), ".proveo")
}

// UserHome resolves the operator's home directory across the three host OSes
// proveo ships for (see .goreleaser.yaml: linux, darwin, windows).
//
// HOME alone is a Unix answer. On Windows it is set by Git Bash / MSYS and by
// nothing else, so a proveo started from PowerShell or cmd read an EMPTY HOME
// and fell through to ".", which put the durable root — sessions, logs, run
// transcripts and now provisioned toolchains — inside whatever directory the
// operator happened to be standing in, typically their repository.
//
// USERPROFILE is the documented Windows answer and HOMEDRIVE+HOMEPATH is its
// domain-joined fallback; both are what os.UserHomeDir consults. This is
// getenv-driven rather than a call to os.UserHomeDir so one seam still governs
// the whole resolution and a test can pin every platform's shape from any host.
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
	// Every source empty. "." keeps proveo runnable rather than erroring at
	// startup, and it is the state the caller sees when a home cannot be found.
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
		// PROVEO_HOME is HOME under a name no launcher rewrites; the seed reads it on
		// both backends so one code path cannot silently target a different home.
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

// ResumeArgs maps proveo --resume/--continue/--ls onto harness CLI argv.
// target is the runnable image target (e.g. cursor-browser, claudecode-solidity).
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
