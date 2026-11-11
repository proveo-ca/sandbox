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
