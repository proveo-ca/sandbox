// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/proveo-ca/proveo/internal/envflag"
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

// gfx1151 is Strix Halo, the one APU Ollama loads onto without OLLAMA_IGPU_ENABLE.
const gfx1151 = 110501

var (
	nvidiaFreeVRAM = func() (uint64, bool) { return readNvidiaMemory("free") }
	nvidiaQuery    = func(field string) (string, error) {
		out, err := exec.Command("nvidia-smi", "--query-gpu=memory."+field, "--format=csv,noheader,nounits").Output()
		return string(out), err
	}
	drmRoot          = "/sys/class/drm"
	kfdNodesRoot     = "/sys/class/kfd/kfd/topology/nodes"
	tegraReleasePath = "/etc/nv_tegra_release"
	memInfoPath      = "/proc/meminfo"
	igpuOptIn        = func() bool { return envflag.On(os.Getenv("OLLAMA_IGPU_ENABLE")) }
)

func hostAvailableMemory() (hostMemory, bool) { return memoryFor(runtime.GOOS, runtime.GOARCH) }

// HostAcceleratorCapacity reports installed discrete VRAM or Apple silicon unified memory.
func HostAcceleratorCapacity() uint64 { return capacityFor(runtime.GOOS, runtime.GOARCH) }

// capacityFor counts unified memory on darwin/arm64, discrete VRAM or an integrated GPU Ollama loads onto on linux,
// and nothing elsewhere.
func capacityFor(goos, goarch string) uint64 {
	switch {
	case goos == "darwin" && goarch == "arm64":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			bytes, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
			return bytes
		}
	case goos == "linux":
		if total, ok := readNvidiaMemory("total"); ok {
			return ((total + (1 << 29)) >> 30) << 30
		}
		if total, _, ok := amdVRAM(drmRoot); ok {
			return total
		}
		return integratedCapacity(goarch)
	}
	return 0
}

// integratedCapacity is what Ollama loads onto a linux integrated GPU: MemTotal for an NVIDIA GPU with no memory
// of its own or a Jetson, apuCapacity otherwise; 0 for none.
func integratedCapacity(goarch string) uint64 {
	total, ok := readMemTotal()
	if !ok {
		return 0
	}
	if nvidiaIntegrated() || goarch == "arm64" && fileExists(tegraReleasePath) {
		return total
	}
	return apuCapacity(drmRoot, total)
}

func nvidiaIntegrated() bool {
	out, err := nvidiaQuery("total")
	return err == nil && slices.Contains(strings.Fields(out), "[N/A]")
}

// apuCapacity is min(max(VRAM carve-out, GTT), memTotal) for an AMD APU Ollama loads onto, memTotal for an opted-in
// Intel GPU, 0 otherwise.
func apuCapacity(root string, memTotal uint64) uint64 {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !drmCard.MatchString(e.Name()) {
			continue
		}
		dev := filepath.Join(root, e.Name(), "device")
		vram, okV := readSysfsUint(filepath.Join(dev, "mem_info_vram_total"))
		gtt, okG := readSysfsUint(filepath.Join(dev, "mem_info_gtt_total"))
		switch {
		case okV && okG && amdAPU(dev, vram) && (igpuOptIn() || kfdTarget(dev) == gfx1151):
			return min(max(vram, gtt), memTotal)
		case igpuOptIn() && readSysfsString(filepath.Join(dev, "vendor")) == "0x8086":
			return memTotal
		}
	}
	return 0
}

// amdAPU reports an integrated amdgpu device: Ollama's classification, or gfx1151 at any carve-out.
func amdAPU(dev string, vramTotal uint64) bool {
	return amdIntegrated(dev, vramTotal) || kfdTarget(dev) == gfx1151
}

// kfdTarget is the gfx_target_version KFD reports for the card at dev; 0 when unknown.
func kfdTarget(dev string) uint64 {
	minor, ok := renderMinor(dev)
	if !ok {
		return 0
	}
	nodes, err := os.ReadDir(kfdNodesRoot)
	if err != nil {
		return 0
	}
	for _, n := range nodes {
		props := kfdProperties(filepath.Join(kfdNodesRoot, n.Name(), "properties"))
		if m, ok := props["drm_render_minor"]; ok && m == minor {
			return props["gfx_target_version"]
		}
	}
	return 0
}

func renderMinor(dev string) (uint64, bool) {
	entries, err := os.ReadDir(filepath.Join(dev, "drm"))
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if n, ok := strings.CutPrefix(e.Name(), "renderD"); ok {
			if minor, err := strconv.ParseUint(n, 10, 64); err == nil {
				return minor, true
			}
		}
	}
	return 0, false
}

func kfdProperties(path string) map[string]uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	props := map[string]uint64{}
	for line := range strings.Lines(string(b)) {
		if f := strings.Fields(line); len(f) == 2 {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				props[f[0]] = n
			}
		}
	}
	return props
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// memoryFor picks the pool Ollama loads into: unified memory on Apple silicon or a linux integrated GPU,
// a discrete GPU's VRAM elsewhere, RAM when no GPU answers.
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
		if _, free, ok := amdVRAM(drmRoot); ok {
			return hostMemory{Free: free, Pool: poolVRAM, RAM: ram}, true
		}
		if integratedCapacity(goarch) > 0 {
			return hostMemory{Free: ram, Pool: poolUnified}, ramOK
		}
		return hostMemory{Free: ram, Pool: poolRAM}, ramOK
	}
	return hostMemory{}, false
}

func readNvidiaMemory(field string) (uint64, bool) {
	out, err := nvidiaQuery(field)
	if err != nil {
		return 0, false
	}
	return parseNvidiaMemory(out)
}

// parseNvidiaMemory sums MiB across GPUs.
func parseNvidiaMemory(out string) (uint64, bool) {
	var mib uint64
	seen := false
	for f := range strings.FieldsSeq(out) {
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

// amdVRAM sums total and free VRAM over discrete amdgpu cards under root.
func amdVRAM(root string) (total, free uint64, found bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, false
	}
	for _, e := range entries {
		if !drmCard.MatchString(e.Name()) {
			continue
		}
		dev := filepath.Join(root, e.Name(), "device")
		capacity, okT := readSysfsUint(filepath.Join(dev, "mem_info_vram_total"))
		used, okU := readSysfsUint(filepath.Join(dev, "mem_info_vram_used"))
		if !okT || !okU || used > capacity || amdAPU(dev, capacity) {
			continue
		}
		total += capacity
		free += capacity - used
		found = true
	}
	return total, free, found
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

func readSysfsString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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

func readMemTotal() (uint64, bool) {
	b, err := os.ReadFile(memInfoPath)
	if err != nil {
		return 0, false
	}
	return parseMemInfoField(string(b), "MemTotal:")
}

func parseMemInfo(out string) (uint64, bool) { return parseMemInfoField(out, "MemAvailable:") }

func parseMemInfoField(out, key string) (uint64, bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == key {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}
