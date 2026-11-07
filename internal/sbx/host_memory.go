// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// hostMemory is the pool Ollama loads a model into and how much of it is free;
// RAM is the free system memory a VRAM overflow lands in.
type hostMemory struct {
	Free uint64
	Pool string
	RAM  uint64
}

const (
	poolUnified = "unified memory"
	poolVRAM    = "VRAM"
	poolRAM     = "RAM"
)

var (
	nvidiaFreeVRAM = readNvidiaFreeVRAM
	drmRoot        = "/sys/class/drm"
	memInfoPath    = "/proc/meminfo"
)

func hostAvailableMemory() (hostMemory, bool) { return memoryFor(runtime.GOOS, runtime.GOARCH) }

// memoryFor picks the pool Ollama loads into: unified memory on Apple silicon,
// a discrete GPU's VRAM elsewhere, RAM when no discrete GPU answers.
func memoryFor(goos, goarch string) (hostMemory, bool) {
	switch goos {
	case "darwin":
		out, err := exec.Command("vm_stat").Output()
		if err != nil {
			return hostMemory{}, false
		}
		free, ok := parseVMStat(string(out))
		pool := poolRAM
		if goarch == "arm64" {
			pool = poolUnified
		}
		return hostMemory{Free: free, Pool: pool}, ok
	case "linux":
		ram, ramOK := readMemAvailable()
		if free, ok := nvidiaFreeVRAM(); ok {
			return hostMemory{Free: free, Pool: poolVRAM, RAM: ram}, true
		}
		if free, ok := amdFreeVRAM(drmRoot); ok {
			return hostMemory{Free: free, Pool: poolVRAM, RAM: ram}, true
		}
		return hostMemory{Free: ram, Pool: poolRAM}, ramOK
	}
	return hostMemory{}, false
}

func readNvidiaFreeVRAM() (uint64, bool) {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.free", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, false
	}
	return parseNvidiaFree(string(out))
}

// parseNvidiaFree sums memory.free (MiB, one line per GPU); any non-numeric reading gives none.
func parseNvidiaFree(out string) (uint64, bool) {
	var mib uint64
	seen := false
	for _, f := range strings.Fields(out) {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, false
		}
		mib += n
		seen = true
	}
	return mib << 20, seen
}

var drmCard = regexp.MustCompile(`^card\d+$`)

// amdFreeVRAM sums free VRAM over discrete amdgpu cards under root.
func amdFreeVRAM(root string) (uint64, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	var free uint64
	seen := false
	for _, e := range entries {
		if !drmCard.MatchString(e.Name()) {
			continue
		}
		dev := filepath.Join(root, e.Name(), "device")
		total, okT := readSysfsUint(filepath.Join(dev, "mem_info_vram_total"))
		used, okU := readSysfsUint(filepath.Join(dev, "mem_info_vram_used"))
		if !okT || !okU || used > total || amdIntegrated(dev, total) {
			continue
		}
		free += total - used
		seen = true
	}
	return free, seen
}

// amdIntegrated classifies an amdgpu device as Ollama's discover/amd.go does.
func amdIntegrated(dev string, vramTotal uint64) bool {
	for _, marker := range []string{"mem_info_vram_vendor", "board_info"} {
		if _, err := os.Stat(filepath.Join(dev, marker)); err == nil {
			return false
		}
	}
	gtt, ok := readSysfsUint(filepath.Join(dev, "mem_info_gtt_total"))
	return ok && vramTotal > 0 && vramTotal <= 4<<30 && gtt >= 8<<30 && gtt >= 4*vramTotal
}

func readSysfsUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

var (
	vmPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	vmLine     = regexp.MustCompile(`^(Pages [a-z]+|"[^"]+"|[A-Za-z ]+):\s+(\d+)\.`)
)

// parseVMStat sums the pages macOS can hand out without swapping: free, inactive, speculative, purgeable.
func parseVMStat(out string) (uint64, bool) {
	pm := vmPageSize.FindStringSubmatch(out)
	if pm == nil {
		return 0, false
	}
	page, _ := strconv.ParseUint(pm[1], 10, 64)
	var pages uint64
	seen := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := vmLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if f == nil {
			continue
		}
		switch f[1] {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			n, _ := strconv.ParseUint(f[2], 10, 64)
			pages += n
			seen = true
		}
	}
	return pages * page, seen
}

func readMemAvailable() (uint64, bool) {
	b, err := os.ReadFile(memInfoPath)
	if err != nil {
		return 0, false
	}
	return parseMemInfo(string(b))
}

func parseMemInfo(out string) (uint64, bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}
