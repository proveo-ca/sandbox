package run

import (
	"os"
	"testing"
)

// TestMain pins an unknown host Ollama version for every test.
func TestMain(m *testing.M) {
	hostOllamaVersion = func() string { return "" }
	os.Exit(m.Run())
}
