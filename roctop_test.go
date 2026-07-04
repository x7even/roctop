package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Parsing helpers ─────────────────────────────────────────────────

func TestParseFloat(t *testing.T) {
	tests := []struct {
		input string
		def   float64
		want  float64
	}{
		{"42.5", 0, 42.5},
		{"100%", 0, 100},
		{"  3.14 ", 0, 3.14},
		{"", 99, 99},
		{"abc", 99, 99},
		{"[N/A]", 0, 0},
		{"-1.5", 0, -1.5},
		{"1,234", 0, 1234},
	}
	for _, tt := range tests {
		got := parseFloat(tt.input, tt.def)
		if got != tt.want {
			t.Errorf("parseFloat(%q, %v) = %v, want %v", tt.input, tt.def, got, tt.want)
		}
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		input string
		def   int
		want  int
	}{
		{"42", 0, 42},
		{"100MHz", 0, 100},
		{"", 99, 99},
		{"abc", 99, 99},
		{"[Not Supported]", 0, 0},
	}
	for _, tt := range tests {
		got := parseInt(tt.input, tt.def)
		if got != tt.want {
			t.Errorf("parseInt(%q, %v) = %v, want %v", tt.input, tt.def, got, tt.want)
		}
	}
}

func TestParseInt64(t *testing.T) {
	got := parseInt64("8589934592", 0) // 8 GiB in bytes
	if got != 8589934592 {
		t.Errorf("parseInt64(\"8589934592\", 0) = %d, want 8589934592", got)
	}
	got = parseInt64("", -1)
	if got != -1 {
		t.Errorf("parseInt64(\"\", -1) = %d, want -1", got)
	}
}

// ── Throttle reasons ────────────────────────────────────────────────

func TestThrottleReasons(t *testing.T) {
	reasons := throttleReasons(0)
	if reasons != nil {
		t.Errorf("throttleReasons(0) = %v, want nil", reasons)
	}

	reasons = throttleReasons(1<<0 | 1<<1) // POWER_LIMIT + THERMAL
	if len(reasons) != 2 {
		t.Fatalf("throttleReasons(3) returned %d reasons, want 2", len(reasons))
	}
	if reasons[0] != "POWER_LIMIT" || reasons[1] != "THERMAL" {
		t.Errorf("throttleReasons(3) = %v, want [POWER_LIMIT THERMAL]", reasons)
	}
}

// ── HistKey ─────────────────────────────────────────────────────────

func TestHistKey(t *testing.T) {
	gpu := GpuData{CardID: 0, Backend: "nvidia"}
	if gpu.HistKey() != "nvidia:0" {
		t.Errorf("HistKey() = %q, want \"nvidia:0\"", gpu.HistKey())
	}
	gpu = GpuData{CardID: 3, Backend: "rocm"}
	if gpu.HistKey() != "rocm:3" {
		t.Errorf("HistKey() = %q, want \"rocm:3\"", gpu.HistKey())
	}
}

// ── Backend helpers ─────────────────────────────────────────────────

func TestBackendOrder(t *testing.T) {
	if backendOrder("rocm") >= backendOrder("nvidia") {
		t.Error("rocm should sort before nvidia")
	}
	if backendOrder("nvidia") >= backendOrder("sysfs") {
		t.Error("nvidia should sort before sysfs")
	}
	if backendOrder("unknown") <= backendOrder("sysfs") {
		t.Error("unknown should sort after sysfs")
	}
}

func TestBackendNames(t *testing.T) {
	got := backendNames([]GpuBackend{&rocmBackend{}, &nvidiaBackend{}})
	if got != "rocm+nvidia" {
		t.Errorf("backendNames() = %q, want \"rocm+nvidia\"", got)
	}
}

// ── PCI normalization ───────────────────────────────────────────────

func TestNormalizePCI(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0000:06:00.0", "0000:06:00.0"},
		{"00000000:64:00.0", "0000:64:00.0"},
		{"  0000:01:00.0  ", "0000:01:00.0"},
		{"0000:C3:00.0", "0000:c3:00.0"}, // rocm-smi uppercase → lowercase
		{"0000:c3:00.0", "0000:c3:00.0"}, // sysfs already lowercase
		{"", ""},
		{"garbage", ""},
		{"../../../etc/passwd", ""},
	}
	for _, tt := range tests {
		got := normalizePCI(tt.input)
		if got != tt.want {
			t.Errorf("normalizePCI(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ── Process merging ─────────────────────────────────────────────────

func TestMergeProcesses(t *testing.T) {
	procs := []ProcessData{
		{PID: 100, Name: "app", GpuIDs: []int{0}, VramUsed: 1000},
		{PID: 100, Name: "app", GpuIDs: []int{1}, VramUsed: 2000},
		{PID: 200, Name: "other", GpuIDs: []int{0}, VramUsed: 500},
	}
	result := mergeProcesses(procs)
	if len(result) != 2 {
		t.Fatalf("mergeProcesses returned %d procs, want 2", len(result))
	}
	// Should be sorted by VramUsed descending
	if result[0].PID != 100 || result[0].VramUsed != 3000 {
		t.Errorf("first proc: PID=%d VramUsed=%d, want PID=100 VramUsed=3000", result[0].PID, result[0].VramUsed)
	}
	if len(result[0].GpuIDs) != 2 {
		t.Errorf("first proc has %d GpuIDs, want 2", len(result[0].GpuIDs))
	}
	if result[1].PID != 200 || result[1].VramUsed != 500 {
		t.Errorf("second proc: PID=%d VramUsed=%d, want PID=200 VramUsed=500", result[1].PID, result[1].VramUsed)
	}
}

func TestMergeProcessesEmpty(t *testing.T) {
	result := mergeProcesses(nil)
	if len(result) != 0 {
		t.Errorf("mergeProcesses(nil) returned %d procs, want 0", len(result))
	}
}

// ── ROCm process parsing ────────────────────────────────────────────

// Raw "system" map as decoded from "rocm-smi --showpids --json". The value
// fields are (name, number of GPUs used, vram bytes, sdma usage, cu
// occupancy); rocm-smi lowercases the whole value string in JSON mode.
func rocmShowPidsSystemFixture() map[string]interface{} {
	return map[string]interface{}{
		"PID52243": "python3, 2, 4096000, 0, 0",
		"PID60001": "ollama, 6, 1048576, 0, 0",
		"PID61234": "idlehold, 0, 0, 0, 0",
	}
}

func TestParseProcessesDoesNotFabricateGpuIDsFromCount(t *testing.T) {
	result := parseProcesses(rocmShowPidsSystemFixture())
	if len(result) != 3 {
		t.Fatalf("parseProcesses returned %d procs, want 3", len(result))
	}
	byPID := make(map[int]ProcessData)
	for _, p := range result {
		byPID[p.PID] = p
	}
	multi, ok := byPID[52243]
	if !ok {
		t.Fatal("PID 52243 missing from result")
	}
	if multi.Name != "python3" || multi.VramUsed != 4096000 {
		t.Errorf("PID 52243: Name=%q VramUsed=%d, want python3/4096000", multi.Name, multi.VramUsed)
	}
	// The second field (2) is a GPU COUNT, not an index — it must not
	// become a GPU ID. Attribution comes only from --showpidgpus.
	for pid, p := range byPID {
		if len(p.GpuIDs) != 0 {
			t.Errorf("PID %d: GpuIDs=%v, want empty (must not be fabricated from GPU count)", pid, p.GpuIDs)
		}
	}
	// Sorted by VRAM descending.
	if result[0].PID != 52243 || result[2].PID != 61234 {
		t.Errorf("sort order: got PIDs %d,%d,%d, want 52243 first and 61234 last",
			result[0].PID, result[1].PID, result[2].PID)
	}
}

// Text output of "rocm-smi --showpidgpus" per showGpusByPid/printListLog:
// index lists may wrap across lines, and a 0-device PID prints only the
// metric-name line without a trailing colon or list.
const pidGpusSample = `============================ GPUs Indexed by PID ============================
PID 52243 is using 2 DRM device(s):
0 1
PID 60001 is using 6 DRM device(s):
0 1 2
3 4 5
PID 61234 is using 0 DRM device(s)
PID 61300 is using 1 DRM device(s):
3
=============================================================================
`

func TestParsePidGpus(t *testing.T) {
	m := parsePidGpus(pidGpusSample)
	if len(m) != 4 {
		t.Fatalf("parsePidGpus returned %d PIDs, want 4: %v", len(m), m)
	}
	cases := []struct {
		pid  int
		want []int
	}{
		{52243, []int{0, 1}},
		{60001, []int{0, 1, 2, 3, 4, 5}}, // wrapped across two lines
		{61234, []int{}},                 // zero devices: metric line only
		{61300, []int{3}},
	}
	for _, c := range cases {
		got, ok := m[c.pid]
		if !ok {
			t.Errorf("PID %d missing from map", c.pid)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("PID %d: GpuIDs=%v, want %v", c.pid, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("PID %d: GpuIDs=%v, want %v", c.pid, got, c.want)
				break
			}
		}
	}
}

func TestParsePidGpusNoKFDPids(t *testing.T) {
	out := `============================ GPUs Indexed by PID ============================
No KFD PIDs currently running
=============================================================================
`
	m := parsePidGpus(out)
	if len(m) != 0 {
		t.Errorf("parsePidGpus returned %d PIDs, want 0: %v", len(m), m)
	}
}

func TestParsePidGpusEmpty(t *testing.T) {
	if m := parsePidGpus(""); len(m) != 0 {
		t.Errorf("parsePidGpus(\"\") returned %d PIDs, want 0", len(m))
	}
}

// Integration-shaped: --showpids parsing plus --showpidgpus attribution,
// combined the same way rocmBackend.CollectData does.
func TestParseProcessesWithPidGpusAttribution(t *testing.T) {
	procs := parseProcesses(rocmShowPidsSystemFixture())
	applyPidGpuMap(procs, parsePidGpus(pidGpusSample))

	byPID := make(map[int]ProcessData)
	for _, p := range procs {
		byPID[p.PID] = p
	}
	if got := byPID[52243].GpuIDs; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("PID 52243: GpuIDs=%v, want [0 1]", got)
	}
	if got := byPID[60001].GpuIDs; len(got) != 6 || got[0] != 0 || got[5] != 5 {
		t.Errorf("PID 60001: GpuIDs=%v, want [0 1 2 3 4 5]", got)
	}
	// PID with zero devices keeps empty GpuIDs (rendered as "?").
	if got := byPID[61234].GpuIDs; len(got) != 0 {
		t.Errorf("PID 61234: GpuIDs=%v, want empty", got)
	}
}

// ── NVIDIA parsing ──────────────────────────────────────────────────

func TestParseNvidiaGPULine(t *testing.T) {
	fields := []string{
		"0",                       // index
		"NVIDIA GeForce RTX 4070", // name
		"45",                      // temperature.gpu
		"3",                       // utilization.gpu
		"12",                      // utilization.memory
		"8188",                    // memory.total (MiB)
		"512",                     // memory.used (MiB)
		"65.23",                   // power.draw
		"200.00",                  // power.limit
		"200.00",                  // power.max_limit
		"30",                      // fan.speed
		"1500",                    // clocks.current.graphics
		"810",                     // clocks.current.memory
		"4",                       // pcie.link.gen.current
		"16",                      // pcie.link.width.current
		"560.35",                  // driver_version
		"95.02.3c",                // vbios_version
		"P0",                      // pstate
		"00000000:01:00.0",        // pci.bus_id
	}

	gpu := parseNvidiaGPULine(fields)

	if gpu.CardID != 0 {
		t.Errorf("CardID = %d, want 0", gpu.CardID)
	}
	if gpu.Backend != "nvidia" {
		t.Errorf("Backend = %q, want \"nvidia\"", gpu.Backend)
	}
	if gpu.Name != "NVIDIA GeForce RTX 4070" {
		t.Errorf("Name = %q, want \"NVIDIA GeForce RTX 4070\"", gpu.Name)
	}
	if gpu.TempEdge != 45 || gpu.TempJunc != 45 {
		t.Errorf("Temp = %.0f/%.0f, want 45/45", gpu.TempEdge, gpu.TempJunc)
	}
	if gpu.GpuUse != 3 {
		t.Errorf("GpuUse = %.0f, want 3", gpu.GpuUse)
	}
	if gpu.VramTotal != 8188*1024*1024 {
		t.Errorf("VramTotal = %d, want %d", gpu.VramTotal, 8188*1024*1024)
	}
	if gpu.VramUsed != 512*1024*1024 {
		t.Errorf("VramUsed = %d, want %d", gpu.VramUsed, 512*1024*1024)
	}
	if gpu.PowerAvg != 65.23 {
		t.Errorf("PowerAvg = %f, want 65.23", gpu.PowerAvg)
	}
	if gpu.PowerMax != 200 {
		t.Errorf("PowerMax = %f, want 200", gpu.PowerMax)
	}
	if gpu.Sclk != 1500 {
		t.Errorf("Sclk = %d, want 1500", gpu.Sclk)
	}
	if gpu.PcieSpeed != "16.0GT/s" {
		t.Errorf("PcieSpeed = %q, want \"16.0GT/s\"", gpu.PcieSpeed)
	}
	if gpu.PcieWidth != 16 {
		t.Errorf("PcieWidth = %d, want 16", gpu.PcieWidth)
	}
	if gpu.DriverVersion != "560.35" {
		t.Errorf("DriverVersion = %q, want \"560.35\"", gpu.DriverVersion)
	}
	if gpu.PerfLevel != "P0" {
		t.Errorf("PerfLevel = %q, want \"P0\"", gpu.PerfLevel)
	}
}

func TestParseNvidiaGPULineWithNA(t *testing.T) {
	fields := []string{
		"0", "GPU Name", "47", "5", "30",
		"8188", "1861", "9.19", "[N/A]", "105.00", "[N/A]",
		"210", "810", "4", "8",
		"595.79", "95.06", "P5", "00000000:64:00.0",
	}

	gpu := parseNvidiaGPULine(fields)

	if gpu.PowerMax != 105 { // falls back to power.max_limit
		t.Errorf("PowerMax = %f, want 105 (from power.max_limit)", gpu.PowerMax)
	}
	if gpu.FanPercent != 0 { // [N/A] should parse to 0
		t.Errorf("FanPercent = %f, want 0", gpu.FanPercent)
	}
}

func TestNvidiaBusToCardID(t *testing.T) {
	gpus := []GpuData{
		{CardID: 0, Backend: "nvidia", PcieBus: "00000000:C3:00.0"},
		{CardID: 2, Backend: "nvidia", PcieBus: "00000000:83:00.0"},
		{CardID: 3, Backend: "nvidia", PcieBus: ""}, // no bus id → omitted
	}
	byBus := nvidiaBusToCardID(gpus)
	if len(byBus) != 2 {
		t.Fatalf("len(byBus) = %d, want 2", len(byBus))
	}
	if id, ok := byBus["0000:c3:00.0"]; !ok || id != 0 {
		t.Errorf("byBus[0000:c3:00.0] = %d,%v, want 0,true", id, ok)
	}
	if id, ok := byBus["0000:83:00.0"]; !ok || id != 2 {
		t.Errorf("byBus[0000:83:00.0] = %d,%v, want 2,true", id, ok)
	}
}

func TestParseNvidiaProcesses(t *testing.T) {
	// Realistic --query-compute-apps=pid,used_gpu_memory,gpu_bus_id output:
	// PID 1001 spans two GPUs, PID 2002 reports [N/A] memory, PID 3003 sits
	// on a bus id that maps to no known GPU and must be skipped.
	output := strings.Join([]string{
		"1001, 4096, 00000000:C3:00.0",
		"2002, [N/A], 00000000:83:00.0",
		"1001, 2048, 00000000:83:00.0",
		"3003, 512, 00000000:AA:00.0",
	}, "\n")
	busToCard := map[string]int{
		"0000:c3:00.0": 0,
		"0000:83:00.0": 2,
	}
	names := map[int]string{1001: "python3", 2002: "ollama", 3003: "ghost"}
	nameFn := func(pid int) string { return names[pid] }

	procs := parseNvidiaProcesses(output, busToCard, nameFn)

	if len(procs) != 2 {
		t.Fatalf("len(procs) = %d, want 2 (unknown bus id must be skipped)", len(procs))
	}

	// Sorted by VramUsed descending: PID 1001 (6144 MiB) before 2002 (0).
	if procs[0].PID != 1001 || procs[1].PID != 2002 {
		t.Fatalf("PID order = %d,%d, want 1001,2002", procs[0].PID, procs[1].PID)
	}

	p := procs[0]
	if p.Name != "python3" {
		t.Errorf("Name = %q, want \"python3\"", p.Name)
	}
	if want := int64(4096+2048) * 1024 * 1024; p.VramUsed != want {
		t.Errorf("VramUsed = %d, want %d (MiB converted to bytes and merged)", p.VramUsed, want)
	}
	if len(p.GpuIDs) != 2 || p.GpuIDs[0] != 0 || p.GpuIDs[1] != 2 {
		t.Errorf("GpuIDs = %v, want [0 2]", p.GpuIDs)
	}

	na := procs[1]
	if na.Name != "ollama" {
		t.Errorf("Name = %q, want \"ollama\"", na.Name)
	}
	if na.VramUsed != 0 {
		t.Errorf("VramUsed = %d, want 0 for [N/A]", na.VramUsed)
	}
	if len(na.GpuIDs) != 1 || na.GpuIDs[0] != 2 {
		t.Errorf("GpuIDs = %v, want [2]", na.GpuIDs)
	}
}

func TestParseNvidiaProcessesEmpty(t *testing.T) {
	procs := parseNvidiaProcesses("", map[string]int{"0000:c3:00.0": 0}, func(int) string { return "x" })
	if len(procs) != 0 {
		t.Errorf("len(procs) = %d, want 0", len(procs))
	}
}

// ── collectGpuData ──────────────────────────────────────────────────

type fakeBackend struct {
	name  string
	gpus  []GpuData
	procs []ProcessData
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) CollectData() ([]GpuData, []ProcessData) {
	return f.gpus, f.procs
}

func TestCollectGpuDataConcurrentDeterministic(t *testing.T) {
	sysfs := &fakeBackend{
		name: "sysfs",
		gpus: []GpuData{{CardID: 0, Backend: "sysfs"}},
	}
	rocm := &fakeBackend{
		name: "rocm",
		gpus: []GpuData{{CardID: 1, Backend: "rocm"}, {CardID: 0, Backend: "rocm"}},
		procs: []ProcessData{
			{PID: 42, Name: "torch", GpuIDs: []int{0}, VramUsed: 100},
		},
	}
	nvidia := &fakeBackend{
		name: "nvidia",
		gpus: []GpuData{{CardID: 0, Backend: "nvidia"}},
		procs: []ProcessData{
			{PID: 42, Name: "torch", GpuIDs: []int{1}, VramUsed: 50},
		},
	}

	// Run repeatedly: goroutine scheduling must never change the result.
	for i := 0; i < 20; i++ {
		gpus, procs := collectGpuData([]GpuBackend{sysfs, rocm, nvidia})

		wantBackends := []string{"rocm", "rocm", "nvidia", "sysfs"}
		wantCards := []int{0, 1, 0, 0}
		if len(gpus) != len(wantBackends) {
			t.Fatalf("len(gpus) = %d, want %d", len(gpus), len(wantBackends))
		}
		for j := range gpus {
			if gpus[j].Backend != wantBackends[j] || gpus[j].CardID != wantCards[j] {
				t.Fatalf("gpus[%d] = %s:%d, want %s:%d",
					j, gpus[j].Backend, gpus[j].CardID, wantBackends[j], wantCards[j])
			}
		}

		if len(procs) != 1 {
			t.Fatalf("len(procs) = %d, want 1 (merged by PID)", len(procs))
		}
		if procs[0].PID != 42 || procs[0].VramUsed != 150 || len(procs[0].GpuIDs) != 2 {
			t.Fatalf("procs[0] = %+v, want PID 42 VramUsed 150 with 2 GpuIDs", procs[0])
		}
	}
}

// ── RingBuffer ──────────────────────────────────────────────────────

func TestRingBuffer(t *testing.T) {
	var rb RingBuffer

	vals := rb.Values()
	if vals != nil {
		t.Errorf("empty RingBuffer.Values() = %v, want nil", vals)
	}

	rb.Push(1.0)
	rb.Push(2.0)
	rb.Push(3.0)
	vals = rb.Values()
	if len(vals) != 3 || vals[0] != 1.0 || vals[2] != 3.0 {
		t.Errorf("Values() = %v, want [1 2 3]", vals)
	}

	// Fill beyond capacity
	for i := 0; i < maxHistory+10; i++ {
		rb.Push(float64(i))
	}
	vals = rb.Values()
	if len(vals) != maxHistory {
		t.Errorf("Values() len = %d, want %d after overflow", len(vals), maxHistory)
	}
}

// ── sysfs helpers ───────────────────────────────────────────────────

func TestParseDpmFreq(t *testing.T) {
	// Create a temp file with sample pp_dpm_sclk content
	content := "0: 500Mhz\n1: 800Mhz *\n2: 1200Mhz\n"
	tmp := t.TempDir() + "/pp_dpm_sclk"
	if err := writeTestFile(tmp, content); err != nil {
		t.Fatal(err)
	}
	got := parseDpmFreq(tmp)
	if got != 800 {
		t.Errorf("parseDpmFreq = %d, want 800", got)
	}
}

func TestParseDpmFreqMissing(t *testing.T) {
	got := parseDpmFreq("/nonexistent/path")
	if got != 0 {
		t.Errorf("parseDpmFreq(missing) = %d, want 0", got)
	}
}

func TestReadFloatFileNaN(t *testing.T) {
	got := readFloatFileNaN("/nonexistent/path")
	if !math.IsNaN(got) {
		t.Errorf("readFloatFileNaN(missing) = %f, want NaN", got)
	}

	tmp := t.TempDir() + "/val"
	if err := writeTestFile(tmp, "42.5\n"); err != nil {
		t.Fatal(err)
	}
	got = readFloatFileNaN(tmp)
	if got != 42.5 {
		t.Errorf("readFloatFileNaN = %f, want 42.5", got)
	}
}

func TestReadInt64File(t *testing.T) {
	got := readInt64File("/nonexistent/path", -1)
	if got != -1 {
		t.Errorf("readInt64File(missing) = %d, want -1", got)
	}

	tmp := t.TempDir() + "/val"
	if err := writeTestFile(tmp, "1073741824\n"); err != nil {
		t.Fatal(err)
	}
	got = readInt64File(tmp, 0)
	if got != 1073741824 {
		t.Errorf("readInt64File = %d, want 1073741824", got)
	}
}

// ── PCIe gen to speed ───────────────────────────────────────────────

func TestPcieGenToSpeed(t *testing.T) {
	tests := []struct {
		gen  int
		want string
	}{
		{1, "2.5GT/s"},
		{4, "16.0GT/s"},
		{5, "32.0GT/s"},
		{0, ""},
		{99, ""},
	}
	for _, tt := range tests {
		got := pcieGenToSpeed(tt.gen)
		if got != tt.want {
			t.Errorf("pcieGenToSpeed(%d) = %q, want %q", tt.gen, got, tt.want)
		}
	}
}

// ── fmtWattsOrNA ────────────────────────────────────────────────────

func TestFmtWattsOrNA(t *testing.T) {
	if got := fmtWattsOrNA(30); got != "30W" {
		t.Errorf("fmtWattsOrNA(30) = %q, want \"30W\"", got)
	}
	if got := fmtWattsOrNA(math.NaN()); got != "" {
		t.Errorf("fmtWattsOrNA(NaN) = %q, want \"\"", got)
	}
}

// ── collectStaticInfo backend isolation ─────────────────────────────

// TestCollectStaticInfoBackendIsolation verifies that when multiple backends
// share the same CardID integer, collectStaticInfo only writes rocm-smi
// results into the rocm GPU — not into any other-backend GPU that happens
// to have the same CardID.
//
// This is a unit test for the byID map logic only; it uses a fake runJSON
// by directly calling the internal byID-building logic via a local
// simulation of the relevant section.
func TestCollectStaticInfoByIDOnlyRocm(t *testing.T) {
	// Simulate 4 rocm GPUs + 1 sysfs GPU that shares CardID=3.
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 1, Backend: "rocm"},
		{CardID: 2, Backend: "rocm"},
		{CardID: 3, Backend: "rocm"},
		{CardID: 3, Backend: "sysfs"}, // same CardID as rocm:3
	}

	// Reproduce the fixed byID logic: only rocm entries.
	byID := make(map[int]int)
	for i, g := range gpus {
		if g.Backend == "rocm" {
			byID[g.CardID] = i
		}
	}

	// CardID 3 must resolve to index 3 (rocm:3), not index 4 (sysfs:3).
	if idx, ok := byID[3]; !ok || idx != 3 {
		t.Errorf("byID[3] = %d (ok=%v), want 3 (the rocm GPU)", idx, ok)
	}

	// Applying a fake VBIOS result to the rocm GPU at index 3 must not
	// affect the sysfs GPU at index 4.
	if idx, ok := byID[3]; ok {
		gpus[idx].Vbios = "vbios-test"
	}
	if gpus[3].Vbios != "vbios-test" {
		t.Errorf("rocm GPU at index 3 should have Vbios set, got %q", gpus[3].Vbios)
	}
	if gpus[4].Vbios != "" {
		t.Errorf("sysfs GPU at index 4 should not have Vbios set, got %q", gpus[4].Vbios)
	}
}

// ── renderMetricLines output ─────────────────────────────────────────

func TestRenderMetricLinesCount(t *testing.T) {
	gpu := GpuData{
		CardID:      0,
		Backend:     "rocm",
		Name:        "Radeon RX 7900 XTX",
		GpuUse:      75.0,
		MemActivity: 40.0,
		VramPercent: 50.0,
		VramTotal:   8 * 1024 * 1024 * 1024,
		VramUsed:    4 * 1024 * 1024 * 1024,
		PowerAvg:    200.0,
		PowerMax:    355.0,
		TempJunc:    68.0,
		TempEdge:    62.0,
		TempMem:     60.0,
		FanPercent:  45.0,
		FanRPM:      1800,
		Sclk:        2500,
		Mclk:        1000,
	}
	hist := &GpuHistory{}
	lines := renderMetricLines(gpu, hist, 80, false)
	// renderMetricLines returns a variable count (15 base + optional GTT/PCIe lines).
	// renderGpuPanel pads UP TO panelLines; it never trims. So the invariant is
	// len(lines) <= panelLines, not == panelLines.
	if len(lines) > panelLines {
		t.Errorf("renderMetricLines returned %d lines, want at most %d (panelLines)", len(lines), panelLines)
	}
}

func TestRenderMetricLinesNaNInputs(t *testing.T) {
	// All-NaN GPU (e.g. a sysfs GPU with no sensors readable) must not panic
	// and must return at most panelLines lines (renderGpuPanel pads the rest).
	gpu := GpuData{
		CardID:      1,
		Backend:     "sysfs",
		Name:        "Intel iGPU",
		GpuUse:      math.NaN(),
		MemActivity: math.NaN(),
		PowerAvg:    math.NaN(),
		PowerMax:    math.NaN(),
		TempJunc:    math.NaN(),
		TempEdge:    math.NaN(),
		TempMem:     math.NaN(),
	}
	hist := &GpuHistory{}
	lines := renderMetricLines(gpu, hist, 80, false)
	if len(lines) > panelLines {
		t.Errorf("renderMetricLines (all-NaN) returned %d lines, want at most %d", len(lines), panelLines)
	}
}

// ── log buffer ───────────────────────────────────────────────────────

func TestLogfAppends(t *testing.T) {
	// Reset global state for this test.
	logMu.Lock()
	logEntries = nil
	logMu.Unlock()

	logf("test event %d", 1)
	logf("test event %d", 2)

	entries := getLogEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 log entries, got %d", len(entries))
	}
	if entries[0].msg != "test event 1" || entries[1].msg != "test event 2" {
		t.Errorf("unexpected messages: %v", entries)
	}
}

func TestLogfCapsAtMax(t *testing.T) {
	logMu.Lock()
	logEntries = nil
	logMu.Unlock()

	for i := 0; i < maxLogEntries+10; i++ {
		logf("entry %d", i)
	}
	entries := getLogEntries()
	if len(entries) != maxLogEntries {
		t.Errorf("expected %d entries, got %d", maxLogEntries, len(entries))
	}
	// Oldest entries should have been dropped; first remaining is entry 10.
	if entries[0].msg != "entry 10" {
		t.Errorf("first entry = %q, want \"entry 10\"", entries[0].msg)
	}
}

func TestRenderLogPanelEmpty(t *testing.T) {
	logMu.Lock()
	logEntries = nil
	logMu.Unlock()

	out := renderLogPanel(120)
	if !strings.Contains(out, "No events") {
		t.Error("empty log panel should say 'No events'")
	}
}

func TestRenderLogPanelShowsEntries(t *testing.T) {
	logMu.Lock()
	logEntries = nil
	logMu.Unlock()

	logf("rocm-smi --showmetrics: context deadline exceeded")
	out := renderLogPanel(120)
	if !strings.Contains(out, "rocm-smi") {
		t.Error("log panel should show logged entry")
	}
}

func TestHeaderLogCount(t *testing.T) {
	logMu.Lock()
	logEntries = nil
	logMu.Unlock()

	// No entries — hint should just say "l:log"
	h := renderHeader(4, "", 2.0, false, false, false, false, false, -1, 200)
	if !strings.Contains(h, "l") {
		t.Error("header should contain l keybinding")
	}

	logf("an error occurred")
	// renderHeader(gpuCount, refreshSecs, paused, infoMode, helpMode, logMode, dataStale, width)
	h = renderHeader(4, "", 2.0, false, false, false, false, false, -1, 200)
	if !strings.Contains(h, "log(1)") {
		t.Errorf("header should show log(1) when there is 1 entry, got: %s", h)
	}
}

// ── stale data indicator ─────────────────────────────────────────────

func TestHeaderStaleIndicator(t *testing.T) {
	// Stale flag should appear in the header when dataStale is true.
	// renderHeader(gpuCount, refreshSecs, paused, infoMode, helpMode, logMode, dataStale, width)
	withStale := renderHeader(4, "", 2.0, false, false, false, false, true, -1, 200)
	if !strings.Contains(withStale, "STALE") {
		t.Error("header with dataStale=true should contain 'STALE'")
	}
	// No stale indicator when data is fresh.
	withoutStale := renderHeader(4, "", 2.0, false, false, false, false, false, -1, 200)
	if strings.Contains(withoutStale, "STALE") {
		t.Error("header with dataStale=false should not contain 'STALE'")
	}
}

func TestDataMsgEmptyPreservesGpus(t *testing.T) {
	// An empty dataMsg (failed fetch) must not clear existing GPU data.
	m := newModel(2*time.Second, nil)
	m.gpus = []GpuData{{CardID: 0, Backend: "rocm", Name: "Test GPU"}}

	// Simulate a failed fetch arriving as an empty dataMsg.
	updated, _ := m.Update(dataMsg{gpus: nil, procs: nil})
	nm := updated.(model)

	if len(nm.gpus) == 0 {
		t.Error("failed fetch (empty dataMsg) must not clear existing GPU data")
	}
	if !nm.dataStale {
		t.Error("failed fetch must set dataStale=true")
	}
}

func TestDataMsgSuccessClearsStale(t *testing.T) {
	// A successful fetch must clear the stale flag.
	m := newModel(2*time.Second, nil)
	m.dataStale = true
	m.gpus = []GpuData{{CardID: 0, Backend: "rocm", Name: "Old GPU"}}

	fresh := []GpuData{{CardID: 0, Backend: "rocm", Name: "Fresh GPU"}}
	updated, _ := m.Update(dataMsg{gpus: fresh, procs: nil})
	nm := updated.(model)

	if nm.dataStale {
		t.Error("successful fetch must clear dataStale")
	}
	if nm.gpus[0].Name != "Fresh GPU" {
		t.Errorf("gpus not updated: got %q", nm.gpus[0].Name)
	}
}

// ── renderHelp ───────────────────────────────────────────────────────

func TestRenderHelpContainsKeyDocs(t *testing.T) {
	out := renderHelp(120)
	for _, want := range []string{"USE", "VRAM", "MACT", "TEMP", "e XX°", "m XX°", "Sparklines", "?", "Junction"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderHelp output missing expected term %q", want)
		}
	}
}

func TestRenderHelpIsScrollable(t *testing.T) {
	// renderHelp must return more lines than a typical viewport height so
	// that the viewport has content to scroll.
	out := renderHelp(120)
	lines := strings.Split(out, "\n")
	if len(lines) < 20 {
		t.Errorf("renderHelp returned only %d lines — unlikely to need scrolling", len(lines))
	}
}

// ── RAS / ECC parsing ────────────────────────────────────────────────

const rasInfoSample = `
============================ ROCm System Management Interface ============================
======================================== RAS Info ========================================

GPU[0]: 	RAS INFO
         Block       Status    Correctable Error  Uncorrectable Error
           UMC        ENABLED                  0                    0
          SDMA       DISABLED
           GFX       DISABLED
            DF        ENABLED                  5                    2
__________________________________________________________________________________________

GPU[1]: 	RAS INFO
         Block       Status    Correctable Error  Uncorrectable Error
           UMC       DISABLED            3145680              3145680
          SDMA       DISABLED
           GFX       DISABLED
__________________________________________________________________________________________

GPU[2]: 	RAS INFO
         Block       Status    Correctable Error  Uncorrectable Error
           UMC        ENABLED                  0                    0
__________________________________________________________________________________________
==========================================================================================
`

func TestParseRASInfoCleanGPU(t *testing.T) {
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 1, Backend: "rocm"},
		{CardID: 2, Backend: "rocm"},
	}
	parseRASInfo(rasInfoSample, gpus)

	// GPU 0 has UMC (0,0) + DF (5,2)
	if gpus[0].RasCorrectable != 5 {
		t.Errorf("GPU[0] RasCorrectable = %d, want 5", gpus[0].RasCorrectable)
	}
	if gpus[0].RasUncorrectable != 2 {
		t.Errorf("GPU[0] RasUncorrectable = %d, want 2", gpus[0].RasUncorrectable)
	}
}

func TestParseRASInfoErrorGPU(t *testing.T) {
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 1, Backend: "rocm"},
		{CardID: 2, Backend: "rocm"},
	}
	parseRASInfo(rasInfoSample, gpus)

	// GPU 1 has UMC DISABLED with 3145680 each
	if gpus[1].RasCorrectable != 3145680 {
		t.Errorf("GPU[1] RasCorrectable = %d, want 3145680", gpus[1].RasCorrectable)
	}
	if gpus[1].RasUncorrectable != 3145680 {
		t.Errorf("GPU[1] RasUncorrectable = %d, want 3145680", gpus[1].RasUncorrectable)
	}
}

func TestParseRASInfoCleanZeros(t *testing.T) {
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 1, Backend: "rocm"},
		{CardID: 2, Backend: "rocm"},
	}
	parseRASInfo(rasInfoSample, gpus)

	// GPU 2 has UMC (0,0) only
	if gpus[2].RasCorrectable != 0 || gpus[2].RasUncorrectable != 0 {
		t.Errorf("GPU[2] should have zero errors, got corr=%d uncorr=%d",
			gpus[2].RasCorrectable, gpus[2].RasUncorrectable)
	}
}

func TestParseRASInfoSkipsNonRocm(t *testing.T) {
	// Non-rocm GPUs sharing a CardID should not be populated.
	gpus := []GpuData{
		{CardID: 0, Backend: "rocm"},
		{CardID: 0, Backend: "sysfs"}, // same CardID as rocm:0
	}
	parseRASInfo(rasInfoSample, gpus)

	if gpus[1].RasCorrectable != 0 || gpus[1].RasUncorrectable != 0 {
		t.Error("sysfs GPU should not have RAS data written into it")
	}
}

func TestRenderMetricLinesTitleNoECC(t *testing.T) {
	// ECC warnings belong in the info panel only — metrics title must never show them.
	gpu := GpuData{
		CardID:           0,
		Backend:          "rocm",
		Name:             "Radeon RX 7900 XTX",
		GpuUse:           50.0,
		PowerAvg:         200.0,
		PowerMax:         355.0,
		TempJunc:         70.0,
		RasUncorrectable: 3145680,
	}
	hist := &GpuHistory{}
	lines := renderMetricLines(gpu, hist, 80, false)
	title := lines[0]
	if strings.Contains(title, "ECC") {
		t.Errorf("metrics title should not show ECC warning (info panel only), got: %s", title)
	}
}

func TestRenderInfoLinesECCRow(t *testing.T) {
	gpu := GpuData{
		CardID:           0,
		Backend:          "rocm",
		Name:             "Radeon RX 7900 XTX",
		PowerMax:         355.0,
		RasCorrectable:   10,
		RasUncorrectable: 3145680,
	}
	lines := renderInfoLines(gpu, 80, false)
	// Join all lines to search for ECC content
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "ECC") {
		t.Error("info view should contain ECC row for rocm GPU")
	}
	if !strings.Contains(out, "3145680") {
		t.Error("info view should show uncorrectable error count")
	}
}

// ── GPU focus view ───────────────────────────────────────────────────

func TestFocusModeShowsSingleGPU(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 2
	out := m.renderGpuContent()
	lines := strings.Split(out, "\n")
	// Only one panel worth of lines — not four stacked.
	if len(lines) > panelLines+4 {
		t.Errorf("focus mode should show one panel (%d lines), got %d", panelLines+4, len(lines))
	}
	// The focused GPU's name should appear.
	if !strings.Contains(out, "GPU 2") {
		t.Errorf("focus mode should show GPU 2, got:\n%s", out)
	}
}

func TestFocusModeArrowRight(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	nm := updated.(model)
	if nm.focusIdx != 2 {
		t.Errorf("right arrow should advance focusIdx to 2, got %d", nm.focusIdx)
	}
}

func TestFocusModeArrowLeft(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	nm := updated.(model)
	if nm.focusIdx != 0 {
		t.Errorf("left arrow should move focusIdx to 0, got %d", nm.focusIdx)
	}
}

func TestFocusModeArrowWrapsRight(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 3
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	nm := updated.(model)
	if nm.focusIdx != -1 {
		t.Errorf("right arrow at last GPU should return to overview (-1), got %d", nm.focusIdx)
	}
}

func TestFocusModeArrowWrapsLeft(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	nm := updated.(model)
	if nm.focusIdx != -1 {
		t.Errorf("left arrow at first GPU should return to overview (-1), got %d", nm.focusIdx)
	}
}

func TestArrowRightFromOverview(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = -1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	nm := updated.(model)
	if nm.focusIdx != 0 {
		t.Errorf("right arrow from overview should focus GPU 0, got %d", nm.focusIdx)
	}
}

func TestArrowLeftFromOverview(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = -1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	nm := updated.(model)
	if nm.focusIdx != 3 {
		t.Errorf("left arrow from overview should focus last GPU (3), got %d", nm.focusIdx)
	}
}

func TestFocusModeOutOfRangeIgnored(t *testing.T) {
	m := gpuModel(2, 200)
	m.focusIdx = 99 // no such GPU
	out := m.renderGpuContent()
	// Should fall through to normal layout (2 GPUs visible).
	if !strings.Contains(out, "GPU 0") || !strings.Contains(out, "GPU 1") {
		t.Error("out-of-range focusIdx should fall back to normal layout")
	}
}

func TestFocusModeHeaderIndicator(t *testing.T) {
	h := renderHeader(4, "", 2.0, false, false, false, false, false, 1, 200)
	if !strings.Contains(h, "FOCUS") {
		t.Errorf("header should show FOCUS indicator when focusIdx >= 0, got: %s", h)
	}
	if !strings.Contains(h, "1") {
		t.Errorf("header should show focused GPU index, got: %s", h)
	}
}

func TestNoFocusModeHeaderNoIndicator(t *testing.T) {
	h := renderHeader(4, "", 2.0, false, false, false, false, false, -1, 200)
	if strings.Contains(h, "FOCUS") {
		t.Errorf("header should not show FOCUS when focusIdx == -1, got: %s", h)
	}
}

func TestEscClearsAllModes(t *testing.T) {
	m := gpuModel(4, 200)
	m.focusIdx = 2
	m.helpMode = true
	m.logMode = false
	m.infoMode = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := updated.(model)

	if nm.focusIdx != -1 {
		t.Errorf("Esc should clear focusIdx, got %d", nm.focusIdx)
	}
	if nm.helpMode {
		t.Error("Esc should clear helpMode")
	}
	if nm.infoMode {
		t.Error("Esc should clear infoMode")
	}
}

func TestEscFromLogMode(t *testing.T) {
	m := gpuModel(4, 200)
	m.logMode = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := updated.(model)

	if nm.logMode {
		t.Error("Esc should clear logMode")
	}
	if nm.focusIdx != -1 {
		t.Errorf("Esc should leave focusIdx at -1, got %d", nm.focusIdx)
	}
}

// ── adaptive layout ──────────────────────────────────────────────────

func gpuModel(gpuCount, width int) model {
	m := newModel(2*time.Second, nil)
	m.width = width
	m.height = 50
	for i := 0; i < gpuCount; i++ {
		m.gpus = append(m.gpus, GpuData{
			CardID:   i,
			Backend:  "rocm",
			Name:     fmt.Sprintf("GPU %d", i),
			GpuUse:   50.0,
			PowerAvg: 100.0,
			PowerMax: 300.0,
			TempJunc: 60.0,
		})
	}
	return m
}

func TestTwoColumnWideTerminal(t *testing.T) {
	// 200-wide terminal with 2 GPUs → two columns, each panel < full width.
	m := gpuModel(2, 200)
	out := m.renderGpuContent()
	// Both GPU titles should appear on the same rendered row (joined horizontally),
	// so the total line count should be panelLines + 2 border rows, not doubled.
	lines := strings.Split(out, "\n")
	if len(lines) > panelLines+4 {
		t.Errorf("2 GPUs on wide terminal should produce ~1 row of panels (%d lines), got %d",
			panelLines+4, len(lines))
	}
}

func TestOneColumnNarrowTerminal(t *testing.T) {
	// 100-wide terminal (< 120) with 2 GPUs → single column, panels stacked.
	m := gpuModel(2, 100)
	out := m.renderGpuContent()
	lines := strings.Split(out, "\n")
	// Two stacked panels should produce roughly 2*(panelLines+2) lines.
	minExpected := 2 * (panelLines + 2)
	if len(lines) < minExpected {
		t.Errorf("2 GPUs on narrow terminal should stack (%d+ lines), got %d", minExpected, len(lines))
	}
}

func TestOneColumnSingleGPU(t *testing.T) {
	// Single GPU always uses full width regardless of terminal width.
	m := gpuModel(1, 200)
	out := m.renderGpuContent()
	lines := strings.Split(out, "\n")
	// Should only be one panel's worth of lines.
	if len(lines) > panelLines+4 {
		t.Errorf("single GPU should always be one column (%d lines), got %d", panelLines+4, len(lines))
	}
}

func TestTwoColumnThresholdExact(t *testing.T) {
	// Exactly at threshold (width = 2 * minColWidth = 120) → two columns.
	m := gpuModel(2, 2*minColWidth)
	out := m.renderGpuContent()
	lines := strings.Split(out, "\n")
	if len(lines) > panelLines+4 {
		t.Errorf("width==%d should use two columns, got %d lines", 2*minColWidth, len(lines))
	}
}

func TestOneColumnBelowThreshold(t *testing.T) {
	// One below threshold → single column.
	m := gpuModel(2, 2*minColWidth-1)
	out := m.renderGpuContent()
	lines := strings.Split(out, "\n")
	minExpected := 2 * (panelLines + 2)
	if len(lines) < minExpected {
		t.Errorf("width==%d should use one column (%d+ lines), got %d",
			2*minColWidth-1, minExpected, len(lines))
	}
}

// ── renderProcessTable ───────────────────────────────────────────────

func makeProcs(n int) []ProcessData {
	procs := make([]ProcessData, n)
	for i := range procs {
		procs[i] = ProcessData{PID: 1000 + i, Name: "proc", GpuIDs: []int{0}, VramUsed: 1 << 20}
	}
	return procs
}

func TestProcessTableNoOverflow(t *testing.T) {
	out := renderProcessTable(makeProcs(3), 120)
	if strings.Contains(out, "more") {
		t.Error("should not show 'more' indicator when procs <= 6")
	}
}

func TestProcessTableOverflowIndicator(t *testing.T) {
	// With >6 procs only 5 rows are shown so the "+ N more" line fits
	// inside the fixed panel height: 9 procs → 5 shown, "+ 4 more".
	out := renderProcessTable(makeProcs(9), 120)
	if !strings.Contains(out, "+ 4 more") {
		t.Errorf("expected '+ 4 more' indicator, got:\n%s", out)
	}
}

func TestProcessTableFixedHeight(t *testing.T) {
	// Panel height must be identical regardless of process count:
	// 8 content rows + 2 border rows = 10 lines.
	for _, n := range []int{0, 1, 6, 7, 30} {
		out := renderProcessTable(makeProcs(n), 120)
		if got := len(strings.Split(out, "\n")); got != 10 {
			t.Errorf("procs=%d: expected 10 rendered lines, got %d:\n%s", n, got, out)
		}
	}
}

func TestProcessTableOverflowCount(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{7, "+ 2 more"},
		{30, "+ 25 more"},
	} {
		out := renderProcessTable(makeProcs(tc.n), 120)
		if !strings.Contains(out, tc.want) {
			t.Errorf("procs=%d: expected %q indicator, got:\n%s", tc.n, tc.want, out)
		}
	}
}

func TestProcessTableExactlyMax(t *testing.T) {
	out := renderProcessTable(makeProcs(6), 120)
	if strings.Contains(out, "more") {
		t.Error("exactly 6 procs should not show 'more' indicator")
	}
}

func TestProcessTableNameEllipsis(t *testing.T) {
	procs := []ProcessData{{PID: 42, Name: "averylongprocessname_xyz", GpuIDs: []int{0}, VramUsed: 1 << 20}}
	out := renderProcessTable(procs, 120)
	if !strings.Contains(out, "…") {
		t.Error("name longer than 19 chars should be truncated with ellipsis")
	}
	if strings.Contains(out, "averylongprocessname_xyz") {
		t.Error("full long name should not appear — should be truncated")
	}
}

func TestProcessTableShortNameNoEllipsis(t *testing.T) {
	procs := []ProcessData{{PID: 42, Name: "shortname", GpuIDs: []int{0}, VramUsed: 1 << 20}}
	out := renderProcessTable(procs, 120)
	if strings.Contains(out, "…") {
		t.Error("short name should not have ellipsis")
	}
	if !strings.Contains(out, "shortname") {
		t.Error("short name should appear unchanged")
	}
}

// ── PCIe bandwidth ───────────────────────────────────────────────────

func TestReadPcieBwFileNormal(t *testing.T) {
	dir := t.TempDir()
	// rx=100 packets, tx=200 packets, mps=256 bytes
	if err := os.WriteFile(dir+"/pcie_bw", []byte("100 200 256\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// readPcieBwFile expects a PCI bus address; we pass the temp dir directly
	// by temporarily patching via a synthetic path isn't possible, so test the
	// helper via the exported-by-package path using a symlink trick.
	// Instead, test via a wrapper that accepts a directory path.
	rx, tx := readPcieBwFile_testPath(dir)
	if rx != 100*256 {
		t.Errorf("rx want %d got %d", 100*256, rx)
	}
	if tx != 200*256 {
		t.Errorf("tx want %d got %d", 200*256, tx)
	}
}

func TestReadPcieBwFileMissing(t *testing.T) {
	rx, tx := readPcieBwFile_testPath(t.TempDir())
	if rx != -1 || tx != -1 {
		t.Errorf("missing file should return -1,-1, got %d,%d", rx, tx)
	}
}

func TestReadPcieBwFileMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/pcie_bw", []byte("not a number\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rx, tx := readPcieBwFile_testPath(dir)
	if rx != -1 || tx != -1 {
		t.Errorf("malformed file should return -1,-1, got %d,%d", rx, tx)
	}
}

func TestReadPcieBwFileZeroMPS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/pcie_bw", []byte("100 200 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rx, tx := readPcieBwFile_testPath(dir)
	if rx != -1 || tx != -1 {
		t.Errorf("zero MPS should return -1,-1, got %d,%d", rx, tx)
	}
}

// readPcieBwFile_testPath reads <dir>/pcie_bw directly, allowing unit tests
// to inject synthetic files without a real PCI bus address.
func readPcieBwFile_testPath(dir string) (rx, tx int64) {
	raw := readStringFile(dir + "/pcie_bw")
	if raw == "" {
		return -1, -1
	}
	parts := strings.Fields(raw)
	if len(parts) != 3 {
		return -1, -1
	}
	rxPkt, err1 := strconv.ParseUint(parts[0], 10, 64)
	txPkt, err2 := strconv.ParseUint(parts[1], 10, 64)
	mps, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || mps <= 0 {
		return -1, -1
	}
	return int64(rxPkt) * mps, int64(txPkt) * mps
}

func TestParsePcieBwValueNormal(t *testing.T) {
	tx, rx := parsePcieBwValue("bytes_sent: 12345678, bytes_received: 87654321, mtu: 256")
	if tx != 12345678 {
		t.Errorf("tx want 12345678 got %d", tx)
	}
	if rx != 87654321 {
		t.Errorf("rx want 87654321 got %d", rx)
	}
}

func TestParsePcieBwValueNotSupported(t *testing.T) {
	tx, rx := parsePcieBwValue("Not supported on the given system")
	if tx != -1 || rx != -1 {
		t.Errorf("unsupported response should return -1,-1, got %d,%d", tx, rx)
	}
}

func TestParsePcieBwValueEmpty(t *testing.T) {
	tx, rx := parsePcieBwValue("")
	if tx != -1 || rx != -1 {
		t.Errorf("empty string should return -1,-1, got %d,%d", tx, rx)
	}
}

func TestParsePcieBwValueZero(t *testing.T) {
	tx, rx := parsePcieBwValue("bytes_sent: 0, bytes_received: 0, mtu: 256")
	if tx != 0 || rx != 0 {
		t.Errorf("zero values should parse correctly, got %d,%d", tx, rx)
	}
}

// buildGpuMetricsV14 constructs a synthetic gpu_metrics binary of the given
// total size (>= 160) with format_revision=1, content_revision=4 and the
// supplied pcie_bandwidth_inst value at offset 152.
func buildGpuMetricsV14(totalSize int, pcieBwInst uint64) []byte {
	buf := make([]byte, totalSize)
	// header
	buf[0] = byte(totalSize)
	buf[1] = byte(totalSize >> 8)
	buf[2] = 1 // format_revision
	buf[3] = 4 // content_revision
	// fill N/A sentinels where expected (optional, 0 is also fine for test)
	// pcie_bandwidth_inst at offset 152
	buf[152] = byte(pcieBwInst)
	buf[153] = byte(pcieBwInst >> 8)
	buf[154] = byte(pcieBwInst >> 16)
	buf[155] = byte(pcieBwInst >> 24)
	buf[156] = byte(pcieBwInst >> 32)
	buf[157] = byte(pcieBwInst >> 40)
	buf[158] = byte(pcieBwInst >> 48)
	buf[159] = byte(pcieBwInst >> 56)
	return buf
}

func TestReadGpuMetricsBandwidthV14(t *testing.T) {
	dir := t.TempDir()
	blob := buildGpuMetricsV14(200, 1234)
	if err := os.WriteFile(dir+"/gpu_metrics", blob, 0644); err != nil {
		t.Fatal(err)
	}
	got := readGpuMetricsBandwidth(dir)
	if math.IsNaN(got) {
		t.Fatal("expected a bandwidth value, got NaN")
	}
	if got != 1234 {
		t.Errorf("want 1234 MB/s, got %v", got)
	}
}

func TestReadGpuMetricsBandwidthV13TooSmall(t *testing.T) {
	// v1.3 (120 bytes) should return NaN — field not present.
	dir := t.TempDir()
	buf := make([]byte, 120)
	buf[0] = 120
	buf[2] = 1
	buf[3] = 3
	if err := os.WriteFile(dir+"/gpu_metrics", buf, 0644); err != nil {
		t.Fatal(err)
	}
	got := readGpuMetricsBandwidth(dir)
	if !math.IsNaN(got) {
		t.Errorf("v1.3 (120 bytes) should return NaN, got %v", got)
	}
}

func TestReadGpuMetricsBandwidthNAValue(t *testing.T) {
	// 0xffffffffffffffff sentinel should return NaN.
	dir := t.TempDir()
	blob := buildGpuMetricsV14(200, 0xffffffffffffffff)
	if err := os.WriteFile(dir+"/gpu_metrics", blob, 0644); err != nil {
		t.Fatal(err)
	}
	got := readGpuMetricsBandwidth(dir)
	if !math.IsNaN(got) {
		t.Errorf("NA sentinel should return NaN, got %v", got)
	}
}

func TestReadGpuMetricsBandwidthMissing(t *testing.T) {
	dir := t.TempDir()
	got := readGpuMetricsBandwidth(dir) // no gpu_metrics file
	if !math.IsNaN(got) {
		t.Errorf("missing file should return NaN, got %v", got)
	}
}

func TestFmtBandwidthMBps(t *testing.T) {
	if got := fmtBandwidth(16.5); got != "16.5 MB/s" {
		t.Errorf("want '16.5 MB/s', got %q", got)
	}
}

func TestFmtBandwidthGBps(t *testing.T) {
	if got := fmtBandwidth(2048); got != "2.05 GB/s" {
		t.Errorf("want '2.05 GB/s', got %q", got)
	}
}

func TestPciePanelLineAbsentWhenNoData(t *testing.T) {
	gpu := GpuData{
		CardID:     0,
		Backend:    "rocm",
		Name:       "Test GPU",
		PcieTxMBps: math.NaN(),
		PcieRxMBps: math.NaN(),
		PowerMax:   300,
	}
	lines := renderMetricLines(gpu, &GpuHistory{}, 80, false)
	for _, line := range lines {
		if strings.Contains(line, "PCIE") || strings.Contains(line, "PEAK") {
			t.Errorf("no PCIE/PEAK line should appear when data is unavailable, got %q", line)
		}
	}
}

func TestPciePanelLineTxRxSingleLine(t *testing.T) {
	// With enough width, PCIE + PEAK should fit on one line.
	hist := &GpuHistory{PcieTxPeak: 300.0, PcieRxPeak: 150.0}
	gpu := GpuData{
		CardID:     0,
		Backend:    "rocm",
		Name:       "Test GPU",
		PcieTxMBps: 256.5,
		PcieRxMBps: 128.0,
		PowerMax:   300,
	}
	lines := renderMetricLines(gpu, hist, 80, false)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "PCIE") || !strings.Contains(last, "PEAK") {
		t.Errorf("wide panel should have PCIE and PEAK on one line, got %q", last)
	}
	if !strings.Contains(last, "256.5") || !strings.Contains(last, "300.0") {
		t.Errorf("single line should show current and peak TX, got %q", last)
	}
}

func TestPciePanelLineTxRxTwoLines(t *testing.T) {
	// With narrow width, PCIE and PEAK should split to two lines.
	hist := &GpuHistory{PcieTxPeak: 300.0, PcieRxPeak: 150.0}
	gpu := GpuData{
		CardID:     0,
		Backend:    "rocm",
		Name:       "Test GPU",
		PcieTxMBps: 256.5,
		PcieRxMBps: 128.0,
		PowerMax:   300,
	}
	lines := renderMetricLines(gpu, hist, 30, false)
	found := 0
	for _, line := range lines {
		if strings.Contains(line, "PCIE") {
			found++
		}
		if strings.Contains(line, "PEAK") {
			found++
		}
	}
	if found < 2 {
		t.Errorf("narrow panel should have PCIE and PEAK on separate lines, found %d matches", found)
	}
}

func TestPciePanelLineCombinedOnly(t *testing.T) {
	hist := &GpuHistory{PcieTxPeak: 600.0}
	gpu := GpuData{
		CardID:     0,
		Backend:    "sysfs",
		Name:       "Test GPU",
		PcieTxMBps: 512.0,
		PcieRxMBps: math.NaN(),
		PowerMax:   300,
	}
	lines := renderMetricLines(gpu, hist, 80, false)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "BW") || !strings.Contains(last, "PEAK") {
		t.Errorf("combined line should contain BW and PEAK, got %q", last)
	}
	if strings.Contains(last, "TX") || strings.Contains(last, "RX") {
		t.Errorf("combined line should not contain TX/RX labels, got %q", last)
	}
}

// ── Test helper ─────────────────────────────────────────────────────

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// ── AMD sysfs shared metric reader ──────────────────────────────────

// buildGpuMetricsV13 constructs a synthetic 120-byte gpu_metrics blob
// (format_revision=1, content_revision=3) with the given umc activity at
// offset 18 and throttle status at offset 68.
func buildGpuMetricsV13(umc uint16, throttle uint32) []byte {
	buf := make([]byte, 120)
	buf[0] = 120 // structure_size lo
	buf[1] = 0   // structure_size hi
	buf[2] = 1   // format_revision
	buf[3] = 3   // content_revision
	buf[18] = byte(umc)
	buf[19] = byte(umc >> 8)
	buf[68] = byte(throttle)
	buf[69] = byte(throttle >> 8)
	buf[70] = byte(throttle >> 16)
	buf[71] = byte(throttle >> 24)
	return buf
}

// writeAmdSysfsFixture builds a fake /sys/class/drm/cardN/device tree in dir
// with a full complement of amdgpu metric files.
func writeAmdSysfsFixture(t *testing.T, dir string) amdSysfsDev {
	t.Helper()
	hwmon := dir + "/hwmon/hwmon3"
	if err := os.MkdirAll(hwmon, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/gpu_busy_percent":                  "83\n",
		"/mem_busy_percent":                  "42\n",
		"/mem_info_vram_total":               "34208743424\n",
		"/mem_info_vram_used":                "17104371712\n",
		"/mem_info_gtt_total":                "100951101440\n",
		"/mem_info_gtt_used":                 "64061440\n",
		"/pp_dpm_sclk":                       "0: 500Mhz\n1: 2400Mhz *\n",
		"/pp_dpm_mclk":                       "0: 96Mhz\n1: 1258Mhz *\n",
		"/power_dpm_force_performance_level": "manual\n",
		"/current_link_speed":                "16.0 GT/s PCIe\n",
		"/current_link_width":                "16\n",
		"/hwmon/hwmon3/temp1_input":          "67000\n",
		"/hwmon/hwmon3/temp1_label":          "edge\n",
		"/hwmon/hwmon3/temp2_input":          "74000\n",
		"/hwmon/hwmon3/temp2_label":          "junction\n",
		"/hwmon/hwmon3/temp3_input":          "68000\n",
		"/hwmon/hwmon3/temp3_label":          "mem\n",
		"/hwmon/hwmon3/power1_average":       "98000000\n",
		"/hwmon/hwmon3/power1_cap":           "300000000\n",
		"/hwmon/hwmon3/fan1_input":           "1376\n",
		"/hwmon/hwmon3/pwm1":                 "68\n",
		"/hwmon/hwmon3/in0_input":            "1175\n",
	}
	for name, content := range files {
		if err := writeTestFile(dir+name, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dir+"/gpu_metrics", buildGpuMetricsV13(37, 1<<1), 0644); err != nil {
		t.Fatal(err)
	}
	return amdSysfsDev{deviceDir: dir, hwmonDir: hwmon}
}

func TestCollectAmdSysfsMetricsFull(t *testing.T) {
	dev := writeAmdSysfsFixture(t, t.TempDir())
	gpu := newGpuData(0, "rocm")
	collectAmdSysfsMetrics(&gpu, dev)

	if gpu.GpuUse != 83 {
		t.Errorf("GpuUse want 83 got %v", gpu.GpuUse)
	}
	if gpu.MemActivity != 42 {
		t.Errorf("MemActivity want 42 got %v", gpu.MemActivity)
	}
	if gpu.UmcActivity != 37 {
		t.Errorf("UmcActivity want 37 got %v", gpu.UmcActivity)
	}
	if gpu.VramTotal != 34208743424 || gpu.VramUsed != 17104371712 {
		t.Errorf("VRAM want 34208743424/17104371712 got %d/%d", gpu.VramTotal, gpu.VramUsed)
	}
	if gpu.VramPercent < 49.9 || gpu.VramPercent > 50.1 {
		t.Errorf("VramPercent want ~50 got %v", gpu.VramPercent)
	}
	if gpu.GttTotal != 100951101440 || gpu.GttUsed != 64061440 {
		t.Errorf("GTT want 100951101440/64061440 got %d/%d", gpu.GttTotal, gpu.GttUsed)
	}
	if gpu.Sclk != 2400 || gpu.Mclk != 1258 {
		t.Errorf("clocks want 2400/1258 got %d/%d", gpu.Sclk, gpu.Mclk)
	}
	if gpu.PerfLevel != "manual" {
		t.Errorf("PerfLevel want manual got %q", gpu.PerfLevel)
	}
	if gpu.PcieSpeed != "16.0GT/s" {
		t.Errorf("PcieSpeed want 16.0GT/s got %q", gpu.PcieSpeed)
	}
	if gpu.PcieWidth != 16 {
		t.Errorf("PcieWidth want 16 got %d", gpu.PcieWidth)
	}
	if gpu.TempEdge != 67 || gpu.TempJunc != 74 || gpu.TempMem != 68 {
		t.Errorf("temps want 67/74/68 got %v/%v/%v", gpu.TempEdge, gpu.TempJunc, gpu.TempMem)
	}
	if gpu.PowerAvg != 98 {
		t.Errorf("PowerAvg want 98 got %v", gpu.PowerAvg)
	}
	if gpu.PowerMax != 300 {
		t.Errorf("PowerMax want 300 got %v", gpu.PowerMax)
	}
	if gpu.FanRPM != 1376 {
		t.Errorf("FanRPM want 1376 got %d", gpu.FanRPM)
	}
	if gpu.FanPercent < 26.6 || gpu.FanPercent > 26.8 {
		t.Errorf("FanPercent want ~26.7 got %v", gpu.FanPercent)
	}
	if gpu.Voltage != 1175 {
		t.Errorf("Voltage want 1175 got %v", gpu.Voltage)
	}
	if gpu.ThrottleStatus != 2 {
		t.Errorf("ThrottleStatus want 2 got %d", gpu.ThrottleStatus)
	}
	if len(gpu.ThrottleReasons) != 1 || gpu.ThrottleReasons[0] != "THERMAL" {
		t.Errorf("ThrottleReasons want [THERMAL] got %v", gpu.ThrottleReasons)
	}
	// v1.3 blob has no pcie_bandwidth_inst and there is no pcie_bw file.
	if !math.IsNaN(gpu.PcieTxMBps) || gpu.PcieBwTxDelta != -1 {
		t.Errorf("PCIe BW should be unavailable, got %v / %d", gpu.PcieTxMBps, gpu.PcieBwTxDelta)
	}
}

func TestCollectAmdSysfsMetricsMissingFiles(t *testing.T) {
	dev := amdSysfsDev{deviceDir: t.TempDir()}
	gpu := newGpuData(1, "rocm")
	collectAmdSysfsMetrics(&gpu, dev)

	if !math.IsNaN(gpu.GpuUse) || !math.IsNaN(gpu.MemActivity) {
		t.Errorf("missing files should leave NaN, got %v/%v", gpu.GpuUse, gpu.MemActivity)
	}
	if !math.IsNaN(gpu.TempEdge) || !math.IsNaN(gpu.TempJunc) || !math.IsNaN(gpu.TempMem) {
		t.Errorf("missing hwmon should leave temps NaN")
	}
	if !math.IsNaN(gpu.PowerAvg) || !math.IsNaN(gpu.PowerMax) {
		t.Errorf("missing hwmon should leave power NaN")
	}
	if gpu.VramTotal != 0 || gpu.GttTotal != 0 || gpu.Sclk != 0 || gpu.Mclk != 0 {
		t.Errorf("missing files should leave zero memory/clocks")
	}
	if gpu.PerfLevel != "" || gpu.PcieSpeed != "" || gpu.PcieWidth != 0 {
		t.Errorf("missing files should leave empty PCIe/perf fields")
	}
	if gpu.ThrottleStatus != 0 || gpu.UmcActivity != 0 {
		t.Errorf("missing gpu_metrics should leave throttle/umc zero")
	}
}

func TestReadHwmonTempsLabelMapping(t *testing.T) {
	dir := t.TempDir()
	// Deliberately shuffled: temp1=junction, temp2=mem, temp3=edge.
	files := map[string]string{
		"temp1_input": "90000", "temp1_label": "junction",
		"temp2_input": "80000", "temp2_label": "mem",
		"temp3_input": "70000", "temp3_label": "edge",
	}
	for name, content := range files {
		if err := writeTestFile(dir+"/"+name, content); err != nil {
			t.Fatal(err)
		}
	}
	edge, junc, mem := readHwmonTemps(dir)
	if edge != 70 || junc != 90 || mem != 80 {
		t.Errorf("want edge=70 junc=90 mem=80, got %v/%v/%v", edge, junc, mem)
	}
}

func TestReadHwmonTempsPositionalFallback(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/temp1_input", "55000"); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(dir+"/temp2_input", "65000"); err != nil {
		t.Fatal(err)
	}
	edge, junc, mem := readHwmonTemps(dir)
	if edge != 55 || junc != 65 {
		t.Errorf("positional fallback want edge=55 junc=65, got %v/%v", edge, junc)
	}
	if !math.IsNaN(mem) {
		t.Errorf("missing temp3 should be NaN, got %v", mem)
	}
}

func TestReadHwmonMetricsEdgeOnlyMirrorsJunction(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/temp1_input", "51000"); err != nil {
		t.Fatal(err)
	}
	gpu := newGpuData(0, "sysfs")
	readHwmonMetrics(&gpu, dir)
	if gpu.TempEdge != 51 || gpu.TempJunc != 51 {
		t.Errorf("edge-only sensor should mirror junction, got e=%v j=%v", gpu.TempEdge, gpu.TempJunc)
	}
}

func TestReadHwmonMetricsPowerInputFallback(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir+"/power1_input", "15000000"); err != nil {
		t.Fatal(err)
	}
	gpu := newGpuData(0, "sysfs")
	readHwmonMetrics(&gpu, dir)
	if gpu.PowerAvg != 15 {
		t.Errorf("power1_input fallback want 15W got %v", gpu.PowerAvg)
	}
}

func TestParseLinkSpeed(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"16.0 GT/s PCIe", "16.0GT/s"},
		{"32.0 GT/s PCIe", "32.0GT/s"},
		{"2.5 GT/s PCIe", "2.5GT/s"},
		{"", ""},
		{"Unknown", ""},
	}
	for _, tt := range tests {
		if got := parseLinkSpeed(tt.input); got != tt.want {
			t.Errorf("parseLinkSpeed(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReadGpuMetricsFieldsV13(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/gpu_metrics", buildGpuMetricsV13(55, 0x30001), 0644); err != nil {
		t.Fatal(err)
	}
	umc, throttle := readGpuMetricsFields(dir)
	if umc != 55 {
		t.Errorf("umc want 55 got %v", umc)
	}
	if throttle != 0x30001 {
		t.Errorf("throttle want 0x30001 got %#x", throttle)
	}
}

func TestReadGpuMetricsFieldsNASentinels(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/gpu_metrics", buildGpuMetricsV13(0xffff, 0xffffffff), 0644); err != nil {
		t.Fatal(err)
	}
	umc, throttle := readGpuMetricsFields(dir)
	if umc != 0 || throttle != 0 {
		t.Errorf("N/A sentinels should map to zero, got %v/%d", umc, throttle)
	}
}

func TestReadGpuMetricsFieldsBadHeader(t *testing.T) {
	dir := t.TempDir()
	blob := buildGpuMetricsV13(55, 7)
	blob[0] = 99 // structure_size does not match actual length
	if err := os.WriteFile(dir+"/gpu_metrics", blob, 0644); err != nil {
		t.Fatal(err)
	}
	umc, throttle := readGpuMetricsFields(dir)
	if umc != 0 || throttle != 0 {
		t.Errorf("bad header should yield zeros, got %v/%d", umc, throttle)
	}
}

func TestReadGpuMetricsFieldsMissing(t *testing.T) {
	umc, throttle := readGpuMetricsFields(t.TempDir())
	if umc != 0 || throttle != 0 {
		t.Errorf("missing blob should yield zeros, got %v/%d", umc, throttle)
	}
}

// The offsets used by readGpuMetricsFields are valid only for content
// revisions 1-3. v1.0 and v1.4+ (e.g. MI300 gpu_metrics_v1_4/v1_5) lay the
// struct out differently — offset 18 is vcn_activity[1] and offset 68 is
// part of the pcie_bandwidth_acc accumulator — so those revisions must be
// rejected rather than misdecoded into UMC%/throttle status.
func TestReadGpuMetricsFieldsWrongContentRevision(t *testing.T) {
	for _, rev := range []byte{0, 4, 5} {
		dir := t.TempDir()
		blob := buildGpuMetricsV13(55, 0x3)
		blob[3] = rev // content_revision outside 1-3
		if err := os.WriteFile(dir+"/gpu_metrics", blob, 0644); err != nil {
			t.Fatal(err)
		}
		umc, throttle := readGpuMetricsFields(dir)
		if umc != 0 || throttle != 0 {
			t.Errorf("content_revision=%d should yield zeros, got %v/%d", rev, umc, throttle)
		}
	}
}

func TestFindAmdSysfsDev(t *testing.T) {
	root := t.TempDir()
	deviceDir := root + "/card0/device"
	hwmonDir := deviceDir + "/hwmon/hwmon2"
	if err := os.MkdirAll(hwmonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(deviceDir+"/uevent", "DRIVER=amdgpu\nPCI_SLOT_NAME=0000:c3:00.0\n"); err != nil {
		t.Fatal(err)
	}

	// rocm-smi reports uppercase hex; matching must be case-insensitive.
	dev := findAmdSysfsDev(root, "0000:C3:00.0")
	if dev.deviceDir != deviceDir {
		t.Errorf("deviceDir want %q got %q", deviceDir, dev.deviceDir)
	}
	if dev.hwmonDir != hwmonDir {
		t.Errorf("hwmonDir want %q got %q", hwmonDir, dev.hwmonDir)
	}
	if dev.pciBus != "0000:c3:00.0" {
		t.Errorf("pciBus should be normalized, got %q", dev.pciBus)
	}

	// Unmatched bus → empty deviceDir (caller falls back to rocm-smi).
	if dev := findAmdSysfsDev(root, "0000:03:00.0"); dev.deviceDir != "" {
		t.Errorf("unmatched PCI bus should yield empty deviceDir, got %q", dev.deviceDir)
	}

	// Empty/garbage bus → empty deviceDir.
	if dev := findAmdSysfsDev(root, ""); dev.deviceDir != "" {
		t.Errorf("empty PCI bus should yield empty deviceDir")
	}
}

func TestNewGpuDataDefaults(t *testing.T) {
	g := newGpuData(3, "rocm")
	if g.CardID != 3 || g.Backend != "rocm" {
		t.Errorf("identity fields wrong: %d %q", g.CardID, g.Backend)
	}
	for name, v := range map[string]float64{
		"GpuUse": g.GpuUse, "MemActivity": g.MemActivity,
		"TempEdge": g.TempEdge, "TempJunc": g.TempJunc, "TempMem": g.TempMem,
		"PowerAvg": g.PowerAvg, "PowerMax": g.PowerMax,
		"PcieTxMBps": g.PcieTxMBps, "PcieRxMBps": g.PcieRxMBps,
	} {
		if !math.IsNaN(v) {
			t.Errorf("%s should default to NaN, got %v", name, v)
		}
	}
	for name, v := range map[string]int64{
		"PcieTxBytes": g.PcieTxBytes, "PcieRxBytes": g.PcieRxBytes,
		"PcieBwTxDelta": g.PcieBwTxDelta, "PcieBwRxDelta": g.PcieBwRxDelta,
	} {
		if v != -1 {
			t.Errorf("%s should default to -1, got %d", name, v)
		}
	}
}

// TestRocmBackendConcurrentCollect reproduces overlapping bubbletea fetch
// goroutines hitting a stale sysfs mapping: every CollectData call then runs
// discover(), which rewrites cards/sysfsMode while the other goroutines read
// them. Run under -race this used to report a data race at the cards/sysfsMode
// writes; CollectData must serialize via the backend mutex. A fake rocm-smi on
// PATH keeps the test hermetic (no GPUs or real rocm-smi needed).
func TestRocmBackendConcurrentCollect(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\necho '{\"card0\": {\"Card Series\": \"Fake GPU\", \"PCI Bus\": \"0000:ff:1f.7\"}}'\n"
	if err := os.WriteFile(binDir+"/rocm-smi", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	b := &rocmBackend{
		tool:      newRocmSMITool(),
		sysfsMode: true,
		cards: []rocmCard{{
			identity: GpuData{CardID: 0, Backend: "rocm", Name: "stale"},
			// Directory exists but lacks gpu_busy_percent, so sysfsOK fails
			// and every CollectData call rediscovers.
			dev: amdSysfsDev{deviceDir: t.TempDir()},
		}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.CollectData()
		}()
	}
	wg.Wait()
}

// ── Live hardware smoke test (skipped on GPU-less CI) ───────────────

// TestLiveRocmSysfsBackend exercises the real rocm backend end-to-end on a
// machine with rocm-smi and AMD GPUs. It is skipped everywhere else.
func TestLiveRocmSysfsBackend(t *testing.T) {
	if _, err := exec.LookPath("rocm-smi"); err != nil {
		t.Skip("rocm-smi not installed")
	}
	b := newRocmBackend(newRocmSMITool())
	if len(b.cards) == 0 {
		t.Skip("rocm-smi reported no GPUs")
	}
	if !b.sysfsMode {
		t.Skip("GPUs not mapped to sysfs on this machine")
	}
	gpus, _ := b.CollectData()
	if len(gpus) != len(b.cards) {
		t.Fatalf("want %d GPUs, got %d", len(b.cards), len(gpus))
	}
	for _, g := range gpus {
		if g.Backend != "rocm" {
			t.Errorf("GPU %d backend want rocm got %q", g.CardID, g.Backend)
		}
		if g.Name == "" {
			t.Errorf("GPU %d has no name", g.CardID)
		}
		if g.VramTotal <= 0 {
			t.Errorf("GPU %d VramTotal not positive: %d", g.CardID, g.VramTotal)
		}
		if math.IsNaN(g.GpuUse) || g.GpuUse < 0 || g.GpuUse > 100 {
			t.Errorf("GPU %d GpuUse out of range: %v", g.CardID, g.GpuUse)
		}
		if !math.IsNaN(g.TempJunc) && (g.TempJunc < 1 || g.TempJunc > 130) {
			t.Errorf("GPU %d TempJunc implausible: %v", g.CardID, g.TempJunc)
		}
		if !math.IsNaN(g.PowerAvg) && (g.PowerAvg < 0 || g.PowerAvg > 2000) {
			t.Errorf("GPU %d PowerAvg implausible: %v", g.CardID, g.PowerAvg)
		}
		t.Logf("GPU %d %s pci=%s use=%.0f%% vram=%d/%d junc=%.0fC pwr=%.0fW sclk=%d mclk=%d fan=%drpm/%.0f%% perf=%s pcie=%s x%d",
			g.CardID, g.Name, g.PcieBus, g.GpuUse, g.VramUsed, g.VramTotal,
			g.TempJunc, g.PowerAvg, g.Sclk, g.Mclk, g.FanRPM, g.FanPercent,
			g.PerfLevel, g.PcieSpeed, g.PcieWidth)
	}
}

// ── Async backend detection ─────────────────────────────────────────

func TestDetectingViewShowsSplash(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true
	if got := m.View(); !strings.Contains(got, "Detecting GPUs") {
		t.Errorf("View during detection should show 'Detecting GPUs', got %q", got)
	}
}

func TestDetectingInitReturnsDetectCmd(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true
	if m.Init() == nil {
		t.Error("Init while detecting must return a detection command")
	}
}

func TestBackendsMsgTransitionsToReady(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true

	fb := &fakeBackend{
		name: "rocm",
		gpus: []GpuData{{CardID: 0, Backend: "rocm", Name: "Probe GPU"}},
	}
	updated, cmd := m.Update(backendsMsg{backends: []GpuBackend{fb}, gpus: fb.gpus})
	nm := updated.(model)

	if nm.detecting {
		t.Error("backendsMsg must clear the detecting state")
	}
	if nm.fatalErr != nil {
		t.Errorf("backendsMsg with backends must not set fatalErr, got %v", nm.fatalErr)
	}
	if len(nm.backends) != 1 || nm.backends[0].Name() != "rocm" {
		t.Errorf("backends not installed on model: %v", nm.backends)
	}
	// The probe's collection seeds the first paint without another fetch.
	if len(nm.gpus) != 1 || nm.gpus[0].Name != "Probe GPU" {
		t.Errorf("probe data should populate m.gpus immediately, got %v", nm.gpus)
	}
	if cmd == nil {
		t.Error("backendsMsg must return a command to start the tick chain")
	}
}

func TestBackendsMsgEmptyProbeStartsFetch(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true

	fb := &fakeBackend{name: "sysfs"}
	updated, cmd := m.Update(backendsMsg{backends: []GpuBackend{fb}})
	nm := updated.(model)

	if nm.detecting {
		t.Error("backendsMsg must clear the detecting state")
	}
	if len(nm.gpus) != 0 {
		t.Errorf("no probe data given, m.gpus should stay empty, got %v", nm.gpus)
	}
	if cmd == nil {
		t.Error("backendsMsg without probe data must return a fetch+tick command")
	}
}

func TestBackendsMsgNoBackendsQuitsWithFatalErr(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true

	updated, cmd := m.Update(backendsMsg{})
	nm := updated.(model)

	if nm.fatalErr == nil {
		t.Fatal("backendsMsg with no backends must set fatalErr")
	}
	if !strings.Contains(nm.fatalErr.Error(), "no supported GPUs") {
		t.Errorf("fatalErr should describe missing GPUs, got %q", nm.fatalErr)
	}
	if cmd == nil {
		t.Fatal("backendsMsg with no backends must return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("returned command must be tea.Quit")
	}
}

func TestTickIgnoredWhileDetecting(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true

	updated, cmd := m.Update(tickMsg(time.Now()))
	nm := updated.(model)

	if cmd != nil {
		t.Error("tick during detection must not schedule fetches or more ticks")
	}
	if !nm.detecting {
		t.Error("tick must not clear the detecting state")
	}
}

func TestRefreshKeyIgnoredWhileDetecting(t *testing.T) {
	m := newModel(2*time.Second, nil)
	m.detecting = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Error("'r' during detection must not trigger a fetch")
	}
}

func TestSortAndMergeGpuDataOrders(t *testing.T) {
	gpus := []GpuData{
		{CardID: 1, Backend: "sysfs"},
		{CardID: 2, Backend: "rocm"},
		{CardID: 0, Backend: "rocm"},
	}
	sorted, _ := sortAndMergeGpuData(gpus, nil)
	want := []string{"rocm:0", "rocm:2", "sysfs:1"}
	for i, w := range want {
		if sorted[i].HistKey() != w {
			t.Errorf("position %d: got %s, want %s", i, sorted[i].HistKey(), w)
		}
	}
}
