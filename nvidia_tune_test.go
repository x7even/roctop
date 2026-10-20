package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// nvLine builds a 19-field nvidia-smi metric row. Only the fields a test
// cares about need naming; the rest are plausible filler.
func nvLine(powerLimit, powerMaxLimit string) []string {
	return []string{
		"0", "NVIDIA RTX 6000 Ada", "61", "44", "17",
		"49140", "12288", "180.25", powerLimit, powerMaxLimit, "38",
		"1965", "10001", "4", "16",
		"580.65.06", "98.02.81.00.01", "P2", "00000000:C3:00.0",
	}
}

// ── Static-limit query ────────────────────────────────────────────────

func TestParseNvidiaStatic(t *testing.T) {
	t.Parallel()
	// Two cards, and deliberately not GPU 0 and 1: the join is by bus id,
	// so a gap in the index must not matter.
	out := strings.Join([]string{
		"00000000:17:00.0, 300.00, 100.00, 2520, 10501",
		"00000000:C3:00.0, 450.00, 150.00, 2850, 10501",
	}, "\n")

	limits := parseNvidiaStatic(out)
	if len(limits) != 2 {
		t.Fatalf("parsed %d rows, want 2: %+v", len(limits), limits)
	}
	got, ok := limits["0000:c3:00.0"]
	if !ok {
		t.Fatalf("bus 0000:c3:00.0 absent; keys = %+v", limits)
	}
	if got.capDefault != 450 || got.capMin != 150 {
		t.Errorf("caps = %v/%v, want 450/150", got.capDefault, got.capMin)
	}
	if got.sclkMax != 2850 || got.mclkMax != 10501 {
		t.Errorf("clocks = %d/%d, want 2850/10501", got.sclkMax, got.mclkMax)
	}
}

// TestParseNvidiaStaticUnsupported covers the two spellings nvidia-smi uses
// for a field a card does not implement. Neither may become a zero that the
// renderer would show as a real limit.
func TestParseNvidiaStaticUnsupported(t *testing.T) {
	t.Parallel()
	out := "00000000:01:00.0, [N/A], [Not Supported], [N/A], [Not Supported]"

	limits := parseNvidiaStatic(out)
	got, ok := limits["0000:01:00.0"]
	if !ok {
		t.Fatalf("row dropped; keys = %+v", limits)
	}
	if !math.IsNaN(got.capDefault) || !math.IsNaN(got.capMin) {
		t.Errorf("caps = %v/%v, want NaN/NaN", got.capDefault, got.capMin)
	}
	if got.sclkMax != 0 || got.mclkMax != 0 {
		t.Errorf("clocks = %d/%d, want 0/0", got.sclkMax, got.mclkMax)
	}
}

func TestParseNvidiaStaticRejectsUnusableRows(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]string{
		"empty":       "",
		"short row":   "00000000:01:00.0, 300.00",
		"no bus id":   "not-a-bus, 300.00, 100.00, 2520, 10501",
		"header only": "pci.bus_id, power.default_limit",
	} {
		if limits := parseNvidiaStatic(out); limits != nil {
			t.Errorf("%s: parseNvidiaStatic = %+v, want nil", name, limits)
		}
	}
}

func TestApplyNvidiaStatic(t *testing.T) {
	t.Parallel()
	gpus := []GpuData{
		parseNvidiaGPULine(nvLine("350.00", "450.00")),
		parseNvidiaGPULine(nvLine("350.00", "450.00")),
	}
	gpus[1].CardID = 2
	gpus[1].PcieBus = "00000000:17:00.0"

	applyNvidiaStatic(gpus, map[string]nvStatic{
		"0000:c3:00.0": {capDefault: 450, capMin: 150, sclkMax: 2850, mclkMax: 10501},
	})

	if gpus[0].PowerCapDefault != 450 || gpus[0].PowerCapMin != 150 {
		t.Errorf("joined caps = %v/%v, want 450/150", gpus[0].PowerCapDefault, gpus[0].PowerCapMin)
	}
	if gpus[0].SclkMax != 2850 || gpus[0].MclkMax != 10501 {
		t.Errorf("joined clocks = %d/%d, want 2850/10501", gpus[0].SclkMax, gpus[0].MclkMax)
	}
	// A card whose bus is not in the map keeps its unavailable sentinels
	// rather than inheriting another card's limits.
	if !math.IsNaN(gpus[1].PowerCapDefault) || !math.IsNaN(gpus[1].PowerCapMin) {
		t.Errorf("unmatched gpu caps = %v/%v, want NaN/NaN",
			gpus[1].PowerCapDefault, gpus[1].PowerCapMin)
	}
	if gpus[1].SclkMax != 0 || gpus[1].MclkMax != 0 {
		t.Errorf("unmatched gpu clocks = %d/%d, want 0/0", gpus[1].SclkMax, gpus[1].MclkMax)
	}

	// A nil or empty map must leave every card alone, not panic.
	applyNvidiaStatic(gpus, nil)
	applyNvidiaStatic(nil, map[string]nvStatic{"0000:c3:00.0": {}})
}

// ── Cache and retry ───────────────────────────────────────────────────

func TestStaticLimitsCachesOneQuery(t *testing.T) {
	t.Parallel()
	calls := 0
	b := &nvidiaBackend{queryFn: func(args ...string) string {
		calls++
		return "00000000:C3:00.0, 450.00, 150.00, 2850, 10501"
	}}

	for i := 0; i < 5; i++ {
		if got := b.staticLimits(); len(got) != 1 {
			t.Fatalf("call %d returned %d rows, want 1", i, len(got))
		}
	}
	if calls != 1 {
		t.Errorf("ran nvidia-smi %d times, want 1 (limits do not change)", calls)
	}
}

// TestStaticLimitsRetriesThenGivesUp: a transient failure must not blank the
// readout for the life of the process, but a driver that rejects one of the
// field names must not cost a subprocess per refresh either.
func TestStaticLimitsRetriesThenGivesUp(t *testing.T) {
	t.Parallel()
	calls := 0
	b := &nvidiaBackend{queryFn: func(args ...string) string {
		calls++
		return "" // nvidia-smi failed or printed nothing
	}}

	for i := 0; i < 10; i++ {
		b.staticLimits()
	}
	if calls != nvidiaStaticAttempts {
		t.Errorf("ran nvidia-smi %d times, want %d", calls, nvidiaStaticAttempts)
	}

	// Recovery before the cap is reached.
	calls = 0
	ok := &nvidiaBackend{queryFn: func(args ...string) string {
		calls++
		if calls == 1 {
			return ""
		}
		return "00000000:C3:00.0, 450.00, 150.00, 2850, 10501"
	}}
	if got := ok.staticLimits(); got != nil {
		t.Errorf("first (failed) call returned %+v, want nil", got)
	}
	if got := ok.staticLimits(); len(got) != 1 {
		t.Errorf("second call returned %d rows, want 1", len(got))
	}
}

// ── Power sentinel ────────────────────────────────────────────────────

// TestNvidiaPowerSentinel pins the laptop/vGPU case: power.limit reads
// UINT32_MAX mW. Sanitising before the fallback is what keeps the power bar
// scaled to the real limit.
func TestNvidiaPowerSentinel(t *testing.T) {
	t.Parallel()
	gpu := parseNvidiaGPULine(nvLine("4294967.50", "450.00"))
	if gpu.PowerMax != 450 {
		t.Errorf("PowerMax = %v, want the 450W max_limit fallback", gpu.PowerMax)
	}
	if gpu.PowerCapMax != 450 {
		t.Errorf("PowerCapMax = %v, want 450", gpu.PowerCapMax)
	}

	// Both fields unusable: unknown, not a megawatt and not a zero.
	none := parseNvidiaGPULine(nvLine("[N/A]", "[N/A]"))
	if !math.IsNaN(none.PowerMax) || !math.IsNaN(none.PowerCapMax) {
		t.Errorf("PowerMax/PowerCapMax = %v/%v, want NaN/NaN", none.PowerMax, none.PowerCapMax)
	}

	normal := parseNvidiaGPULine(nvLine("350.00", "450.00"))
	if normal.PowerMax != 350 || normal.PowerCapMax != 450 {
		t.Errorf("PowerMax/PowerCapMax = %v/%v, want 350/450", normal.PowerMax, normal.PowerCapMax)
	}
}

func TestSanePowerW(t *testing.T) {
	t.Parallel()
	for _, bad := range []float64{math.NaN(), 0, -1, 4294967.50, 10001} {
		if got := sanePowerW(bad); !math.IsNaN(got) {
			t.Errorf("sanePowerW(%v) = %v, want NaN", bad, got)
		}
	}
	for _, ok := range []float64{1, 350, 10000} {
		if got := sanePowerW(ok); got != ok {
			t.Errorf("sanePowerW(%v) = %v, want it unchanged", ok, got)
		}
	}
}

// ── Rendering an NVIDIA card ──────────────────────────────────────────

// nvTuned returns a card as the backend builds one: a 350 W cap against a
// 450 W board default, clock ceilings, and no voltage or OverDrive data.
func nvTuned() GpuData {
	gpu := parseNvidiaGPULine(nvLine("350.00", "450.00"))
	gpu.PowerCapDefault = 450
	gpu.PowerCapMin = 150
	gpu.SclkMax = 2850
	gpu.MclkMax = 10501
	return gpu
}

func TestRenderTuneLineNvidiaCap(t *testing.T) {
	t.Parallel()
	line := plainText(renderTuneLine(nvTuned(), 100, 5))
	for _, want := range []string{"TUNE", "CAP", "350W", "of 450W"} {
		if !strings.Contains(line, want) {
			t.Errorf("NVIDIA TUNE line missing %q: %q", want, line)
		}
	}
	// nvidia-smi exposes neither core voltage nor clock offsets, so those
	// segments must stay absent rather than render a zero.
	for _, unwanted := range []string{"VDD", "CLK±", "MEM≤"} {
		if strings.Contains(line, unwanted) {
			t.Errorf("NVIDIA TUNE line shows %q, which nvidia-smi cannot supply: %q", unwanted, line)
		}
	}

	// A card running at its board default has nothing to report.
	stock := nvTuned()
	stock.PowerMax = 450
	if l := renderTuneLine(stock, 100, 5); l != "" {
		t.Errorf("uncapped NVIDIA card produced a TUNE row: %q", l)
	}
}

// TestNvidiaPartialLimits is the state no AMD path can reach: the per-tick
// max_limit is known while the cached default and min are not, because the
// static query failed. Nothing may render as NaN or as a half-open range.
func TestNvidiaPartialLimits(t *testing.T) {
	t.Parallel()
	gpu := parseNvidiaGPULine(nvLine("350.00", "450.00"))
	gpu.SclkMax = 2850 // clock ceilings can also arrive half-known

	info := plainText(strings.Join(renderInfoLines(gpu, 100, false), "\n"))
	if strings.Contains(info, "NaN") {
		t.Errorf("info view printed NaN:\n%s", info)
	}
	if strings.Contains(info, "-450W") {
		t.Errorf("info view printed a half-open cap range:\n%s", info)
	}
	// Only one ceiling known: fmtClkMax renders it alone, in GHz, rather
	// than pairing it with a zero.
	if !strings.Contains(info, "2.85GHz") {
		t.Errorf("info view lost the known core-clock ceiling:\n%s", info)
	}
	if strings.Contains(info, "2850/0") || strings.Contains(info, "/0MHz") {
		t.Errorf("info view paired the known ceiling with a zero:\n%s", info)
	}
	// No voltage sensor on NVIDIA: the row must read N/A, not 0mV.
	if strings.Contains(info, "0mV") {
		t.Errorf("info view claims a voltage reading for an NVIDIA card:\n%s", info)
	}

	text := renderSnapshotText([]GpuData{gpu}, nil, []string{"nvidia"})
	if strings.Contains(text, "NaN") {
		t.Errorf("snapshot text printed NaN:\n%s", text)
	}

	raw, err := buildSnapshotJSON([]GpuData{gpu}, nil, []string{"nvidia"})
	if err != nil {
		t.Fatalf("buildSnapshotJSON: %v", err)
	}
	var snap struct {
		Gpus []map[string]interface{} `json:"gpus"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	g := snap.Gpus[0]
	if v, ok := g["power_cap_max_watts"].(float64); !ok || v != 450 {
		t.Errorf("power_cap_max_watts = %v, want 450", g["power_cap_max_watts"])
	}
	for _, key := range []string{"power_cap_default_watts", "power_cap_min_watts"} {
		if v, present := g[key]; !present || v != nil {
			t.Errorf("%s = %v, want null", key, v)
		}
	}
	if _, present := g["overdrive"]; present {
		t.Errorf("NVIDIA card carries an overdrive object: %v", g["overdrive"])
	}
	if v, present := g["mclk_max_mhz"]; present {
		t.Errorf("mclk_max_mhz = %v, want the key omitted", v)
	}
}

// TestRenderMetricLinesNvidiaNoNaN sweeps the panel NVIDIA users actually
// see. Voltage is NaN on every NVIDIA card and PowerCapMax is now filled on
// every tick, so an ungated formatter anywhere in the panel would print NaN
// on exactly the hardware that cannot be tested here.
func TestRenderMetricLinesNvidiaNoNaN(t *testing.T) {
	t.Parallel()
	cards := map[string]GpuData{
		"limits known":     nvTuned(),
		"limits absent":    parseNvidiaGPULine(nvLine("350.00", "450.00")),
		"no limits at all": parseNvidiaGPULine(nvLine("[N/A]", "[N/A]")),
	}
	for name, gpu := range cards {
		for cw := 4; cw <= 120; cw++ {
			out := plainText(strings.Join(
				renderMetricLines(gpu, &GpuHistory{}, cw, false), "\n"))
			if strings.Contains(out, "NaN") {
				t.Fatalf("%s at cw=%d printed NaN:\n%s", name, cw, out)
			}
			if strings.Contains(out, "0mV") {
				t.Fatalf("%s at cw=%d claims a voltage reading:\n%s", name, cw, out)
			}
		}
		info := plainText(strings.Join(renderInfoLines(gpu, 100, false), "\n"))
		if strings.Contains(info, "NaN") {
			t.Errorf("%s info view printed NaN:\n%s", name, info)
		}
	}
}

// TestSnapshotNvidiaFullLimits checks the shipped shape when every field is
// available, including that the clock ceilings reach the snapshot.
func TestSnapshotNvidiaFullLimits(t *testing.T) {
	t.Parallel()
	raw, err := buildSnapshotJSON([]GpuData{nvTuned()}, nil, []string{"nvidia"})
	if err != nil {
		t.Fatalf("buildSnapshotJSON: %v", err)
	}
	var snap struct {
		Gpus []map[string]interface{} `json:"gpus"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for key, want := range map[string]float64{
		"power_cap_watts":         350,
		"power_cap_default_watts": 450,
		"power_cap_min_watts":     150,
		"power_cap_max_watts":     450,
		"sclk_max_mhz":            2850,
		"mclk_max_mhz":            10501,
	} {
		v, ok := snap.Gpus[0][key].(float64)
		if !ok || v != want {
			t.Errorf("%s = %v, want %v", key, snap.Gpus[0][key], want)
		}
	}
	// No voltage key: NVIDIA cannot report one, and 0 mV is not a reading.
	if v, present := snap.Gpus[0]["voltage_mv"]; present {
		t.Errorf("voltage_mv = %v, want the key omitted for an NVIDIA card", v)
	}
}
