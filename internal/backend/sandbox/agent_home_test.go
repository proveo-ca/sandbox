// SPEC: _spec/defs/hermes/hermes-persistence.puml (EVERY SESSION)
package sandbox

import (
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestDurableAgentHomeReachesEverySession(t *testing.T) {
	t.Parallel()
	state := sbx.StateHomeVar + "=/Users/op/.proveo"
	for _, c := range []struct {
		name, target string
		env, want    []string
	}{
		{"hermes with a state home", "hermes", []string{state, "HERMES_HOME=/opt/data"},
			[]string{state, "HERMES_HOME=/Users/op/.proveo/hermes/data"}},
		{"hermes without a state home", "hermes", []string{"HERMES_HOME=/opt/data"}, []string{"HERMES_HOME=/opt/data"}},
		{"a def with no durable home var", "codex", []string{state}, []string{state}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := durableAgentHome(c.target, c.env); !slices.Equal(got, c.want) {
				t.Errorf("durableAgentHome(%q, %v) = %v, want %v", c.target, c.env, got, c.want)
			}
		})
	}
}
