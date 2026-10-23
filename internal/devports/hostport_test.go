// SPEC: _spec/internal/devports/dev-ports.puml
package devports

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHostPortsPrependOneThroughNineAndStopAt65535(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		guest int
		want  []int
	}{
		{8083, []int{18083, 28083, 38083, 48083, 58083}},
		{3000, []int{13000, 23000, 33000, 43000, 53000, 63000}},
		{80, []int{180, 280, 380, 480, 580, 680, 780, 880, 980}},
		{6553, []int{16553, 26553, 36553, 46553, 56553}},
		{18083, nil},
		{0, nil},
	} {
		if diff := cmp.Diff(tc.want, HostPorts(tc.guest)); diff != "" {
			t.Errorf("HostPorts(%d) mismatch (-want +got):\n%s", tc.guest, diff)
		}
	}
}

func TestHostPortForTakesTheFirstFreePrefix(t *testing.T) {
	t.Parallel()
	taken := map[int]bool{18083: true, 28083: true}
	if h, ok := HostPortFor(8083, func(p int) bool { return !taken[p] }); !ok || h != 38083 {
		t.Errorf("HostPortFor(8083) = %d, %v; want 38083, true", h, ok)
	}
	if h, ok := HostPortFor(8083, func(p int) bool { return p != 8083 }); !ok || h != 18083 {
		t.Errorf("HostPortFor(8083) with all free = %d, %v; want 18083, never the bare port", h, ok)
	}
}

func TestHostPortForFindsNothingWhenEveryPrefixIsTakenOrTooBig(t *testing.T) {
	t.Parallel()
	asked := map[int]bool{}
	if h, ok := HostPortFor(8083, func(p int) bool { asked[p] = true; return false }); ok {
		t.Errorf("HostPortFor(8083) with all taken = %d, want none", h)
	}
	for _, p := range []int{8083, 68083, 98083} {
		if asked[p] {
			t.Errorf("HostPortFor(8083) probed %d; only prefixes 1–9 under 65535 qualify", p)
		}
	}
	if h, ok := HostPortFor(18083, func(int) bool { return true }); ok {
		t.Errorf("HostPortFor(18083) = %d, want none: no prefix fits under 65535", h)
	}
}

func TestHostPortWhyNamesTheTakenPortsOrTheCeiling(t *testing.T) {
	t.Parallel()
	if got, want := HostPortWhy(8083), "host :18083 :28083 :38083 :48083 :58083 are all in use"; got != want {
		t.Errorf("HostPortWhy(8083) = %q, want %q", got, want)
	}
	if got, want := HostPortWhy(18083), "no host port 118083…918083 fits under 65535"; got != want {
		t.Errorf("HostPortWhy(18083) = %q, want %q", got, want)
	}
}
