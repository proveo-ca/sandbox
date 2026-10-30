// SPEC: _spec/internal/sbx/host-inference.puml
package sbx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseNvidiaFreeSumsEveryGPU(t *testing.T) {
	got, ok := parseNvidiaFree("15360\n8192\n")
	if want := uint64(15360+8192) << 20; !ok || got != want {
		t.Errorf("parseNvidiaFree = %d, %v; want %d", got, ok, want)
	}
	for _, out := range []string{"", "[N/A]\n", "15360\n[N/A]\n"} {
		if _, ok := parseNvidiaFree(out); ok {
			t.Errorf("parseNvidiaFree(%q) must give no reading", out)
		}
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
	got, ok := amdFreeVRAM(root)
	if want := uint64(22 << 30); !ok || got != want {
		t.Errorf("amdFreeVRAM = %d, %v; want the discrete card's %d, not the APU's carve-out or a connector", got, ok, want)
	}

	apu := t.TempDir()
	writeCard(t, apu, "card0", map[string]uint64{
		"mem_info_vram_total": 512 << 20, "mem_info_vram_used": 0, "mem_info_gtt_total": 16 << 30})
	if _, ok := amdFreeVRAM(apu); ok {
		t.Error("an APU-only host has no discrete VRAM; it must fall back to RAM")
	}
}

func stubLinuxProbes(t *testing.T, nvidia uint64, nvidiaOK bool, drm, meminfo string) {
	t.Helper()
	origN, origD, origM := nvidiaFreeVRAM, drmRoot, memInfoPath
	t.Cleanup(func() { nvidiaFreeVRAM, drmRoot, memInfoPath = origN, origD, origM })
	nvidiaFreeVRAM = func() (uint64, bool) { return nvidia, nvidiaOK }
	drmRoot, memInfoPath = drm, meminfo
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
