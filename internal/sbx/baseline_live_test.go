//go:build livesbx

package sbx

import "testing"

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
