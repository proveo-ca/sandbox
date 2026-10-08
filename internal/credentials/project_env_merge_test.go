// SPEC: _spec/internal/sbx/clone-workspace.puml
package credentials

import (
	"slices"
	"testing"
)

func TestMergeProjectEnvCarriesSandboxEditsHome(t *testing.T) {
	t.Parallel()
	strip := []string{"ANTHROPIC_API_KEY"}
	host := "# db\nDB_URL=postgres://x\nexport ANTHROPIC_API_KEY=sk-ant\nFEATURE=on\nOLD=1\n"
	base := "# db\nDB_URL=postgres://x\nFEATURE=on\nOLD=1\n"
	for _, tc := range []struct {
		name, host, base, edited    string
		want                        string
		applied, conflicts, refused []string
	}{
		{
			name: "no edit leaves the host byte for byte",
			host: host, edited: base,
			want: host,
		},
		{
			name: "a changed value replaces its line and keeps the brokered key",
			host: host, edited: "# db\nDB_URL=postgres://y\nFEATURE=on\nOLD=1\n",
			want:    "# db\nDB_URL=postgres://y\nexport ANTHROPIC_API_KEY=sk-ant\nFEATURE=on\nOLD=1\n",
			applied: []string{"DB_URL"},
		},
		{
			name: "an added name is appended and a removed one dropped",
			host: host, edited: "# db\nDB_URL=postgres://x\nFEATURE=on\nNEW=2\n",
			want:    "# db\nDB_URL=postgres://x\nexport ANTHROPIC_API_KEY=sk-ant\nFEATURE=on\nNEW=2\n",
			applied: []string{"NEW", "OLD"},
		},
		{
			name:      "a name the host changed during the run keeps the host value",
			host:      "# db\nDB_URL=postgres://host\nexport ANTHROPIC_API_KEY=sk-ant\nFEATURE=on\nOLD=1\n",
			edited:    "# db\nDB_URL=postgres://sbx\nFEATURE=on\nOLD=1\n",
			want:      "# db\nDB_URL=postgres://host\nexport ANTHROPIC_API_KEY=sk-ant\nFEATURE=on\nOLD=1\n",
			conflicts: []string{"DB_URL"},
		},
		{
			name: "the sandbox never writes a brokered key",
			host: host, edited: base + "ANTHROPIC_API_KEY=sk-leak\n",
			want:    host,
			refused: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name: "an append onto a host file without a trailing newline stays line-separated",
			host: "A=1", base: "A=1", edited: "A=1\nB=2",
			want:    "A=1\nB=2\n",
			applied: []string{"B"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := base
			if tc.base != "" {
				b = tc.base
			}
			m := MergeProjectEnv([]byte(tc.host), []byte(b), []byte(tc.edited), strip)
			if string(m.Body) != tc.want {
				t.Errorf("body:\n%s\nwant:\n%s", m.Body, tc.want)
			}
			if !slices.Equal(m.Applied, tc.applied) || !slices.Equal(m.Conflicts, tc.conflicts) || !slices.Equal(m.Refused, tc.refused) {
				t.Errorf("applied=%v conflicts=%v refused=%v; want %v %v %v",
					m.Applied, m.Conflicts, m.Refused, tc.applied, tc.conflicts, tc.refused)
			}
		})
	}
}
