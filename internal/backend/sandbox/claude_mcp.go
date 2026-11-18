// SPEC: _spec/_plans/claudecode-mcp-toggle.puml
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/sbx"
)

const claudeStrictMCP = "--strict-mcp-config"

func (in Input) claudeMCPOff() bool {
	return Harness(in) == "claudecode" && !in.MCPGateway
}

func (in Input) ValidateLaunch() error {
	if !in.claudeMCPOff() {
		return nil
	}
	for _, arg := range in.Extra {
		if arg == "--" {
			break
		}
		flag, _, assigned := strings.Cut(arg, "=")
		if flag == "--mcp-config" || flag == "--no-strict-mcp-config" || (flag == claudeStrictMCP && assigned) {
			return fmt.Errorf("Claude Code MCP is off and conflicts with %s", flag)
		}
	}
	return nil
}

func launchFlags(in Input) []string {
	flags := slices.Clone(in.Extra)
	if in.claudeMCPOff() {
		return append([]string{claudeStrictMCP}, flags...)
	}
	return withMobileMCP(Harness(in), in.AgentEnv, flags)
}

func claudeMCPRestartError(name string) error {
	return fmt.Errorf("cannot apply the requested Claude Code MCP mode to %s while it is running. Stop the sandbox and run again", name)
}

func stoppedForClaudeLaunch(name string, listed *sbx.Listed) error {
	if listed == nil {
		return fmt.Errorf("cannot verify whether %s is stopped. Check sbx ls before launching Claude Code", name)
	}
	switch strings.ToLower(listed.Status) {
	case "stopped", "created", "exited":
		return nil
	case "running":
		return claudeMCPRestartError(name)
	default:
		return fmt.Errorf("cannot launch Claude Code with the requested MCP mode while %s is %s", name, listed.Status)
	}
}

func (a HomeAccess) keptClaudeFiles(was *launchReceipt) (HomeAccess, error) {
	if a.FilesRoot == "" {
		return a, nil
	}
	if was == nil || !filepath.IsAbs(was.KitDir) || filepath.Base(was.KitDir) != sbx.AgentName("claudecode") ||
		filepath.Base(filepath.Dir(was.KitDir)) != "kit" {
		return a, fmt.Errorf("missing the kept sandbox's home-files mount record")
	}
	a.FilesRoot = filepath.Join(filepath.Dir(filepath.Dir(was.KitDir)), "home-files")
	info, err := os.Lstat(a.FilesRoot)
	if err != nil {
		return a, err
	}
	if !info.IsDir() {
		return a, fmt.Errorf("kept home-files path is not a directory: %s", a.FilesRoot)
	}
	return a, nil
}

func prepareKeptClaude(in Input, name string, was *launchReceipt) (HomeAccess, error) {
	a, err := in.HomeAccess.keptClaudeFiles(was)
	if err == nil {
		err = a.refreshFiles()
	}
	if err != nil {
		return HomeAccess{}, fmt.Errorf("refresh Claude Code config for %s: %w", name, err)
	}
	if in.MCPGateway && a.FilesRoot != "" {
		script := `test "$` + proveohome.ConfigFilesRootVar + `" = ` + sbx.ShellJoin([]string{a.FilesRoot}) +
			` && . /entrypoint-lib.sh && proveo_sync_config restore`
		if out, err := guestRunIn(in.ctx(), "exec", "-w", "/", name, "--", "bash", "-c", script); err != nil {
			return HomeAccess{}, fmt.Errorf("restore Claude Code config for %s: %s", name, failure(err, out))
		}
	}
	return a, nil
}
