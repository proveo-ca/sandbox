// SPEC: _spec/internal/results/result-store.puml
package sbx

import (
	"encoding/json"
	"strings"
)

// Listed is one sandbox as `sbx ls --json` reports it.
type Listed struct {
	Name       string   `json:"name"`
	Agent      string   `json:"agent"`
	Status     string   `json:"status"`
	Workspaces []string `json:"workspaces"`
}

// Running reports whether the sandbox is up.
func (l Listed) Running() bool { return strings.EqualFold(l.Status, "running") }

// listJSON is `sbx ls --json`; tests replace it.
var listJSON = func() ([]byte, error) { return bounded(Binary, "ls", "--json") }

// List reads every sandbox sbx knows. ok is false when sbx gave no listing that parses.
// An empty listing is returned as it is: it can be a busy daemon as well as an empty host.
func List() (rows []Listed, ok bool) {
	out, err := listJSON()
	if err != nil {
		return nil, false
	}
	rows, err = ParseListing(out)
	return rows, err == nil
}

// ParseListing reads `sbx ls --json`: a bare array since sbx 0.48, {"sandboxes": [...]} before it.
func ParseListing(out []byte) ([]Listed, error) {
	var bare []Listed
	if err := json.Unmarshal(out, &bare); err == nil {
		return bare, nil
	}
	var wrapped struct {
		Sandboxes []Listed `json:"sandboxes"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Sandboxes, nil
}
