// SPEC: _spec/internal/results/result-store.puml
package sbx

import (
	"errors"
	"testing"
)

func TestListReadsBothShapesAndSaysWhenThereIsNoListing(t *testing.T) {
	prev := listJSON
	t.Cleanup(func() { listJSON = prev })
	for _, c := range []struct {
		name   string
		out    string
		err    error
		rows   int
		wantOK bool
	}{
		{"a bare array", `[{"name":"proveo-a","agent":"proveo-claudecode","status":"running","workspaces":["/w"]},{"name":"proveo-b","status":"stopped"}]`, nil, 2, true},
		{"the wrapped shape", `{"sandboxes":[{"name":"proveo-a","status":"Running"}]}`, nil, 1, true},
		{"an empty host, or a busy daemon", `[]`, nil, 0, true},
		{"sbx failed", ``, errors.New("exit status 1"), 0, false},
		{"not JSON", `SANDBOX AGENT STATUS`, nil, 0, false},
	} {
		listJSON = func() ([]byte, error) { return []byte(c.out), c.err }
		rows, ok := List()
		if len(rows) != c.rows || ok != c.wantOK {
			t.Errorf("%s: List = %d rows, ok %v; want %d, %v", c.name, len(rows), ok, c.rows, c.wantOK)
		}
		if c.rows > 0 && !rows[0].Running() {
			t.Errorf("%s: the first row does not read as running: %+v", c.name, rows[0])
		}
	}
}

func TestAListedSandboxCarriesItsPublishedPorts(t *testing.T) {
	rows, err := ParseListing([]byte(`[{"name":"proveo-a","status":"running","ports":[` +
		`{"host_ip":"127.0.0.1","host_port":28083,"sandbox_port":8083,"protocol":"tcp4"}]}]`))
	if err != nil || len(rows) != 1 {
		t.Fatalf("ParseListing = %+v, %v", rows, err)
	}
	if got := rows[0].Ports; len(got) != 1 || got[0].HostPort != 28083 || got[0].SandboxPort != 8083 || got[0].HostIP != "127.0.0.1" {
		t.Errorf("ports = %+v, want 127.0.0.1:28083 → 8083", got)
	}
}
