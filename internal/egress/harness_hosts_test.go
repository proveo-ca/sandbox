package egress

import (
	"strings"
	"testing"
)

func TestHarnessHostsReachTheACL(t *testing.T) {
	conf, matched, _ := ProviderAllowConf([]string{"anthropic"}, ".opencode.ai models.dev")
	for _, want := range []string{".anthropic.com", ".opencode.ai models.dev"} {
		if !strings.Contains(conf, want) {
			t.Errorf("acl missing %q\n%s", want, conf)
		}
	}
	if len(matched) != 2 {
		t.Errorf("matched=%v, want provider + custom", matched)
	}
}
