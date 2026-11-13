// SPEC: _spec/cmd/proveo/github-access.puml
package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
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
	return &choiceui.Form{Title: "GitHub access — API login and sandbox Git",
		Glyphs: posture.GlyphModeFrom(os.Getenv), Header: header,
		Rows: []choiceui.Row{{Label: "sandbox Git", Options: []string{githubSSH, githubHTTPS, "later"}, Selected: selected, CellRunes: 20,
			Help: map[string]string{
				githubSSH:   "agents may authenticate with keys exposed by the host agent; private keys stay on the host",
				githubHTTPS: "use sbx's github token for Git HTTPS; preserve host gh preferences and stored remote URLs",
				"later":     "defer sandbox Git authentication setup",
			}}},
	}
}

// applyGitHubAccess asks how sandbox Git reaches GitHub and saves the answer, and says what it is
// in the git block as the "access" fact. Unprompted it only reports; a Ctrl+C that ended a
// program it handed the terminal to is its error.
func applyGitHubAccess(bin string, prompt bool, out *sandbox.Block, noTerminal bool) error {
	path, err := githubaccess.Path()
	if err != nil {
		out.Item(sandbox.Item{Warn: true, Text: "GitHub access — settings unreadable", Detail: errLines(err)})
		return nil
	}
	current := ""
	if _, err := os.Stat(path); err == nil {
		cfg, err := githubaccess.Load(path)
		if err != nil {
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — settings unreadable", Detail: errLines(err)})
			return nil
		}
		current = cfg.Transport
	}
	for {
		h := githubaccess.Inspect(bin, githubaccess.Output, os.Getenv)
		if !prompt {
			out.Fact(unaskedAccess(h, current, noTerminal))
			return nil
		}
		form := githubAccessForm(h, current)
		ok, err := form.Run()
		if err != nil {
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — not chosen", Detail: errLines(err)})
			return nil
		}
		if !ok {
			out.Fact(ui.Fact{Role: ui.RoleHost, Key: "access", Value: cmp.Or(current, "not chosen")})
			return nil
		}
		mode := map[string]string{githubSSH: githubaccess.SSH, githubHTTPS: githubaccess.HTTPS, "later": githubaccess.Later}[form.Selection("sandbox Git")]
		if mode == githubaccess.SSH && h.SSHProblem() != "" {
			action, err := githubSSHRepair(h)
			if err != nil {
				out.Item(sandbox.Item{Warn: true, Text: "GitHub SSH — not repaired", Detail: errLines(err)})
				return nil
			}
			switch action {
			case "enable + restart":
				if err := runArgv([]string{bin, "settings", "set", "ssh.agentForwardingEnabled", "true"}); err != nil {
					if interrupted(err) {
						return err
					}
					out.Item(sandbox.Item{Warn: true, Text: "SSH-agent forwarding — not enabled", Detail: errDetail(err)})
					continue
				}
				if err := runArgv([]string{bin, "daemon", "restart"}); err != nil {
					if interrupted(err) {
						return err
					}
					out.Item(sandbox.Item{Warn: true, Text: "sbx daemon — not restarted", Detail: errDetail(err)})
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
				return nil
			}
		}
		if mode == githubaccess.HTTPS {
			if it, missing := httpsTokenMissing(hostGhAuth()); missing {
				out.Item(it)
				current = githubaccess.HTTPS
				continue
			}
		}
		if err := githubaccess.Save(path, mode); err != nil {
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — not saved", Detail: errLines(err)})
			return nil
		}
		out.Fact(ui.Fact{Role: ui.RoleHost, Key: "access", Value: mode, Detail: []string{ui.Tilde(path)}})
		return nil
	}
}

// httpsTokenMissing is the warning an HTTPS choice gets when g holds no GitHub token; missing is
// false when it holds one.
func httpsTokenMissing(g ghAuth) (it sandbox.Item, missing bool) {
	tok, err := g.lookup()
	switch {
	case errors.Is(err, errCredentialStoreTimeout):
		return ghTimeoutItem(githubHTTPS), true
	case tok == "":
		return sandbox.Item{Warn: true, Text: githubHTTPS + " — needs a GitHub API credential", Detail: []string{"`gh auth login` on the host, then choose it again"}}, true
	}
	return sandbox.Item{}, false
}

// unaskedAccess is the "access" fact when init did not ask: the transport, the host's keys and
// what is wrong with its SSH, with one how-to only when no terminal could ask.
func unaskedAccess(h githubaccess.Host, current string, noTerminal bool) ui.Fact {
	access := ui.Fact{Role: ui.RoleHost, Key: "access", Value: cmp.Or(current, "not chosen"),
		Detail: []string{hostKeys(len(h.Keys)) + "; sandbox verification pending"}}
	if state, _, _ := strings.Cut(h.SSHProblem(), ";"); state != "" {
		access.Detail = append(access.Detail, state)
	}
	if noTerminal {
		access.Detail = append(access.Detail, "`proveo init` from a terminal signs in and chooses it")
	}
	return access
}

// hostKeys counts the host agent's keys.
func hostKeys(n int) string {
	if n == 1 {
		return "1 host key"
	}
	return fmt.Sprintf("%d host keys", n)
}

func githubSSHRepair(h githubaccess.Host) (string, error) {
	opts := []string{"retry", githubHTTPS, "later"}
	if h.ForwardingKnown && !h.Forwarding {
		opts = append([]string{"enable + restart"}, opts...)
	}
	f := &choiceui.Form{Title: "GitHub SSH needs attention", Glyphs: posture.GlyphModeFrom(os.Getenv),
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
