//go:build e2e

// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/tests/testing-strategy.puml

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestInitPrintChangesNothingOnThisHost(t *testing.T) {
	host := sbx.DetectHost()
	if _, err := sbx.PlanFor(host, "/tmp/unused", "/tmp/unused"); err != nil {
		t.Skipf("no Docker Sandboxes build for %s/%s: %v", host.OS, host.Arch, err)
	}

	bin := buildProveo(t)
	work := t.TempDir()
	prefix := filepath.Join(work, "prefix")

	cmd := exec.Command(bin, "init", "--print", "--prefix", prefix)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo init --print: %v\n%s", err, out)
	}
	got := string(out)
	t.Logf("proveo init --print on %s/%s:\n%s", host.OS, host.Arch, got)

	// It must name the pinned release, the asset it would fetch, and the host.
	for _, want := range []string{sbx.Release, host.OS + "/" + host.Arch, "github.com/docker/sbx-releases"} {
		if !strings.Contains(got, want) {
			t.Errorf("--print never mentions %q", want)
		}
	}

	// And it must not have acted on any of it.
	if _, err := os.Stat(prefix); err == nil {
		t.Errorf("--print created %s; it must change nothing", prefix)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("--print left %s behind in the working directory", e.Name())
	}
}

func TestInitReportsThisHostsPrerequisites(t *testing.T) {
	host := sbx.DetectHost()
	checks := sbx.Prereqs(host, sbx.DefaultProbe())
	if len(checks) == 0 {
		t.Skipf("no prerequisites defined for %s", host.OS)
	}
	for _, c := range checks {
		t.Logf("%-16s ok=%-5v blocks=%-8q %s", c.Name, c.OK, string(c.Blocks), c.Detail)
		if strings.TrimSpace(c.Detail) == "" {
			t.Errorf("%q states no detail", c.Name)
		}
		if !c.OK && strings.TrimSpace(c.Fix) == "" {
			t.Errorf("%q failed and names no fix", c.Name)
		}
	}
	if host.OS == "linux" {
		var names []string
		for _, c := range checks {
			names = append(names, c.Name)
		}
		for _, want := range []string{"KVM device", "kvm group", "e2fsprogs"} {
			if !contains(names, want) {
				t.Errorf("linux host reported no %q check; got %v", want, names)
			}
		}
	}
}

func TestInitProvenanceSubjectStillExists(t *testing.T) {
	host := sbx.DetectHost()
	plan, err := sbx.PlanFor(host, "/tmp/unused", "/tmp/unused")
	if err != nil {
		t.Skipf("no build for %s/%s", host.OS, host.Arch)
	}
	if plan.Provenance == nil {
		t.Skipf("%s/%s publishes no digest that covers its asset", host.OS, host.Arch)
	}

	body, err := fetchURL(plan.Provenance.URL)
	if err != nil {
		t.Skipf("cannot reach the release (%v)", err)
	}
	digest, err := sbx.DigestFromProvenance(body, plan.Provenance.Subject)
	if err != nil {
		t.Fatalf("v%s no longer publishes subject %q: %v — init would refuse to install",
			sbx.Release, plan.Provenance.Subject, err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest %q is not a sha256", digest)
	}
	t.Logf("%s → %s", plan.Provenance.Subject, digest)
}

// fetchURL is a plain GET; the provenance statement is a few hundred KB and the
// suite has no reason to hold a client of its own.
func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
