package sbx

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// SPEC: _spec/internal/sbx/kit-domain-form.puml
func TestDomainPatternsTranslatesSquidsDotIntoBothHalves(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		host string
		want []string
	}{
		// The CLI reference is explicit that `example.com` and `*.example.com`
		// do not cover each other, and Squid's dot meant BOTH.
		{"squid suffix becomes apex and wildcard", ".x.ai", []string{"x.ai", "*.x.ai"}},
		{"multi-label squid suffix", ".cursor.com", []string{"cursor.com", "*.cursor.com"}},
		// A bare host is a statement that subdomains are NOT included.
		{"bare host is never widened", "api2.cursor.sh", []string{"api2.cursor.sh"}},
		{"bare apex is never widened", "openrouter.ai", []string{"openrouter.ai"}},
		// Already sbx's grammar: re-translating would corrupt it.
		{"wildcard passes through", "*.example.com", []string{"*.example.com"}},
		{"allow-all passes through", "**", []string{"**"}},
		// A port suffix rides both halves.
		{"port rides both halves", ".x.ai:443", []string{"x.ai:443", "*.x.ai:443"}},
		{"bare host keeps its port", "api.x.ai:8080", []string{"api.x.ai:8080"}},
		// Degenerate input must not become a pattern that matches everything.
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"dot only", ".", nil},
		{"padded", "  .x.ai  ", []string{"x.ai", "*.x.ai"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, DomainPatterns(tc.host)); diff != "" {
				t.Errorf("DomainPatterns(%q) (-want +got):\n%s", tc.host, diff)
			}
		})
	}
}

func TestAllowPatternsDeduplicatesAcrossTheTwoForms(t *testing.T) {
	t.Parallel()
	// The registry lists openrouter BOTH ways; the translation must not emit
	// `openrouter.ai` twice.
	got := AllowPatterns([]string{"openrouter.ai", ".openrouter.ai", ".x.ai", "", "  "})
	want := []string{"*.openrouter.ai", "openrouter.ai", "*.x.ai", "x.ai"}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Errorf("AllowPatterns emitted %q %d times, want exactly 1: %v", w, seen[w], got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("AllowPatterns = %v, want exactly %v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("AllowPatterns is not sorted: %v", got)
			break
		}
	}
}

// The regression this whole file exists for: nothing sbx is handed may carry
// Squid's leading dot, because sbx does not enforce that shape at all.
func TestNoTranslatedPatternKeepsSquidsLeadingDot(t *testing.T) {
	t.Parallel()
	for _, h := range []string{".x.ai", ".cursor.sh", ".cursor.com", ".anthropic.com", ".openrouter.ai"} {
		for _, p := range DomainPatterns(h) {
			if strings.HasPrefix(p, ".") {
				t.Errorf("DomainPatterns(%q) still emits %q — a leading dot is Squid's "+
					"`dstdomain` notation and matches nothing in an sbx Kit", h, p)
			}
		}
	}
}
