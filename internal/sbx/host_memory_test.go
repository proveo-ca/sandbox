// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParseNvidiaFreeSumsEveryGPU(t *testing.T) {
	got, ok := parseNvidiaMemory("15360\n8192\n")
	if want := uint64(15360+8192) << 20; !ok || got != want {
		t.Errorf("parseNvidiaMemory = %d, %v; want %d", got, ok, want)
	}
	for _, out := range []string{"", "[N/A]\n", "15360\n[N/A]\n"} {
		if _, ok := parseNvidiaMemory(out); ok {
			t.Errorf("parseNvidiaMemory(%q) must give no reading", out)
		}
	}
}

func TestNvidiaCapacityUsesNominalTotalRatherThanFreeMemory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux NVIDIA probe")
	}
	bin := t.TempDir()
	probe := `#!/bin/sh
case "$1" in
  --query-gpu=memory.total) printf '%s\n' 97887 ;;
  --query-gpu=memory.free) printf '%s\n' 12000 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "nvidia-smi"), []byte(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := HostAcceleratorCapacity(); got != 96<<30 {
		t.Errorf("capacity = %d bytes, want 96 GiB", got)
	}
}

func writeCard(t *testing.T, root, card string, files map[string]uint64, markers ...string) {
	t.Helper()
	dev := filepath.Join(root, card, "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, v := range files {
		if err := os.WriteFile(filepath.Join(dev, name), []byte(strconv.FormatUint(v, 10)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range markers {
		if err := os.WriteFile(filepath.Join(dev, m), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAMDFreeVRAMCountsDiscreteCardsOnly(t *testing.T) {
	root := t.TempDir()
	writeCard(t, root, "card0", map[string]uint64{"mem_info_vram_total": 24 << 30, "mem_info_vram_used": 2 << 30}, "board_info")
	writeCard(t, root, "card1", map[string]uint64{
		"mem_info_vram_total": 512 << 20, "mem_info_vram_used": 100 << 20, "mem_info_gtt_total": 16 << 30})
	writeCard(t, root, "card0-DP-1", map[string]uint64{"mem_info_vram_total": 99 << 30, "mem_info_vram_used": 0})
	total, free, ok := amdVRAM(root)
	if !ok || total != 24<<30 || free != 22<<30 {
		t.Errorf("amdVRAM = %d total, %d free, %v; want 24 GiB total and 22 GiB free", total, free, ok)
	}

	apu := t.TempDir()
	writeCard(t, apu, "card0", map[string]uint64{
		"mem_info_vram_total": 512 << 20, "mem_info_vram_used": 0, "mem_info_gtt_total": 16 << 30})
	if _, _, ok := amdVRAM(apu); ok {
		t.Error("an APU-only host has no discrete VRAM; it must fall back to RAM")
	}
}

func stubLinuxProbes(t *testing.T, nvidia uint64, nvidiaOK bool, drm, meminfo string) {
	t.Helper()
	origN, origD, origM := nvidiaFreeVRAM, drmRoot, memInfoPath
	t.Cleanup(func() { nvidiaFreeVRAM, drmRoot, memInfoPath = origN, origD, origM })
	nvidiaFreeVRAM = func() (uint64, bool) { return nvidia, nvidiaOK }
	drmRoot, memInfoPath = drm, meminfo
	stubIntegratedProbes(t, "", errors.New("no nvidia-smi"), t.TempDir(), false)
}

// stubIntegratedProbes pins nvidia-smi's total, the KFD topology, the Jetson marker and OLLAMA_IGPU_ENABLE.
func stubIntegratedProbes(t *testing.T, nvidiaTotal string, nvidiaErr error, kfd string, optIn bool) {
	t.Helper()
	origQ, origK, origT, origO := nvidiaQuery, kfdNodesRoot, tegraReleasePath, igpuOptIn
	t.Cleanup(func() { nvidiaQuery, kfdNodesRoot, tegraReleasePath, igpuOptIn = origQ, origK, origT, origO })
	nvidiaQuery = func(string) (string, error) { return nvidiaTotal, nvidiaErr }
	kfdNodesRoot, tegraReleasePath = kfd, filepath.Join(t.TempDir(), "nv_tegra_release")
	igpuOptIn = func() bool { return optIn }
}

func writeMemInfo(t *testing.T, totalGiB, availGiB uint64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	body := fmt.Sprintf("MemTotal:       %d kB\nMemAvailable:   %d kB\n", totalGiB<<20, availGiB<<20)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeAPU writes an amdgpu card with render node renderD128 and a KFD node naming its gfx target.
func writeAPU(t *testing.T, vram, gtt, target uint64) (drm, kfd string) {
	t.Helper()
	drm, kfd = t.TempDir(), t.TempDir()
	writeCard(t, drm, "card0", map[string]uint64{"mem_info_vram_total": vram, "mem_info_vram_used": 0, "mem_info_gtt_total": gtt})
	if err := os.MkdirAll(filepath.Join(drm, "card0", "device", "drm", "renderD128"), 0o755); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(kfd, "1")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	props := fmt.Sprintf("cpu_cores_count 0\nsimd_count 80\ngfx_target_version %d\ndrm_render_minor 128\n", target)
	if err := os.WriteFile(filepath.Join(node, "properties"), []byte(props), 0o644); err != nil {
		t.Fatal(err)
	}
	return drm, kfd
}

func TestAnNVIDIAGPUWithoutItsOwnMemoryCountsMemTotal(t *testing.T) {
	stubLinuxProbes(t, 0, false, t.TempDir(), writeMemInfo(t, 119, 100))
	stubIntegratedProbes(t, "[N/A]\n", nil, t.TempDir(), false)
	if got := capacityFor("linux", "arm64"); got != 119<<30 {
		t.Errorf("GB10 capacity = %s, want MemTotal 119.0 GiB", GiB(got))
	}
	mem, ok := memoryFor("linux", "arm64")
	if !ok || mem.Pool != poolUnified || mem.Free != 100<<30 {
		t.Errorf("GB10 pool = %+v (%v), want unified memory with MemAvailable free", mem, ok)
	}
}

func TestAJetsonCountsMemTotal(t *testing.T) {
	stubLinuxProbes(t, 0, false, t.TempDir(), writeMemInfo(t, 61, 50))
	if err := os.WriteFile(tegraReleasePath, []byte("# R36 (release), REVISION: 4.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := capacityFor("linux", "arm64"); got != 61<<30 {
		t.Errorf("Jetson capacity = %s, want MemTotal 61.0 GiB", GiB(got))
	}
	if got := capacityFor("linux", "amd64"); got != 0 {
		t.Errorf("an amd64 host with a stray Tegra marker = %s, want 0", GiB(got))
	}
}

func TestStrixHaloCountsWhatOllamaLoadsOntoIt(t *testing.T) {
	for _, c := range []struct {
		name              string
		vram, gtt, memGiB uint64
		want              uint64
	}{
		{"small carve-out, large GTT", 512 << 20, 110 << 30, 124, 110 << 30},
		{"96 GiB carve-out leaves the OS 31 GiB", 96 << 30, 15 << 30, 31, 31 << 30},
	} {
		drm, kfd := writeAPU(t, c.vram, c.gtt, gfx1151)
		stubLinuxProbes(t, 0, false, drm, writeMemInfo(t, c.memGiB, c.memGiB-4))
		stubIntegratedProbes(t, "", errors.New("no nvidia-smi"), kfd, false)
		if _, _, ok := amdVRAM(drm); ok {
			t.Errorf("%s: a gfx1151 APU must not count as discrete VRAM", c.name)
		}
		if got := capacityFor("linux", "amd64"); got != c.want {
			t.Errorf("%s: capacity = %s, want %s", c.name, GiB(got), GiB(c.want))
		}
		if mem, _ := memoryFor("linux", "amd64"); mem.Pool != poolUnified {
			t.Errorf("%s: pool = %q, want unified memory", c.name, mem.Pool)
		}
	}
}

func TestOtherIntegratedGPUsCountOnlyWhenOptedIn(t *testing.T) {
	const gfx1150 = 110500
	drm, kfd := writeAPU(t, 512<<20, 32<<30, gfx1150)
	intel := t.TempDir()
	if err := os.MkdirAll(filepath.Join(intel, "card0", "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(intel, "card0", "device", "vendor"), []byte("0x8086\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, drm string
		optIn     bool
		want      uint64
	}{
		{"Strix Point", drm, false, 0},
		{"Strix Point opted in", drm, true, 32 << 30},
		{"Intel iGPU", intel, false, 0},
		{"Intel iGPU opted in", intel, true, 64 << 30},
	} {
		stubLinuxProbes(t, 0, false, c.drm, writeMemInfo(t, 64, 60))
		stubIntegratedProbes(t, "", errors.New("no nvidia-smi"), kfd, c.optIn)
		if got := capacityFor("linux", "amd64"); got != c.want {
			t.Errorf("%s: capacity = %s, want %s", c.name, GiB(got), GiB(c.want))
		}
	}
}

func TestLinuxMemoryPrefersDiscreteVRAMOverRAM(t *testing.T) {
	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemAvailable:   15309824 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	amd := t.TempDir()
	writeCard(t, amd, "card0", map[string]uint64{"mem_info_vram_total": 16 << 30, "mem_info_vram_used": 0}, "board_info")
	for _, c := range []struct {
		name     string
		nvidia   uint64
		nvidiaOK bool
		drm      string
		free     uint64
		pool     string
	}{
		{"nvidia", 24 << 30, true, amd, 24 << 30, poolVRAM},
		{"amd", 0, false, amd, 16 << 30, poolVRAM},
		{"no discrete gpu", 0, false, t.TempDir(), 15309824 * 1024, poolRAM},
	} {
		stubLinuxProbes(t, c.nvidia, c.nvidiaOK, c.drm, meminfo)
		got, ok := memoryFor("linux", "amd64")
		if !ok || got.Free != c.free || got.Pool != c.pool {
			t.Errorf("%s: memoryFor = %+v, %v; want %d of %s", c.name, got, ok, c.free, c.pool)
		}
		if c.pool == poolVRAM && got.RAM != 15309824*1024 {
			t.Errorf("%s: a VRAM reading must carry the free RAM its overflow lands in, got %d", c.name, got.RAM)
		}
	}
}

func TestAVRAMOverflowStatesTheGPUShareAndWhereTheRestRuns(t *testing.T) {
	const weights = 17741872154 // qwen3.8:latest
	rtx3070 := NoFitError{Model: "qwen3.8:latest", Pool: poolVRAM, Weights: weights,
		Need: weights + weights/8 + 1<<30, Free: 7740 << 20}
	for _, c := range []struct {
		name  string
		ram   uint64
		swaps bool
	}{
		{"20 GiB RAM free", 20 << 30, false},
		{"8 GiB RAM free", 8 << 30, true},
		{"RAM unread", 0, false},
	} {
		e := rtx3070
		e.RAM = c.ram
		got := e.Consequence()
		for _, want := range []string{"~38% on the GPU", "~12.0 GiB on the CPU"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: consequence lacks %q: %q", c.name, want, got)
			}
		}
		if strings.Contains(got, "swap") != c.swaps {
			t.Errorf("%s: swap named = %v, want %v: %q", c.name, !c.swaps, c.swaps, got)
		}
	}
}

func TestGPUShare(t *testing.T) {
	for _, c := range []struct {
		free, need uint64
		want       int
	}{{0, 10, 0}, {5, 10, 50}, {10, 10, 100}, {20, 10, 100}, {1, 0, 100}} {
		if got := gpuShare(c.free, c.need); got != c.want {
			t.Errorf("gpuShare(%d, %d) = %d, want %d", c.free, c.need, got, c.want)
		}
	}
}

func TestNoFitErrorNamesWhatLoadingAnywayCosts(t *testing.T) {
	vram := (&NoFitError{Model: "m", Pool: poolVRAM}).Error()
	if !strings.Contains(vram, "CPU") || strings.Contains(vram, "swap") {
		t.Errorf("a VRAM overflow offloads to the CPU, it does not swap: %q", vram)
	}
	for _, pool := range []string{poolRAM, poolUnified} {
		if msg := (&NoFitError{Model: "m", Pool: pool}).Error(); !strings.Contains(msg, "swap") {
			t.Errorf("a %s overflow swaps: %q", pool, msg)
		}
	}
}

func TestOnlyAppleSiliconCountsUnifiedMemoryAsAcceleratorCapacity(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "amd64"}, {"windows", "amd64"}, {"windows", "arm64"}, {"freebsd", "amd64"}} {
		if got := capacityFor(p[0], p[1]); got != 0 {
			t.Errorf("%s/%s capacity = %d, want 0: no unified memory or discrete VRAM proveo can read", p[0], p[1], got)
		}
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		if got := capacityFor("darwin", "arm64"); got == 0 {
			t.Error("Apple silicon must report its unified memory")
		}
	}
}
