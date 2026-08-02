package egress

import (
	"strings"
	"testing"
)

func TestAllowlistCoversEveryDetectedProvider(t *testing.T) {
	conf, matched, _ := ProviderAllowConf([]string{"anthropic", "openai", "xai", "google", "zai", "moonshot"}, "")
	for _, want := range []string{".anthropic.com", ".openai.com", ".x.ai", "generativelanguage.googleapis.com", ".z.ai", ".moonshot.ai"} {
		if !strings.Contains(conf, want) {
			t.Errorf("allowlist missing %q\n%s", want, conf)
		}
	}
	if len(matched) != 6 {
		t.Errorf("matched %d providers, want 6: %v", len(matched), matched)
	}
}
