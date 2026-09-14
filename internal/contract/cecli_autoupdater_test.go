// SPEC: _spec/defs/cecli/cecli-paradigm.puml
package contract_test

import (
	"regexp"
	"strings"
	"testing"
)

func TestCecliSampleDoesNotRecommendAnUpdaterTheImageCannotSatisfy(t *testing.T) {
	t.Parallel()
	sample := readRepoFile(t, "defs/cecli/sample.cecli.conf.yml")
	if !regexp.MustCompile(`(?m)^check-update:\s*false\s*$`).MatchString(sample) {
		t.Errorf("defs/cecli/sample.cecli.conf.yml does not set `check-update: false` — the " +
			"sample tells the operator to copy it to their project root, so recommending an " +
			"updater that cannot write /opt/cecli hands them a failure on every run")
	}

	// The premise, pinned: the venv is root-built and never handed to the
	// runtime user. If that ever changes, this test should fail and the
	// recommendation above be re-argued rather than silently drift.
	df := readRepoFile(t, "defs/cecli/Dockerfile")
	if !strings.Contains(df, "python3 -m venv /opt/cecli") {
		t.Fatal("defs/cecli/Dockerfile no longer builds the venv at /opt/cecli — re-check who owns it")
	}
	if regexp.MustCompile(`chown[^\n]*\s/opt/cecli`).MatchString(df) {
		t.Error("defs/cecli/Dockerfile now chowns /opt/cecli to the runtime user, so the venv is " +
			"writable at run time. That defeats the CECLI_VERSION pin the same way a writable npm " +
			"prefix would defeat claudecode's — decide deliberately, then update this test")
	}
}
