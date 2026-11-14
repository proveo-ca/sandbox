// SPEC: _spec/_devops/agent-update-prompt.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import "fmt"

// AgentUpdateAnswer is what the operator does about a newer agent.
type AgentUpdateAnswer int

const (
	AgentUpdateLater AgentUpdateAnswer = iota
	AgentUpdateNow
	AgentUpdateSkip
)

const agentUpdateWidth = 70

func agentUpdateTitle(agent string) string { return "a newer " + agent + " agent is available" }

// AgentUpdateCopy explains moving a run's agent from one release to a newer one, step by step.
func AgentUpdateCopy(agent, from, to string, steps []string) []string {
	lines := []string{agentUpdateTitle(agent), from + " → " + to, ""}
	for i, s := range steps {
		lines = append(lines, wrap(fmt.Sprintf("%d. %s", i+1, s), agentUpdateWidth, 3)...)
	}
	return append(lines, "", "Later asks again in 24 hours.", "Skip waits for a release after "+to+".")
}

// AskAgentUpdate asks on the operator's terminal what to do about a newer agent; asked is false when no terminal opens.
func AskAgentUpdate(agent, from, to string, steps []string) (answer AgentUpdateAnswer, asked bool) {
	got, asked := askDialog(agentUpdateDialog(agent, from, to, steps))
	return AgentUpdateAnswer(got), asked
}

func agentUpdateDialog(agent, from, to string, steps []string) dialog {
	return newDialog(agentUpdateTitle(agent), AgentUpdateCopy(agent, from, to, steps), int(AgentUpdateLater),
		dialogButton{Label: "Later", Key: 'l'},
		dialogButton{Label: "Update", Key: 'u'},
		dialogButton{Label: "Skip", Key: 's'},
	)
}
