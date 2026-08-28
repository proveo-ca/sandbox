//go:build livesbx

package sbx

import "testing"

// Run with: go test -tags livesbx ./internal/sbx/ -run TestPolicyBaselineLive -v
// It reads the HOST's real sbx baseline and asserts nothing about which one it
// is, only that the classification is readable and one of the three sbx names.
func TestPolicyBaselineLive(t *testing.T) {
	name, ok := PolicyBaseline()
	t.Logf("host sbx baseline = %q (known=%v)", name, ok)
	if !ok {
		t.Skip("sbx policy unreadable on this host")
	}
	for _, b := range Baselines() {
		if b == name {
			return
		}
	}
	t.Errorf("baseline %q is not one of %v", name, Baselines())
}
