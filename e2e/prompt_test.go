//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml

package e2e

import "testing"

func TestPromptWorkdirAcceptsBothBackends(t *testing.T) {
	for _, c := range []struct {
		name    string
		screen  string
		workdir string
		ok      bool
	}{
		{
			name:    "docker, workspace at /app",
			screen:  "Workspace: /app\nI have no name!@8101a68d15b5:/app$",
			workdir: "/app",
			ok:      true,
		},
		{
			name:    "sandbox, workspace at its own host path",
			screen:  "Starting shell agent in sandbox 'proveo-1789458382-46514'...\nclaude@proveo-1789458382-46514:003$",
			workdir: "003",
			ok:      true,
		},
		{
			name:    "sandbox, opencode",
			screen:  "opencode@proveo-1789459221-51842:001$",
			workdir: "001",
			ok:      true,
		},
		{
			name:    "root shell",
			screen:  "root@7c90e616a520:/workspace#",
			workdir: "/workspace",
			ok:      true,
		},
		{
			name:   "the run's own output, no shell yet",
			screen: "● backend: docker sandboxes (sbx)\n   ✓ Created sandbox proveo-1789458382-46514\nStarting shell agent in sandbox 'proveo-1789458382-46514'...",
			ok:     false,
		},
		{
			name:   "an email address in the scrollback is not a prompt",
			screen: "GIT_AUTHOR_EMAIL=e2e@proveo.test",
			ok:     false,
		},
		{
			name:    "the newest prompt wins",
			screen:  "claude@proveo-1:001$\nbash -c 'true'\nclaude@proveo-1:001$",
			workdir: "001",
			ok:      true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := promptWorkdir(c.screen)
			if ok != c.ok {
				t.Fatalf("promptWorkdir ok = %v, want %v (got %q)", ok, c.ok, got)
			}
			if ok && got != c.workdir {
				t.Errorf("promptWorkdir = %q, want %q", got, c.workdir)
			}
		})
	}
}
