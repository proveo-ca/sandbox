package sbx

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The pin is an assertion, not a preference: init downloads by exact tag, so a
// bumped Release with a stale URL builder would 404 on a host proveo cannot see.
func TestReleaseAssetURLNamesThePinnedTag(t *testing.T) {
	t.Parallel()
	got := ReleaseAssetURL("DockerSandboxes-linux-amd64.tar.gz")
	want := "https://github.com/docker/sbx-releases/releases/download/v" + Release +
		"/DockerSandboxes-linux-amd64.tar.gz"
	if got != want {
		t.Fatalf("ReleaseAssetURL = %q, want %q", got, want)
	}
}

func TestReleaseIsNotOlderThanTheVersionRunDemands(t *testing.T) {
	t.Parallel()
	if Older(Release, MinVersion) {
		t.Fatalf("Release %s is older than MinVersion %s — init would install an sbx that Available() refuses",
			Release, MinVersion)
	}
}

func TestPlanForEverySupportedHost(t *testing.T) {
	t.Parallel()
	const prefix = "/home/op/.docker/sbx"
	const dl = "/tmp/dl"

	cases := []struct {
		name     string
		host     Host
		asset    string
		bin      string
		packaged string   // the release asset the distro path would install; "" for none
		argv     []string // and how; must escalate, since that is what it costs
		alt      string
		steps    int
	}{
		{
			name:  "darwin arm64 extracts the tarball as the prefix",
			host:  Host{OS: "darwin", Arch: "arm64"},
			asset: "DockerSandboxes-darwin.tar.gz",
			bin:   prefix + "/bin/sbx",
			alt:   "brew trust docker/tap && brew install docker/tap/sbx",
			steps: 2,
		},
		{
			name:     "ubuntu 24.04 amd64 offers the 2404 deb beside the tarball",
			host:     Host{OS: "linux", Arch: "amd64", Distro: "ubuntu", Like: "debian", Version: "24.04"},
			asset:    "DockerSandboxes-linux-amd64.tar.gz",
			bin:      prefix + "/bin/sbx",
			packaged: "DockerSandboxes-linux-amd64-ubuntu2404.deb",
			argv:     []string{"sudo", "apt-get", "install", "-y", "./DockerSandboxes-linux-amd64-ubuntu2404.deb"},
			alt:      "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx",
			steps:    3,
		},
		{
			name:     "ubuntu 26.04 amd64 moves to the 2604 deb",
			host:     Host{OS: "linux", Arch: "amd64", Distro: "ubuntu", Like: "debian", Version: "26.04"},
			asset:    "DockerSandboxes-linux-amd64.tar.gz",
			bin:      prefix + "/bin/sbx",
			packaged: "DockerSandboxes-linux-amd64-ubuntu2604.deb",
			argv:     []string{"sudo", "apt-get", "install", "-y", "./DockerSandboxes-linux-amd64-ubuntu2604.deb"},
			alt:      "curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh && sudo apt install docker-sbx",
			steps:    3,
		},
		{
			name:     "rocky amd64 offers the one rpm the release ships",
			host:     Host{OS: "linux", Arch: "amd64", Distro: "rocky", Like: "rhel centos fedora", Version: "9.4"},
			asset:    "DockerSandboxes-linux-amd64.tar.gz",
			bin:      prefix + "/bin/sbx",
			packaged: "DockerSandboxes-linux-amd64-rockylinux8.rpm",
			argv:     []string{"sudo", "dnf", "install", "-y", "./DockerSandboxes-linux-amd64-rockylinux8.rpm"},
			steps:    3,
		},
		{
			name:  "an unrecognised distro still gets the tarball, with nothing packaged",
			host:  Host{OS: "linux", Arch: "arm64", Distro: "void"},
			asset: "DockerSandboxes-linux-arm64.tar.gz",
			bin:   prefix + "/bin/sbx",
			steps: 3,
		},
		{
			name:  "windows amd64 hands off to the MSI, which owns placement",
			host:  Host{OS: "windows", Arch: "amd64"},
			asset: "DockerSandboxes.msi",
			bin:   "",
			alt:   "winget install -h Docker.sbx",
			steps: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := PlanFor(tc.host, prefix, dl)
			if err != nil {
				t.Fatalf("PlanFor(%+v) = %v", tc.host, err)
			}
			if plan.Asset != tc.asset {
				t.Errorf("asset = %q, want %q", plan.Asset, tc.asset)
			}
			if !strings.HasSuffix(plan.URL, "/"+tc.asset) {
				t.Errorf("URL %q does not end in the asset it names", plan.URL)
			}
			if !strings.Contains(plan.URL, "/v"+Release+"/") {
				t.Errorf("URL %q does not carry the pinned tag", plan.URL)
			}
			if plan.Bin != tc.bin {
				t.Errorf("Bin = %q, want %q", plan.Bin, tc.bin)
			}
			if tc.packaged == "" {
				if plan.Packaged != nil {
					t.Errorf("Packaged = %+v, want none for this host", plan.Packaged)
				}
			} else {
				if plan.Packaged == nil {
					t.Fatalf("no Packaged; want %s", tc.packaged)
				}
				if plan.Packaged.Asset != tc.packaged {
					t.Errorf("Packaged.Asset = %q, want %q", plan.Packaged.Asset, tc.packaged)
				}
				if !strings.HasSuffix(plan.Packaged.URL, "/"+tc.packaged) {
					t.Errorf("Packaged.URL %q does not end in its asset", plan.Packaged.URL)
				}
				if got := strings.Join(plan.Packaged.Argv, " "); got != strings.Join(tc.argv, " ") {
					t.Errorf("Packaged.Argv = %q, want %q", got, strings.Join(tc.argv, " "))
				}
				if plan.Packaged.Argv[0] != "sudo" {
					t.Errorf("the distro path is the one that costs root; argv starts %q", plan.Packaged.Argv[0])
				}
			}
			if plan.Alt != tc.alt {
				t.Errorf("Alt = %q, want %q", plan.Alt, tc.alt)
			}
			if len(plan.Steps) != tc.steps {
				t.Fatalf("got %d steps, want %d: %+v", len(plan.Steps), tc.steps, plan.Steps)
			}
			for i, s := range plan.Steps {
				if len(s.Argv) == 0 {
					t.Errorf("step %d has no argv", i)
				}
				if strings.TrimSpace(s.What) == "" {
					t.Errorf("step %d says nothing about why it runs", i)
				}
			}
		})
	}
}

func TestPlanStepsNeverEscalate(t *testing.T) {
	t.Parallel()
	for _, h := range []Host{
		{OS: "darwin", Arch: "arm64"},
		{OS: "linux", Arch: "amd64", Distro: "ubuntu", Like: "debian", Version: "24.04"},
		{OS: "linux", Arch: "arm64", Distro: "rocky", Like: "rhel"},
		{OS: "windows", Arch: "amd64"},
	} {
		plan, err := PlanFor(h, "/p", "/dl")
		if err != nil {
			t.Fatalf("PlanFor(%+v) = %v", h, err)
		}
		for _, s := range plan.Steps {
			for _, a := range s.Argv {
				if a == "sudo" || a == "doas" {
					t.Errorf("%s/%s step %q escalates: %v", h.OS, h.Arch, s.What, s.Argv)
				}
			}
		}
	}
}

func TestPlanForRefusesPlatformsWithNoBuild(t *testing.T) {
	t.Parallel()
	for _, h := range []Host{
		{OS: "darwin", Arch: "amd64"},
		{OS: "linux", Arch: "riscv64"},
		{OS: "windows", Arch: "arm64"},
		{OS: "freebsd", Arch: "amd64"},
	} {
		if _, err := PlanFor(h, "/p", "/dl"); err == nil {
			t.Errorf("PlanFor(%s/%s) succeeded; there is no such asset on the release", h.OS, h.Arch)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	t.Parallel()
	kv := parseOSRelease(`NAME="Rocky Linux"
ID="rocky"
ID_LIKE="rhel centos fedora"
VERSION_ID="9.4"
# a comment
`)
	if kv["ID"] != "rocky" || kv["ID_LIKE"] != "rhel centos fedora" || kv["VERSION_ID"] != "9.4" {
		t.Fatalf("parseOSRelease = %+v", kv)
	}
}

func TestLinuxPrereqsOnAHostThatCannotRunSbx(t *testing.T) {
	t.Parallel()
	got := Prereqs(Host{OS: "linux", Arch: "amd64"}, Probe{
		Exists:   func(string) bool { return false },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		Groups:   func() ([]string, error) { return []string{"users"}, nil },
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("no selinux") },
	})

	byName := map[string]Prereq{}
	for _, c := range got {
		byName[c.Name] = c
	}
	for want, stage := range map[string]Stage{
		"KVM device": BlocksRun,
		"kvm group":  BlocksRun,
		"e2fsprogs":  BlocksInstall,
	} {
		c, ok := byName[want]
		if !ok {
			t.Fatalf("no %q check; got %+v", want, got)
		}
		if c.OK {
			t.Errorf("%q passed on a host that has none of it", want)
		}
		if c.Blocks != stage {
			t.Errorf("%q blocks %q, want %q", want, c.Blocks, stage)
		}
		if strings.TrimSpace(c.Fix) == "" {
			t.Errorf("%q states no fix, which leaves the operator where they started", want)
		}
	}
	if _, ok := byName["SELinux"]; ok {
		t.Errorf("an unreadable /sys/fs/selinux/enforce must not be reported as enforcing")
	}
	if n := len(Blocking(got, BlocksInstall)); n != 1 {
		t.Fatalf("Blocking(install) = %d, want only e2fsprogs", n)
	}
	if n := len(Blocking(got, BlocksRun)); n != 2 {
		t.Fatalf("Blocking(run) = %d, want KVM and the group", n)
	}
}

func TestAHostThatCannotRunYetCanStillBeInstalledOn(t *testing.T) {
	t.Parallel()
	got := Prereqs(Host{OS: "linux", Arch: "amd64"}, Probe{
		Exists:   func(string) bool { return false }, // no /dev/kvm
		LookPath: func(string) (string, error) { return "/sbin/mkfs.ext4", nil },
		Groups:   func() ([]string, error) { return []string{"users"}, nil },
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("no selinux") },
	})
	if n := len(Blocking(got, BlocksInstall)); n != 0 {
		t.Fatalf("install blocked by %v; nothing here stops the install", Names(Blocking(got, BlocksInstall)))
	}
	if n := len(Blocking(got, BlocksRun)); n != 2 {
		t.Fatalf("Blocking(run) = %d, want KVM and the group", n)
	}
}

func TestLinuxPrereqsPassOnAReadyHostAndWarnUnderSELinux(t *testing.T) {
	t.Parallel()
	got := Prereqs(Host{OS: "linux", Arch: "amd64"}, Probe{
		Exists:   func(p string) bool { return p == kvmDevice },
		LookPath: func(string) (string, error) { return "/sbin/mkfs.ext4", nil },
		Groups:   func() ([]string, error) { return []string{"users", "kvm"}, nil },
		ReadFile: func(string) ([]byte, error) { return []byte("1\n"), nil },
	})
	for _, stage := range []Stage{BlocksInstall, BlocksRun} {
		if n := len(Blocking(got, stage)); n != 0 {
			t.Fatalf("a ready host is blocked from %q by %v", stage, Names(Blocking(got, stage)))
		}
	}
	var sel *Prereq
	for i := range got {
		if got[i].Name == "SELinux" {
			sel = &got[i]
		}
	}
	if sel == nil {
		t.Fatal("an enforcing host must be told the bundled profile is AppArmor's")
	}
	if sel.Blocks != BlocksNothing {
		t.Errorf("SELinux enforcing blocks %q; it must warn, since sbx may still work there", sel.Blocks)
	}
	if !strings.Contains(sel.Fix, "ausearch") {
		t.Errorf("the SELinux fix must point at the denial log, got %q", sel.Fix)
	}
}

// A permissive host is not a warning. Reporting one would train the operator to
// ignore the row that matters on an enforcing host.
func TestPermissiveSELinuxIsNotReported(t *testing.T) {
	t.Parallel()
	got := Prereqs(Host{OS: "linux", Arch: "amd64"}, Probe{
		Exists:   func(p string) bool { return p == kvmDevice },
		LookPath: func(string) (string, error) { return "/sbin/mkfs.ext4", nil },
		Groups:   func() ([]string, error) { return []string{"kvm"}, nil },
		ReadFile: func(string) ([]byte, error) { return []byte("0\n"), nil },
	})
	for _, c := range got {
		if c.Name == "SELinux" {
			t.Fatalf("permissive SELinux reported as %+v", c)
		}
	}
}

func TestDarwinPrereqRefusesIntel(t *testing.T) {
	t.Parallel()
	intel := Prereqs(Host{OS: "darwin", Arch: "amd64"}, Probe{})
	if len(Blocking(intel, BlocksInstall)) != 1 {
		t.Fatalf("darwin/amd64 must block the install — there is no asset, got %+v", intel)
	}
	silicon := Prereqs(Host{OS: "darwin", Arch: "arm64"}, Probe{})
	for _, stage := range []Stage{BlocksInstall, BlocksRun} {
		if n := len(Blocking(silicon, stage)); n != 0 {
			t.Fatalf("darwin/arm64 is blocked from %q by %v", stage, Names(Blocking(silicon, stage)))
		}
	}
}

func TestServerStateReadsTheDaemonsOwnVerdict(t *testing.T) {
	restore := sh
	t.Cleanup(func() { sh = restore })

	sh.VersionJSON = func() ([]byte, error) {
		return []byte(`{"client":{"version":"0.42.0"},"server":{"state":"unavailable"}}`), nil
	}
	got, err := ServerState()
	if err != nil {
		t.Fatal(err)
	}
	if got != "unavailable" {
		t.Fatalf("ServerState = %q, want unavailable", got)
	}

	sh.VersionJSON = func() ([]byte, error) { return []byte(`{"client":{"version":"0.42.0"}}`), nil }
	if _, err := ServerState(); err == nil {
		t.Fatal("a payload with no server state must be an error, not an empty verdict")
	}
}

func TestDefaultPrefixFollowsTheReleaseInstaller(t *testing.T) {
	t.Parallel()
	if got := DefaultPrefix("/home/op"); got != "/home/op/.docker/sbx" {
		t.Fatalf("DefaultPrefix = %q, want the installer's own ~/.docker/sbx", got)
	}
	if got := DefaultPrefix("  "); got != "" {
		t.Fatalf("DefaultPrefix with no home = %q, want empty", got)
	}
}

func TestOnlyLinuxCarriesAVerifiableDigest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host    Host
		subject string
	}{
		{Host{OS: "linux", Arch: "amd64"}, "static/linux/amd64/docker-sbx_" + Release + ".tgz"},
		{Host{OS: "linux", Arch: "arm64"}, "static/linux/arm64/docker-sbx_" + Release + ".tgz"},
	} {
		plan, err := PlanFor(tc.host, "/p", "/dl")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Provenance == nil {
			t.Fatalf("%s/%s must verify — its provenance subject covers the released asset", tc.host.OS, tc.host.Arch)
		}
		if plan.Provenance.Subject != tc.subject {
			t.Errorf("subject = %q, want %q", plan.Provenance.Subject, tc.subject)
		}
		if !strings.HasSuffix(plan.Provenance.URL, "/"+plan.Provenance.Asset) {
			t.Errorf("provenance URL %q does not end in its asset", plan.Provenance.URL)
		}
	}

	for _, h := range []Host{{OS: "darwin", Arch: "arm64"}, {OS: "windows", Arch: "amd64"}} {
		plan, err := PlanFor(h, "/p", "/dl")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Provenance != nil {
			t.Errorf("%s/%s claims a digest; measured 2026-09-07, its provenance describes different bytes", h.OS, h.Arch)
		}
	}
}

func TestDigestFromProvenanceMatchesBySubjectName(t *testing.T) {
	t.Parallel()
	const body = `{"_type":"https://in-toto.io/Statement/v1","subject":[
		{"name":"static/linux/arm64/docker-sbx_0.42.0.tgz","digest":{"sha256":"5F16183D"}},
		{"name":"static/linux/amd64/docker-sbx_0.42.0.tgz","digest":{"sha256":"A88C56F0"}}]}`

	// The wanted subject is deliberately not first: taking subject[0] would
	// verify amd64 bytes against an arm64 digest the moment a build reorders.
	got, err := DigestFromProvenance([]byte(body), "static/linux/amd64/docker-sbx_0.42.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a88c56f0" {
		t.Fatalf("digest = %q, want the amd64 subject's, lowercased", got)
	}
	if _, err := DigestFromProvenance([]byte(body), "static/linux/riscv64/docker-sbx_0.42.0.tgz"); err == nil {
		t.Fatal("an absent subject must be an error, not an empty digest that verifies nothing")
	}
	if _, err := DigestFromProvenance([]byte("not json"), "x"); err == nil {
		t.Fatal("unparseable provenance must be an error")
	}
}
