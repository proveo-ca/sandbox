// SPEC: _spec/cmd/proveo/github-access.puml
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/githubaccess"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	githubSSH   = "SSH agent"
	githubHTTPS = "HTTPS token"
)

func githubAccessForm(h githubaccess.Host, current string) *choiceui.Form {
	login := h.Login
	if login == "" {
		login = "unavailable; API sign-in is independent of SSH"
	}
	protocol := h.Protocol
	if protocol == "" {
		protocol = "unconfigured"
	}
	selected := 1
	if current == githubaccess.SSH || (current == "" && h.Protocol == githubaccess.SSH) {
		selected = 0
	}
	if current == githubaccess.Later {
		selected = 2
	}
	header := []string{"GitHub API       " + login, "Host Git         " + protocol,
		fmt.Sprintf("Host SSH agent   %d key(s)", len(h.Keys)), "Sandbox SSH      verification pending until proveo run"}
	for _, key := range h.Keys {
		header = append(header, "  "+key)
	}
	if why := h.SSHProblem(); why != "" {
		header = append(header, "SSH status       "+why)
	}
	return &choiceui.Form{Title: "GitHub access — API login and sandbox Git", NoAxis: true,
		Glyphs: posture.GlyphModeFrom(os.Getenv), Header: header,
		Rows: []choiceui.Row{{Label: "sandbox Git", Options: []string{githubSSH, githubHTTPS, "later"}, Selected: selected, CellRunes: 20,
			Help: map[string]string{
				githubSSH:   "agents may authenticate with keys exposed by the host agent; private keys stay on the host",
				githubHTTPS: "use sbx's github token for Git HTTPS; preserve host gh preferences and stored remote URLs",
				"later":     "defer sandbox Git authentication setup",
			}}},
	}
}

func applyGitHubAccess(bin string, prompt bool) {
	path, err := githubaccess.Path()
	if err != nil {
		ui.Warnf("GitHub access settings: %v", err)
		return
	}
	current := ""
	if _, err := os.Stat(path); err == nil {
		cfg, err := githubaccess.Load(path)
		if err != nil {
			ui.Warnf("GitHub access settings: %v", err)
			return
		}
		current = cfg.Transport
	}
	for {
		h := githubaccess.Inspect(bin, githubaccess.Output, os.Getenv)
		if !prompt {
			ui.Notef("GitHub SSH: %d host key(s); sandbox verification pending; run proveo init from a terminal to choose access", len(h.Keys))
			if why := h.SSHProblem(); why != "" {
				ui.Notef("%s", why)
			}
			return
		}
		form := githubAccessForm(h, current)
		ok, err := form.Run()
		if err != nil {
			ui.Warnf("GitHub access: %v", err)
			return
		}
		if !ok {
			return
		}
		mode := map[string]string{githubSSH: githubaccess.SSH, githubHTTPS: githubaccess.HTTPS, "later": githubaccess.Later}[form.Selection("sandbox Git")]
		if mode == githubaccess.SSH && h.SSHProblem() != "" {
			action, err := githubSSHRepair(h)
			if err != nil {
				ui.Warnf("GitHub SSH: %v", err)
				return
			}
			switch action {
			case "enable + restart":
				if err := runArgv([]string{bin, "settings", "set", "ssh.agentForwardingEnabled", "true"}); err != nil {
					ui.Warnf("could not enable SSH forwarding: %v", err)
					continue
				}
				if err := runArgv([]string{bin, "daemon", "restart"}); err != nil {
					ui.Warnf("daemon restart failed: %v", err)
					continue
				}
				current = githubaccess.SSH
				continue
			case "retry":
				current = githubaccess.SSH
				continue
			case githubHTTPS:
				mode = githubaccess.HTTPS
			case "later":
				mode = githubaccess.Later
			default:
				return
			}
		}
		if mode == githubaccess.HTTPS {
			if tok, _ := hostGhAuth().resolve(); tok == "" {
				ui.Warnf("HTTPS requires a GitHub API credential; run gh auth login on the host, then retry")
				current = githubaccess.HTTPS
				continue
			}
		}
		if err := githubaccess.Save(path, mode); err != nil {
			ui.Warnf("could not save GitHub access: %v", err)
			return
		}
		ui.Storef("sandbox Git: %s — %s", mode, path)
		if mode == githubaccess.SSH {
			ui.Notef("host configured; proveo run verifies sandbox SSH before releasing the agent")
		}
		return
	}
}

func githubSSHRepair(h githubaccess.Host) (string, error) {
	opts := []string{"retry", githubHTTPS, "later"}
	if h.ForwardingKnown && !h.Forwarding {
		opts = append([]string{"enable + restart"}, opts...)
	}
	f := &choiceui.Form{Title: "GitHub SSH needs attention", NoAxis: true,
		Header: []string{h.SSHProblem(), "Load a host key with ssh-add; register its public key with GitHub.",
			"Configure a stable agent with sbx settings set ssh.agentSocketPath <socket>."},
		Rows: []choiceui.Row{{Label: "repair", Options: opts, CellRunes: 20, Help: map[string]string{
			"enable + restart": "enables host-wide SSH-agent forwarding and restarts the sbx daemon; this affects existing sandboxes",
			"retry":            "check the host agent and sbx settings again",
			githubHTTPS:        "use the GitHub API token for sandbox Git operations",
			"later":            "save Git authentication as deferred",
		}}},
	}
	ok, err := f.Run()
	if err != nil || !ok {
		return "", err
	}
	return strings.TrimSpace(f.Selection("repair")), nil
}
