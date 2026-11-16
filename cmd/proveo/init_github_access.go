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
	"github.com/proveo-ca/proveo/internal/gitaccess"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	githubSSH   = "SSH agent"
	githubHTTPS = "HTTPS token"
)

// githubAccessForm is the sandbox Git question as a bare form: what the host has as facts, and
// one row with the transport current or the host's own Git protocol picks.
func githubAccessForm(h gitaccess.Host, current string) *choiceui.Form {
	login := h.Login
	if login == "" {
		login = "unavailable; API sign-in is independent of SSH"
	}
	protocol := h.Protocol
	if protocol == "" {
		protocol = "unconfigured"
	}
	selected := 1
	if current == gitaccess.SSH || (current == "" && h.Protocol == gitaccess.SSH) {
		selected = 0
	}
	if current == gitaccess.Later {
		selected = 2
	}
	header := []string{"api: " + login, "git: " + protocol, fmt.Sprintf("agent: %d key(s)", len(h.Keys)),
		"Sandbox SSH verification pending until proveo run"}
	for _, key := range h.Keys {
		header = append(header, "key: "+key)
	}
	if why := h.SSHProblem(); why != "" {
		header = append(header, "  SSH status: "+why)
	}
	return &choiceui.Form{Title: titleAccess, Header: header,
		Rows: []choiceui.Row{{Label: accessRow, Divider: true, Heading: "GitHub", Options: []string{githubSSH, githubHTTPS, "later"},
			Selected: selected, Cols: cols(3), CellRunes: cellRunes,
			Help: map[string]string{
				githubSSH:   "agents may authenticate with keys exposed by the host agent; private keys stay on the host",
				githubHTTPS: "use sbx's github token for Git HTTPS; preserve host gh preferences and stored remote URLs",
				"later":     "defer sandbox Git authentication setup",
			}}},
	}
}

// accessRow is the sandbox Git question's row.
const accessRow = "sandbox Git"

// accessStage is what the sandbox Git step reads and runs: sbx, whether init may ask, the git
// block, the sub-page it asks on, and what it tells the GitHub row and the report.
type accessStage struct {
	bin        string
	prompt     bool
	out        *sandbox.Block
	noTerminal bool
	ask        func(title string, header []string, rows []choiceui.Row, enter func(*choiceui.Form) string) (*choiceui.Form, choiceui.Outcome, error)
	said       *ghSaid
	restarted  func() // the sbx daemon restarted, so the latest diagnose is stale
}

// say adds the sandbox Git answer to the GitHub row's outcome.
func (a accessStage) say(m choiceui.Mark, access string) {
	if a.said == nil {
		return
	}
	a.said.mark = choiceui.Worst(a.said.mark, m)
	a.said.text += " · sandbox Git: " + access
}

// applyGitHubAccess asks how sandbox Git reaches GitHub, as a sub-page of page 2, saves the
// answer, and adds it to the GitHub row. Esc there leaves it for later; a confirmed quit there is
// errInitQuit. Unprompted it only reports, with the host's keys under the row; a Ctrl+C that ended
// a program it handed the terminal to is its error.
// SPEC: _spec/cmd/proveo/github-access.puml
func applyGitHubAccess(a accessStage) error {
	bin, out := a.bin, a.out
	path, err := gitaccess.Path()
	if err != nil {
		out.Item(sandbox.Item{Warn: true, Text: "GitHub access — settings unreadable", Detail: errLines(err)})
		return nil
	}
	current := ""
	if _, err := os.Stat(path); err == nil {
		cfg, err := gitaccess.Load(path)
		if err != nil {
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — settings unreadable", Detail: errLines(err)})
			return nil
		}
		current = cfg.Transport
	}
	saveWord := func(*choiceui.Form) string { return runWord(1) }
	for {
		h := gitaccess.Inspect(bin, gitaccess.Output, os.Getenv)
		if !a.prompt {
			f := unaskedAccess(h, current, a.noTerminal)
			a.say(choiceui.MarkLater, f.Value)
			out.Item(sandbox.Item{Text: accessRow + " — " + f.Value, Detail: f.Detail})
			return nil
		}
		bare := githubAccessForm(h, current)
		form, answer, err := a.ask(bare.Title, bare.Header, bare.Rows, saveWord)
		switch {
		case err != nil:
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — not chosen", Detail: errLines(err)})
			return nil
		case answer == choiceui.Quit:
			return errInitQuit
		case answer != choiceui.Accepted:
			a.say(choiceui.MarkLater, "later")
			return nil
		}
		mode := map[string]string{githubSSH: gitaccess.SSH, githubHTTPS: gitaccess.HTTPS, "later": gitaccess.Later}[form.Selection(accessRow)]
		if mode == gitaccess.SSH && h.SSHProblem() != "" {
			action, err := githubSSHRepair(h, a.ask)
			if err != nil {
				if errors.Is(err, errInitQuit) {
					return err
				}
				out.Item(sandbox.Item{Warn: true, Text: "GitHub SSH — not repaired", Detail: errLines(err)})
				return nil
			}
			switch action {
			case "enable + restart":
				if err := runArgvCaptured([]string{bin, "settings", "set", "ssh.agentForwardingEnabled", "true"}); err != nil {
					out.Item(sandbox.Item{Warn: true, Text: "SSH-agent forwarding — not enabled", Detail: errDetail(err)})
					continue
				}
				err := runArgvCaptured([]string{bin, "daemon", "restart"})
				if a.restarted != nil {
					a.restarted()
				}
				if err != nil {
					out.Item(sandbox.Item{Warn: true, Text: "sbx daemon — not restarted", Detail: errDetail(err)})
					continue
				}
				current = gitaccess.SSH
				continue
			case "retry":
				current = gitaccess.SSH
				continue
			case githubHTTPS:
				mode = gitaccess.HTTPS
			case "later":
				mode = gitaccess.Later
			default:
				a.say(choiceui.MarkLater, "later")
				return nil
			}
		}
		if mode == gitaccess.HTTPS {
			if it, missing := httpsTokenMissing(hostGhAuth()); missing {
				out.Item(it)
				current = gitaccess.HTTPS
				continue
			}
		}
		if err := gitaccess.Save(path, mode); err != nil {
			out.Item(sandbox.Item{Warn: true, Text: "GitHub access — not saved", Detail: errLines(err)})
			a.say(choiceui.MarkNeeds, "not saved")
			return nil
		}
		switch mode {
		case gitaccess.Later:
			a.say(choiceui.MarkLater, "later")
		case gitaccess.SSH:
			a.say(choiceui.MarkDone, githubSSH)
		default:
			a.say(choiceui.MarkDone, githubHTTPS)
		}
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
func unaskedAccess(h gitaccess.Host, current string, noTerminal bool) ui.Fact {
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

// githubSSHRepair asks, as a sub-page, what to do about an SSH agent sandboxes cannot use; "" is later.
func githubSSHRepair(h gitaccess.Host, ask func(string, []string, []choiceui.Row, func(*choiceui.Form) string) (*choiceui.Form, choiceui.Outcome, error)) (string, error) {
	opts := []string{"retry", githubHTTPS, "later"}
	if h.ForwardingKnown && !h.Forwarding {
		opts = append([]string{"enable + restart"}, opts...)
	}
	header := []string{h.SSHProblem(), "Load a host key with ssh-add; register its public key with GitHub.",
		"Configure a stable agent with sbx settings set ssh.agentSocketPath <socket>."}
	rows := []choiceui.Row{{Label: "repair", Divider: true, Heading: "GitHub", Options: opts, Cols: cols(len(opts)), CellRunes: cellRunes,
		Help: map[string]string{
			"enable + restart": "enables host-wide SSH-agent forwarding and restarts the sbx daemon; this affects existing sandboxes",
			"retry":            "check the host agent and sbx settings again",
			githubHTTPS:        "use the GitHub API token for sandbox Git operations",
			"later":            "save Git authentication as deferred",
		}}}
	enter := func(f *choiceui.Form) string {
		switch f.Selection("repair") {
		case "enable + restart":
			return runWord(2)
		case "retry":
			return runWord(0)
		}
		return runWord(1)
	}
	f, out, err := ask(titleRepair, header, rows, enter)
	switch {
	case err != nil:
		return "", err
	case out == choiceui.Quit:
		return "", errInitQuit
	case out != choiceui.Accepted:
		return "", nil
	}
	return strings.TrimSpace(f.Selection("repair")), nil
}
