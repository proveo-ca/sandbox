//go:build e2e

// SPEC: _spec/_devops/agent-update-prompt.puml
package e2e

import (
	"os"
	"testing"

	"github.com/proveo-ca/proveo/internal/agentupdate"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestMain turns the agent-update check off for every run the suite launches, unless the caller set it.
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv(agentupdate.EnvSwitch); !set {
		_ = os.Setenv(agentupdate.EnvSwitch, "0")
	}
	tmux.SessionEnv = append(tmux.SessionEnv, agentupdate.EnvSwitch+"="+os.Getenv(agentupdate.EnvSwitch))
	os.Exit(m.Run())
}
