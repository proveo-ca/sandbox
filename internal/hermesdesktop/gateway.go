// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (GATEWAY)
package hermesdesktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// UserDataEnv points Hermes Desktop at a userData directory other than its own.
const UserDataEnv = "HERMES_DESKTOP_USER_DATA_DIR"

const gatewayID = "proveo-sandbox"

// UserDataDir is the Hermes Desktop userData proveo launches with, apart from the operator's own.
func UserDataDir(proveoHome string) string { return filepath.Join(proveoHome, "desktop", "hermes") }

// RegisterGateway makes the SSH gateway user@host the primary connection in userData's
// connections.json, keeping every other connection the file holds.
func RegisterGateway(userData, user, host string) error {
	if err := os.MkdirAll(userData, 0o700); err != nil {
		return err
	}
	path := filepath.Join(userData, "connections.json")
	reg := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &reg)
	}
	var conns []any
	if old, ok := reg["connections"].([]any); ok {
		for _, c := range old {
			if m, ok := c.(map[string]any); ok && m["id"] == gatewayID {
				continue
			}
			conns = append(conns, c)
		}
	}
	conns = append(conns, map[string]any{
		"id": gatewayID, "kind": "ssh", "label": "proveo sandbox", "host": host, "user": user,
	})
	reg["version"] = 2
	reg["primary"] = gatewayID
	reg["lastUsed"] = gatewayID
	reg["launchMode"] = "primary"
	reg["connections"] = conns
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(userData, ".connections-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
