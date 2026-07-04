package main

// Tests for the amd-smi runners and parsers. The testdata/amdsmi_*.json
// fixtures are real output captured from amd-smi 26.2.1 (ROCm 7.2) on a
// 4x AMD Radeon AI PRO R9700 machine running a live vllm workload.

import (
	"math"
	"os"
	"os/exec"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ── Value helpers ────────────────────────────────────────────────────

func TestAmdSmiFloatShapes(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		val  float64
		unit string
		ok   bool
	}{
		{"plain number", 42.5, 42.5, "", true},
		{"numeric string", "300", 300, "", true},
		{"string with unit", "32 GT/s", 32, "GT/s", true},
		{"nested object", map[string]interface{}{"value": 32624.0, "unit": "MB"}, 32624, "MB", true},
		{"nested N/A value", map[string]interface{}{"value": "N/A", "unit": "GB/s"}, 0, "", false},
		{"nested N/A unit", map[string]interface{}{"value": 7.0, "unit": "N/A"}, 7, "", true},
		{"N/A string", "N/A", 0, "", false},
		{"empty string", "", 0, "", false},
		{"nil", nil, 0, "", false},
		{"missing value key", map[string]interface{}{"unit": "MB"}, 0, "", false},
	}
	for _, c := range cases {
		val, unit, ok := amdSmiFloat(c.in)
		if val != c.val || unit != c.unit || ok != c.ok {
			t.Errorf("%s: amdSmiFloat = (%v, %q, %v), want (%v, %q, %v)",
				c.name, val, unit, ok, c.val, c.unit, c.ok)
		}
	}
}

func TestAmdSmiBytesUnits(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int64
		ok   bool
	}{
		{"plain bytes", 28899092000.0, 28899092000, true},
		{"nested B", map[string]interface{}{"value": 76000.0, "unit": "B"}, 76000, true},
		{"nested MB", map[string]interface{}{"value": 112.0, "unit": "MB"}, 112 << 20, true},
		{"nested KiB", map[string]interface{}{"value": 8268.0, "unit": "KiB"}, 8268 << 10, true},
		{"string GiB", "2 GiB", 2 << 30, true},
		{"unknown unit", "5 potatoes", 0, false},
		{"N/A", "N/A", 0, false},
	}
	for _, c := range cases {
		got, ok := amdSmiBytes(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: amdSmiBytes = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAmdSmiEntriesShapes(t *testing.T) {
	if got := parseAmdSmiEntries([]byte(`[{"gpu": 0}, {"gpu": 1}]`)); len(got) != 2 {
		t.Errorf("top-level array: got %d entries, want 2", len(got))
	}
	if got := parseAmdSmiEntries([]byte(`{"gpu_data": [{"gpu": 0}]}`)); len(got) != 1 {
		t.Errorf("gpu_data wrapper: got %d entries, want 1", len(got))
	}
	if got := parseAmdSmiEntries(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	if got := parseAmdSmiEntries([]byte("not json")); got != nil {
		t.Errorf("garbage input: got %v, want nil", got)
	}
	if got := parseAmdSmiEntries([]byte(`{"other": 1}`)); len(got) != 0 {
		t.Errorf("object without gpu_data: got %d entries, want 0", len(got))
	}
}

// ── Discovery / identity ─────────────────────────────────────────────

func TestParseAmdSmiStaticFixture(t *testing.T) {
	gpus := parseAmdSmiStatic(readFixture(t, "amdsmi_static.json"))
	if len(gpus) != 4 {
		t.Fatalf("got %d GPUs, want 4", len(gpus))
	}
	wantBDF := []string{"0000:03:00.0", "0000:23:00.0", "0000:83:00.0", "0000:c3:00.0"}
	for i, g := range gpus {
		if g.CardID != i {
			t.Errorf("GPU %d: CardID = %d", i, g.CardID)
		}
		if g.Backend != "rocm" {
			t.Errorf("GPU %d: Backend = %q, want rocm", i, g.Backend)
		}
		if g.Name != "AMD Radeon AI PRO R9700" {
			t.Errorf("GPU %d: Name = %q", i, g.Name)
		}
		if g.Vendor != "Advanced Micro Devices Inc. [AMD/ATI]" {
			t.Errorf("GPU %d: Vendor = %q", i, g.Vendor)
		}
		if g.GfxVersion != "gfx1201" {
			t.Errorf("GPU %d: GfxVersion = %q, want gfx1201", i, g.GfxVersion)
		}
		if normalizePCI(g.PcieBus) != wantBDF[i] {
			t.Errorf("GPU %d: PcieBus = %q, want %s", i, g.PcieBus, wantBDF[i])
		}
		if g.SKU != "APM107573" {
			t.Errorf("GPU %d: SKU = %q, want APM107573", i, g.SKU)
		}
		// Identity-only: dynamic metrics must keep their sentinels.
		if !math.IsNaN(g.GpuUse) || !math.IsNaN(g.PowerAvg) {
			t.Errorf("GPU %d: dynamic metrics not at sentinels (use=%v pwr=%v)", i, g.GpuUse, g.PowerAvg)
		}
	}
}

func TestParseAmdSmiStaticEmpty(t *testing.T) {
	if got := parseAmdSmiStatic(nil); len(got) != 0 {
		t.Errorf("got %d GPUs from empty payload, want 0", len(got))
	}
}

// ── Static info ──────────────────────────────────────────────────────

func TestApplyAmdSmiStaticFixture(t *testing.T) {
	raw := readFixture(t, "amdsmi_static.json")
	gpus := parseAmdSmiStatic(raw)
	// A non-rocm GPU sharing CardID 0 must not receive rocm static fields.
	gpus = append(gpus, GpuData{CardID: 0, Backend: "sysfs"})

	applyAmdSmiStatic(raw, gpus)

	if gpus[0].Vbios != "113-APM107573-100" {
		t.Errorf("Vbios = %q, want 113-APM107573-100", gpus[0].Vbios)
	}
	if gpus[0].MemVendor != "samsung" {
		t.Errorf("MemVendor = %q, want samsung", gpus[0].MemVendor)
	}
	if gpus[0].UniqueID != "0x64ac21a676f77a5b" {
		t.Errorf("UniqueID = %q, want 0x64ac21a676f77a5b", gpus[0].UniqueID)
	}
	for i := 0; i < 4; i++ {
		if gpus[i].DriverVersion != "6.16.6" {
			t.Errorf("GPU %d: DriverVersion = %q, want 6.16.6", i, gpus[i].DriverVersion)
		}
	}
	if gpus[1].Vbios != "113-APM107573-101" {
		t.Errorf("GPU 1 Vbios = %q, want 113-APM107573-101", gpus[1].Vbios)
	}
	sysfsGpu := gpus[4]
	if sysfsGpu.Vbios != "" || sysfsGpu.DriverVersion != "" || sysfsGpu.UniqueID != "" {
		t.Errorf("sysfs GPU received rocm static fields: %+v", sysfsGpu)
	}
}

func TestApplyAmdSmiEccFixture(t *testing.T) {
	gpus := parseAmdSmiStatic(readFixture(t, "amdsmi_static.json"))
	// Preset a nonzero count to verify the fixture's real zeros overwrite it.
	gpus[2].RasCorrectable = 99
	applyAmdSmiEcc(readFixture(t, "amdsmi_metric_ecc.json"), gpus)
	for i, g := range gpus {
		if g.RasCorrectable != 0 || g.RasUncorrectable != 0 {
			t.Errorf("GPU %d: RAS counts = %d/%d, want 0/0", i, g.RasCorrectable, g.RasUncorrectable)
		}
	}
}

func TestApplyAmdSmiEccNonzeroAndNA(t *testing.T) {
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 1, Backend: "rocm", RasCorrectable: 7},
	}
	payload := `{"gpu_data": [
		{"gpu": 0, "ecc": {"total_correctable_count": 3, "total_uncorrectable_count": 1}},
		{"gpu": 1, "ecc": {"total_correctable_count": "N/A", "total_uncorrectable_count": "N/A"}}
	]}`
	applyAmdSmiEcc([]byte(payload), gpus)
	if gpus[0].RasCorrectable != 3 || gpus[0].RasUncorrectable != 1 {
		t.Errorf("GPU 0 RAS = %d/%d, want 3/1", gpus[0].RasCorrectable, gpus[0].RasUncorrectable)
	}
	// N/A counts must leave existing values untouched.
	if gpus[1].RasCorrectable != 7 {
		t.Errorf("GPU 1 RasCorrectable = %d, want 7 (unchanged)", gpus[1].RasCorrectable)
	}
}

// ── Process listing ──────────────────────────────────────────────────

func TestParseAmdSmiProcessesFixture(t *testing.T) {
	procs := parseAmdSmiProcesses(readFixture(t, "amdsmi_process.json"))
	// The capture has 6 distinct PIDs, each listed under all 4 GPUs.
	if len(procs) != 6 {
		t.Fatalf("got %d processes, want 6", len(procs))
	}
	// Sorted by VRAM descending: the biggest vllm worker first.
	if procs[0].PID != 1883505 {
		t.Errorf("procs[0].PID = %d, want 1883505", procs[0].PID)
	}
	// Process-wide totals must be deduplicated, not summed across the 4
	// per-GPU listings (which would report ~4x the real usage).
	if procs[0].VramUsed != 29429556000 {
		t.Errorf("procs[0].VramUsed = %d, want 29429556000", procs[0].VramUsed)
	}
	if procs[0].Name != "python3.12" {
		t.Errorf("procs[0].Name = %q, want python3.12 (basename)", procs[0].Name)
	}
	wantGpus := []int{0, 1, 2, 3}
	if len(procs[0].GpuIDs) != len(wantGpus) {
		t.Fatalf("procs[0].GpuIDs = %v, want %v", procs[0].GpuIDs, wantGpus)
	}
	for i, id := range wantGpus {
		if procs[0].GpuIDs[i] != id {
			t.Errorf("procs[0].GpuIDs = %v, want %v", procs[0].GpuIDs, wantGpus)
			break
		}
	}
	for i := 1; i < len(procs); i++ {
		if procs[i].VramUsed > procs[i-1].VramUsed {
			t.Errorf("processes not sorted by VRAM desc at %d", i)
		}
	}
}

func TestParseAmdSmiProcessesNoneRunning(t *testing.T) {
	payload := `[
		{"gpu": 0, "process_list": [{"process_info": "No running processes detected"}]},
		{"gpu": 1, "process_list": [{"process_info": "No running processes detected"}]}
	]`
	if procs := parseAmdSmiProcesses([]byte(payload)); len(procs) != 0 {
		t.Errorf("got %d processes, want 0", len(procs))
	}
}

func TestParseAmdSmiProcessesDefensiveShapes(t *testing.T) {
	// Plain-number memory, missing memory_usage, and string pid with units
	// stripped must all parse without panicking.
	payload := `[{"gpu": 2, "process_list": [
		{"process_info": {"name": "plain", "pid": 10, "memory_usage": {"vram_mem": 1048576}}},
		{"process_info": {"name": "nomem", "pid": 11}},
		{"process_info": {"name": "strpid", "pid": "12", "memory_usage": {"vram_mem": {"value": 2, "unit": "MB"}}}}
	]}]`
	procs := parseAmdSmiProcesses([]byte(payload))
	if len(procs) != 3 {
		t.Fatalf("got %d processes, want 3", len(procs))
	}
	byPID := make(map[int]ProcessData)
	for _, p := range procs {
		byPID[p.PID] = p
	}
	if byPID[10].VramUsed != 1048576 {
		t.Errorf("PID 10 VramUsed = %d, want 1048576", byPID[10].VramUsed)
	}
	if byPID[11].VramUsed != 0 {
		t.Errorf("PID 11 VramUsed = %d, want 0", byPID[11].VramUsed)
	}
	if byPID[12].VramUsed != 2<<20 {
		t.Errorf("PID 12 VramUsed = %d, want %d", byPID[12].VramUsed, 2<<20)
	}
	if got := byPID[10].GpuIDs; len(got) != 1 || got[0] != 2 {
		t.Errorf("PID 10 GpuIDs = %v, want [2]", got)
	}
}

// ── Tool selection and fallback wiring ───────────────────────────────

func TestSelectAmdToolPrefersRocmSMI(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"rocm-smi", "amd-smi"} {
		if err := os.WriteFile(binDir+"/"+name, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	tool := selectAmdTool()
	if tool == nil || tool.name != "rocm-smi" {
		t.Fatalf("tool = %+v, want rocm-smi", tool)
	}
}

func TestSelectAmdToolFallsBackToAmdSMI(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(binDir+"/amd-smi", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	tool := selectAmdTool()
	if tool == nil || tool.name != "amd-smi" {
		t.Fatalf("tool = %+v, want amd-smi", tool)
	}
}

func TestSelectAmdToolNeitherInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if tool := selectAmdTool(); tool != nil {
		t.Fatalf("tool = %+v, want nil", tool)
	}
}

// TestRocmBackendViaFakeAmdSmi exercises the rocm backend end-to-end with a
// fake amd-smi on PATH and no rocm-smi: discovery finds the GPU, the sysfs
// mapping fails (bogus PCI bus), and CollectData degrades to the identity +
// process fallback.
func TestRocmBackendViaFakeAmdSmi(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
static) echo '{"gpu_data": [{"gpu": 0, "asic": {"market_name": "Fake amd-smi GPU"}, "bus": {"bdf": "0000:ff:1f.7"}}]}' ;;
process) echo '[{"gpu": 0, "process_list": [{"process_info": {"name": "/usr/bin/fake", "pid": 42, "memory_usage": {"vram_mem": {"value": 1000, "unit": "B"}}}}]}]' ;;
esac
`
	if err := os.WriteFile(binDir+"/amd-smi", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	tool := selectAmdTool()
	if tool == nil || tool.name != "amd-smi" {
		t.Fatalf("tool = %+v, want amd-smi", tool)
	}
	b := newRocmBackend(*tool)
	if len(b.cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(b.cards))
	}
	if b.sysfsMode {
		t.Fatal("sysfsMode = true, want false (bogus PCI bus cannot map)")
	}
	gpus, procs := b.CollectData()
	if len(gpus) != 1 || gpus[0].Name != "Fake amd-smi GPU" {
		t.Fatalf("gpus = %+v, want one Fake amd-smi GPU", gpus)
	}
	if len(procs) != 1 || procs[0].PID != 42 || procs[0].Name != "fake" || procs[0].VramUsed != 1000 {
		t.Fatalf("procs = %+v, want PID 42 'fake' 1000 B", procs)
	}
}

// ── Live hardware smoke test (skipped on GPU-less CI) ────────────────

// TestLiveAmdSmi validates the amd-smi parsers against the real binary and,
// when rocm-smi is also installed, cross-checks GPU identity (PCI bus set)
// between the two tools.
func TestLiveAmdSmi(t *testing.T) {
	if _, err := exec.LookPath(amdSMI); err != nil {
		t.Skip("amd-smi not installed")
	}
	gpus := amdSmiDiscover()
	if len(gpus) == 0 {
		t.Skip("amd-smi reported no GPUs")
	}
	amdBDF := make(map[string]bool)
	for _, g := range gpus {
		if g.Name == "" {
			t.Errorf("GPU %d has no name", g.CardID)
		}
		bdf := normalizePCI(g.PcieBus)
		if bdf == "" {
			t.Errorf("GPU %d has no valid PCI bus (%q)", g.CardID, g.PcieBus)
		}
		amdBDF[bdf] = true
	}

	amdSmiStaticInfo(gpus)
	for _, g := range gpus {
		if g.DriverVersion == "" {
			t.Errorf("GPU %d has no driver version after static info", g.CardID)
		}
		t.Logf("amd-smi GPU %d %s pci=%s gfx=%s sku=%s vbios=%s memvendor=%s uid=%s drv=%s ras=%d/%d",
			g.CardID, g.Name, g.PcieBus, g.GfxVersion, g.SKU, g.Vbios,
			g.MemVendor, g.UniqueID, g.DriverVersion, g.RasCorrectable, g.RasUncorrectable)
	}

	procs := collectAmdSmiProcesses()
	for _, p := range procs {
		if p.PID <= 0 {
			t.Errorf("process with invalid PID: %+v", p)
		}
		t.Logf("amd-smi proc pid=%d name=%s gpus=%v vram=%d", p.PID, p.Name, p.GpuIDs, p.VramUsed)
	}

	// Cross-check against rocm-smi identity when available.
	if _, err := exec.LookPath(rocmSMI); err != nil {
		t.Log("rocm-smi not installed; skipping cross-check")
		return
	}
	rocmGpus := newRocmSMITool().discover()
	if len(rocmGpus) == 0 {
		t.Log("rocm-smi reported no GPUs; skipping cross-check")
		return
	}
	if len(rocmGpus) != len(gpus) {
		t.Errorf("GPU count mismatch: amd-smi %d vs rocm-smi %d", len(gpus), len(rocmGpus))
	}
	for _, g := range rocmGpus {
		bdf := normalizePCI(g.PcieBus)
		if !amdBDF[bdf] {
			t.Errorf("rocm-smi GPU %d bus %s not found by amd-smi (amd-smi buses: %v)", g.CardID, bdf, amdBDF)
		}
	}
}
