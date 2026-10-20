package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestSnapshotJSONOverDrive covers the shape consumers will script against:
// cap context as nullable numbers, OverDrive as an object that is absent
// rather than empty when the driver exposes nothing.
func TestSnapshotJSONOverDrive(t *testing.T) {
	t.Parallel()
	tuned := tuneGpu()
	tuned.SclkMax = 2350
	tuned.MclkMax = 1300

	bare := newGpuData(1, "sysfs")
	bare.Name = "Radeon RX 580"

	raw, err := buildSnapshotJSON([]GpuData{tuned, bare}, nil, []string{"sysfs"})
	if err != nil {
		t.Fatalf("buildSnapshotJSON: %v", err)
	}
	var snap struct {
		Gpus []map[string]interface{} `json:"gpus"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if len(snap.Gpus) != 2 {
		t.Fatalf("got %d gpus", len(snap.Gpus))
	}

	g := snap.Gpus[0]
	for key, want := range map[string]float64{
		"power_cap_watts":         210,
		"power_cap_default_watts": 300,
		"power_cap_min_watts":     210,
		"power_cap_max_watts":     330,
		"sclk_max_mhz":            2350,
		"mclk_max_mhz":            1300,
	} {
		v, ok := g[key].(float64)
		if !ok || v != want {
			t.Errorf("%s = %v, want %v", key, g[key], want)
		}
	}

	od, ok := g["overdrive"].(map[string]interface{})
	if !ok {
		t.Fatalf("overdrive = %v, want an object", g["overdrive"])
	}
	// A zero offset must be present as 0, not omitted: "untouched but
	// tunable" and "not tunable" are different facts.
	if v, present := od["sclk_offset_mhz"]; !present || v.(float64) != 0 {
		t.Errorf("sclk_offset_mhz = %v (present=%v), want 0", v, present)
	}
	if v := od["voltage_offset_mv"]; v == nil || v.(float64) != -20 {
		t.Errorf("voltage_offset_mv = %v, want -20", v)
	}
	if v := od["mclk_max_mhz"]; v == nil || v.(float64) != 1300 {
		t.Errorf("mclk_max_mhz = %v, want 1300", v)
	}
	for key, want := range map[string][]float64{
		"voltage_offset_range_mv": {-200, 0},
		"sclk_offset_range_mhz":   {-500, 1000},
		"mclk_range_mhz":          {97, 1500},
	} {
		arr, ok := od[key].([]interface{})
		if !ok || len(arr) != 2 {
			t.Errorf("%s = %v, want a 2-element array", key, od[key])
			continue
		}
		if arr[0].(float64) != want[0] || arr[1].(float64) != want[1] {
			t.Errorf("%s = %v, want %v", key, arr, want)
		}
	}

	// The card with no OverDrive support: caps null, no overdrive key, and no
	// zero-valued clock ceilings masquerading as data.
	b := snap.Gpus[1]
	for _, key := range []string{
		"power_cap_watts", "power_cap_default_watts",
		"power_cap_min_watts", "power_cap_max_watts",
	} {
		if v, present := b[key]; !present || v != nil {
			t.Errorf("bare gpu %s = %v, want null", key, v)
		}
	}
	for _, key := range []string{"overdrive", "sclk_max_mhz", "mclk_max_mhz"} {
		if v, present := b[key]; present {
			t.Errorf("bare gpu has %s = %v, want the key omitted", key, v)
		}
	}
}

// TestSnapshotJSONPartialOverDrive pins the RDNA3-style card: a voltage
// offset and nothing else must not invent clock fields.
func TestSnapshotJSONPartialOverDrive(t *testing.T) {
	t.Parallel()
	gpu := newGpuData(0, "sysfs")
	gpu.OD = parseOD(odFixture(t, "pp_od_clk_voltage_rdna3.txt"))

	raw, err := buildSnapshotJSON([]GpuData{gpu}, nil, nil)
	if err != nil {
		t.Fatalf("buildSnapshotJSON: %v", err)
	}
	var snap struct {
		Gpus []struct {
			OverDrive map[string]interface{} `json:"overdrive"`
		} `json:"gpus"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	od := snap.Gpus[0].OverDrive
	if od == nil {
		t.Fatal("overdrive absent for a card reporting a voltage offset")
	}
	if v, present := od["voltage_offset_mv"]; !present || v.(float64) != 0 {
		t.Errorf("voltage_offset_mv = %v (present=%v), want 0", v, present)
	}
	for _, key := range []string{
		"sclk_offset_mhz", "sclk_offset_range_mhz", "mclk_min_mhz",
		"mclk_max_mhz", "mclk_range_mhz",
	} {
		if v, present := od[key]; present {
			t.Errorf("overdrive has %s = %v, want it omitted", key, v)
		}
	}
}

func TestSnapshotTextOverDrive(t *testing.T) {
	t.Parallel()
	tuned := tuneGpu()
	tuned.SclkMax = 2350
	tuned.MclkMax = 1300
	tuned.Sclk = 2350
	tuned.Mclk = 1300

	out := renderSnapshotText([]GpuData{tuned}, nil, []string{"sysfs"})
	for _, want := range []string{
		"Power: 15W avg / 210W cap (default 300W, range 210W-330W)",
		"Clocks: sclk 2.35GHz / 2.35GHz max  mclk 1.30GHz / 1.30GHz max",
		"Voltage: 604mV",
		"OverDrive: v offset -20mV",
		"sclk offset +0MHz",
		"mclk 97-1300MHz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot text missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "NaN") {
		t.Errorf("snapshot text printed NaN:\n%s", out)
	}
}

// ── Info view ────────────────────────────────────────────────────────

func TestInfoLinesShowTuningRows(t *testing.T) {
	t.Parallel()
	gpu := tuneGpu()
	gpu.SclkMax = 2350
	gpu.MclkMax = 1300
	gpu.Vendor = "Advanced Micro Devices"
	gpu.PcieBus = "0000:03:00.0"

	out := plainText(strings.Join(renderInfoLines(gpu, 100, false), "\n"))
	for _, want := range []string{
		"Pwr Cap", "210W",
		"Cap Range", "210-330W",
		"Clk Max", "2350/1300MHz",
		"V Offset", "-20mV",
		"V Range", "-200",
		"Clk Off", "+0MHz",
		"Clk Range", "-500",
		"Mem Clk", "97-1300MHz",
		"Mem Range",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info view missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "NaN") {
		t.Errorf("info view printed NaN:\n%s", out)
	}
}

// TestInfoLinesHideOverDriveRowsWhenAbsent: a card without OverDrive must not
// spend four rows on N/A values, and must still show its power cap.
func TestInfoLinesHideOverDriveRowsWhenAbsent(t *testing.T) {
	t.Parallel()
	gpu := newGpuData(0, "sysfs")
	gpu.Name = "Radeon RX 580"
	gpu.PowerMax = 185

	out := plainText(strings.Join(renderInfoLines(gpu, 100, false), "\n"))
	for _, unwanted := range []string{"V Offset", "Clk Off", "Mem Range"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("info view shows %q for a card with no OverDrive table:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "Pwr Cap") || !strings.Contains(out, "185W") {
		t.Errorf("info view lost the power cap:\n%s", out)
	}
	if strings.Contains(out, "NaN") {
		t.Errorf("info view printed NaN:\n%s", out)
	}
}

func TestInfoLinesAllUnknownNoPanic(t *testing.T) {
	t.Parallel()
	gpu := newGpuData(0, "nvidia")
	gpu.Name = "Unknown GPU"
	for cw := 20; cw <= 120; cw += 10 {
		out := plainText(strings.Join(renderInfoLines(gpu, cw, true), "\n"))
		if strings.Contains(out, "NaN") {
			t.Errorf("cw=%d info view printed NaN:\n%s", cw, out)
		}
	}
}

func TestPosToNull(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{math.NaN(), 0, -1} {
		if got := posToNull(v); got != nil {
			t.Errorf("posToNull(%v) = %v, want nil", v, *got)
		}
	}
	if got := posToNull(300); got == nil || *got != 300 {
		t.Errorf("posToNull(300) = %v, want 300", got)
	}
}
