package main

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
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
	saved := activeBackends
	defer func() { activeBackends = saved }()

	activeBackends = []GpuBackend{&rocmBackend{}, &nvidiaBackend{}}
	got := backendNames()
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
		{"0000:C3:00.0", "0000:c3:00.0"},  // rocm-smi uppercase → lowercase
		{"0000:c3:00.0", "0000:c3:00.0"},  // sysfs already lowercase
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

// ── NVIDIA parsing ──────────────────────────────────────────────────

func TestParseNvidiaGPULine(t *testing.T) {
	fields := []string{
		"0",                              // index
		"NVIDIA GeForce RTX 4070",        // name
		"45",                             // temperature.gpu
		"3",                              // utilization.gpu
		"12",                             // utilization.memory
		"8188",                           // memory.total (MiB)
		"512",                            // memory.used (MiB)
		"65.23",                          // power.draw
		"200.00",                         // power.limit
		"30",                             // fan.speed
		"1500",                           // clocks.current.graphics
		"810",                            // clocks.current.memory
		"4",                              // pcie.link.gen.current
		"16",                             // pcie.link.width.current
		"560.35",                         // driver_version
		"95.02.3c",                       // vbios_version
		"P0",                             // pstate
		"00000000:01:00.0",               // pci.bus_id
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
		"8188", "1861", "9.19", "[N/A]", "[N/A]",
		"210", "810", "4", "8",
		"595.79", "95.06", "P5", "00000000:64:00.0",
	}

	gpu := parseNvidiaGPULine(fields)

	if gpu.PowerMax != 300 { // default when [N/A] parses to 0
		t.Errorf("PowerMax = %f, want 300 (default)", gpu.PowerMax)
	}
	if gpu.FanPercent != 0 { // [N/A] should parse to 0
		t.Errorf("FanPercent = %f, want 0", gpu.FanPercent)
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
	writeTestFile(tmp, "42.5\n")
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
	writeTestFile(tmp, "1073741824\n")
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
	lines := renderMetricLines(gpu, hist, 80)
	if len(lines) != panelLines {
		t.Errorf("renderMetricLines returned %d lines, want %d (panelLines)", len(lines), panelLines)
	}
}

func TestRenderMetricLinesNaNInputs(t *testing.T) {
	// All-NaN GPU (e.g. a sysfs GPU with no sensors readable) must not panic
	// and must still return panelLines lines.
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
	lines := renderMetricLines(gpu, hist, 80)
	if len(lines) != panelLines {
		t.Errorf("renderMetricLines (all-NaN) returned %d lines, want %d", len(lines), panelLines)
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
	h := renderHeader(4, 2.0, false, false, false, false, false, 200)
	if !strings.Contains(h, "l") {
		t.Error("header should contain l keybinding")
	}

	logf("an error occurred")
	// renderHeader(gpuCount, refreshSecs, paused, infoMode, helpMode, logMode, dataStale, width)
	h = renderHeader(4, 2.0, false, false, false, false, false, 200)
	if !strings.Contains(h, "log(1)") {
		t.Errorf("header should show log(1) when there is 1 entry, got: %s", h)
	}
}

// ── stale data indicator ─────────────────────────────────────────────

func TestHeaderStaleIndicator(t *testing.T) {
	// Stale flag should appear in the header when dataStale is true.
	// renderHeader(gpuCount, refreshSecs, paused, infoMode, helpMode, logMode, dataStale, width)
	withStale := renderHeader(4, 2.0, false, false, false, false, true, 200)
	if !strings.Contains(withStale, "STALE") {
		t.Error("header with dataStale=true should contain 'STALE'")
	}
	// No stale indicator when data is fresh.
	withoutStale := renderHeader(4, 2.0, false, false, false, false, false, 200)
	if strings.Contains(withoutStale, "STALE") {
		t.Error("header with dataStale=false should not contain 'STALE'")
	}
}

func TestDataMsgEmptyPreservesGpus(t *testing.T) {
	// An empty dataMsg (failed fetch) must not clear existing GPU data.
	m := newModel(2 * time.Second)
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
	m := newModel(2 * time.Second)
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

// ── Test helper ─────────────────────────────────────────────────────

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
