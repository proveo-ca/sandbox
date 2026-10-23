// SPEC: _spec/internal/devports/dev-ports.puml
package devports

import (
	"fmt"
	"strconv"
	"strings"
)

const maxPort = 65535

// HostPorts lists the host ports a sandbox port may publish on: the digits 1–9 prepended, each ≤ 65535.
func HostPorts(guest int) []int {
	if guest < 1 {
		return nil
	}
	shift := 1
	for range strconv.Itoa(guest) {
		shift *= 10
	}
	var out []int
	for prefix := 1; prefix <= 9; prefix++ {
		h := prefix*shift + guest
		if h > maxPort {
			break
		}
		out = append(out, h)
	}
	return out
}

// HostPortFor picks the first of HostPorts(guest) that free accepts.
func HostPortFor(guest int, free func(int) bool) (int, bool) {
	for _, h := range HostPorts(guest) {
		if free(h) {
			return h, true
		}
	}
	return 0, false
}

// HostPortWhy says why HostPortFor found no host port for guest.
func HostPortWhy(guest int) string {
	hosts := HostPorts(guest)
	if len(hosts) == 0 {
		return fmt.Sprintf("no host port 1%d…9%d fits under %d", guest, guest, maxPort)
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = ":" + strconv.Itoa(h)
	}
	return "host " + strings.Join(names, " ") + " are all in use"
}
