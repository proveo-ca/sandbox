// SPEC: _spec/internal/sbx/host-readiness.puml
package sbx

import (
	"strings"
	"testing"
)

func TestOnlyVerifiedSeriesPassWithoutAWarning(t *testing.T) {
	for _, c := range []struct {
		version  string
		verified bool
	}{
		{"0.42.0", true},
		{"0.46.0", true},
		{"0.49.2", true},
		{"0.49.7", true},
		{"0.47.0", false},
		{"0.48.0", false},
		{"0.50.0", false},
		{"1.42.0", false},
		{"garbage", false},
	} {
		if got := IsVerified(c.version); got != c.verified {
			t.Errorf("IsVerified(%q) = %v, want %v", c.version, got, c.verified)
		}
	}
}

func TestAnUnverifiedSbxNamesTheVerifiedSeries(t *testing.T) {
	note := UnverifiedNote("0.48.0")
	for _, want := range []string{"0.48.0", "not a version proveo has verified", "0.46", "0.49"} {
		if !strings.Contains(note, want) {
			t.Errorf("UnverifiedNote(0.48.0) = %q, lacks %q", note, want)
		}
	}
	for _, v := range []string{"0.49.2", "", "garbage"} {
		if note := UnverifiedNote(v); note != "" {
			t.Errorf("UnverifiedNote(%q) = %q, want no warning", v, note)
		}
	}
}

func TestThePinnedReleaseIsVerified(t *testing.T) {
	if !IsVerified(Release) || !IsVerified(MinVersion) {
		t.Errorf("Release %s and MinVersion %s must sit in Verified %v", Release, MinVersion, Verified)
	}
}
