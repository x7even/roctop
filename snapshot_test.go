package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// snapshotFixtureGpus returns one fully-populated GPU and one with the
// NaN/empty defaults a sysfs-only card would carry.
func snapshotFixtureGpus() []GpuData {
	full := newGpuData(0, "rocm")
	full.Name = "AMD Instinct MI210"
	full.Vendor = "Advanced Micro Devices"
	full.GpuUse = 100
	full.MemActivity = 43
	full.VramUsed = 30 << 30
	full.VramTotal = 64 << 30
	full.VramPercent = 46.9
	full.PowerAvg = 250
	full.PowerMax = 300
	full.TempEdge = 55
	full.TempJunc = 62
	full.TempMem = 71
	full.FanPercent = 40
	full.FanRPM = 1800
	full.Sclk = 1700
	full.Mclk = 1600
	full.PerfLevel = "auto"
	full.PcieBus = "0000:83:00.0"
	full.PcieSpeed = "16.0GT/s"
	full.PcieWidth = 16
	full.ThrottleStatus = 1
	full.ThrottleReasons = []string{"POWER_LIMIT"}

	sparse := newGpuData(1, "sysfs")
	sparse.Name = "Unknown GPU"
	// All NaN-able floats stay NaN from newGpuData; FanPercent defaults to 0
	// there, so force NaN to exercise the N/A path.
	sparse.FanPercent = math.NaN()
	return []GpuData{full, sparse}
}

func snapshotFixtureProcs() []ProcessData {
	return []ProcessData{
		{PID: 4242, Name: "vllm::worker_tp", GpuIDs: []int{1, 0}, VramUsed: 28 << 30},
		{PID: 99, Name: "mystery", VramUsed: 1 << 20}, // no GPU attribution
	}
}

func TestSnapshotTextContent(t *testing.T) {
	out := renderSnapshotText(snapshotFixtureGpus(), snapshotFixtureProcs(), []string{"rocm", "sysfs"})

	for _, want := range []string{
		"backends: rocm+sysfs",
		"GPU 0 [rocm] AMD Instinct MI210",
		"Use: 100%",
		"MACT: 43%",
		"VRAM: 30.0GB / 64.0GB (46.9%)",
		"Power: 250W avg / 300W cap",
		"Temp: edge 55C  junction 62C  mem 71C",
		"Fan: 40% (1800 RPM)",
		"Clocks: sclk 1.70GHz  mclk 1.60GHz   Perf: auto",
		"PCIe: bus 0000:83:00.0  link 16.0GT/s x16",
		"PCIe BW: n/a (requires TUI sampling)",
		"Throttle: POWER_LIMIT",
		"GPU 1 [sysfs] Unknown GPU",
		"4242",
		"vllm::worker_tp",
		"0,1", // GpuIDs sorted
		"28.0 GB",
		"?", // process without attribution
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot text missing %q\n---\n%s", want, out)
		}
	}
}

func TestSnapshotTextNaNPrintsNA(t *testing.T) {
	out := renderSnapshotText(snapshotFixtureGpus(), nil, []string{"sysfs"})
	// The sparse GPU block: every NaN metric must render as N/A.
	sparse := out[strings.Index(out, "GPU 1 [sysfs]"):]
	for _, want := range []string{
		"Use: N/A", "MACT: N/A", "Power: N/A avg / N/A cap",
		"edge N/A", "junction N/A", "mem N/A", "Fan: N/A",
	} {
		if !strings.Contains(sparse, want) {
			t.Errorf("sparse GPU block missing %q\n---\n%s", want, sparse)
		}
	}
	if strings.Contains(out, "NaN") {
		t.Error("snapshot text must never print NaN")
	}
	if !strings.Contains(out, "(no GPU processes)") {
		t.Error("empty process list must be labelled")
	}
}

func TestSnapshotTextNoANSI(t *testing.T) {
	out := renderSnapshotText(snapshotFixtureGpus(), snapshotFixtureProcs(), []string{"rocm"})
	if strings.Contains(out, "\x1b") {
		t.Error("snapshot text must not contain ANSI escapes")
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	raw, err := buildSnapshotJSON(snapshotFixtureGpus(), snapshotFixtureProcs(), []string{"rocm", "sysfs"})
	if err != nil {
		t.Fatalf("buildSnapshotJSON: %v", err)
	}

	var snap struct {
		Version   string   `json:"version"`
		Backends  []string `json:"backends"`
		Gpus      []map[string]interface{}
		Processes []map[string]interface{}
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if snap.Version != version {
		t.Errorf("version = %q, want %q", snap.Version, version)
	}
	if len(snap.Backends) != 2 || snap.Backends[0] != "rocm" {
		t.Errorf("backends = %v", snap.Backends)
	}
	if len(snap.Gpus) != 2 || len(snap.Processes) != 2 {
		t.Fatalf("got %d gpus, %d processes", len(snap.Gpus), len(snap.Processes))
	}

	full := snap.Gpus[0]
	if full["card_id"].(float64) != 0 || full["backend"] != "rocm" {
		t.Errorf("unexpected first gpu: %v", full)
	}
	if full["gpu_use_percent"].(float64) != 100 {
		t.Errorf("gpu_use_percent = %v", full["gpu_use_percent"])
	}
	if full["vram_total_bytes"].(float64) != float64(64<<30) {
		t.Errorf("vram_total_bytes = %v", full["vram_total_bytes"])
	}

	// NaN fields must serialize as JSON null on the sparse GPU.
	sparse := snap.Gpus[1]
	for _, key := range []string{
		"gpu_use_percent", "mem_activity_percent", "power_avg_watts",
		"power_cap_watts", "temp_edge_c", "temp_junction_c", "temp_mem_c",
	} {
		if v, present := sparse[key]; !present || v != nil {
			t.Errorf("sparse gpu %s = %v, want null", key, v)
		}
	}

	proc := snap.Processes[0]
	if proc["pid"].(float64) != 4242 || proc["name"] != "vllm::worker_tp" {
		t.Errorf("unexpected first process: %v", proc)
	}
	ids := proc["gpu_ids"].([]interface{})
	if len(ids) != 2 || ids[0].(float64) != 0 || ids[1].(float64) != 1 {
		t.Errorf("gpu_ids = %v, want sorted [0 1]", ids)
	}
	// A process with no attribution must still emit an empty array, not null.
	if v := snap.Processes[1]["gpu_ids"]; v == nil {
		t.Error("gpu_ids for unattributed process must be [], not null")
	}
}

func TestSnapshotJSONNeverEmitsNaN(t *testing.T) {
	// Every NaN-able float set to NaN must not break marshalling.
	g := newGpuData(0, "rocm")
	g.VramPercent = math.NaN()
	g.Voltage = math.NaN()
	raw, err := buildSnapshotJSON([]GpuData{g}, nil, nil)
	if err != nil {
		t.Fatalf("buildSnapshotJSON with all-NaN gpu: %v", err)
	}
	if strings.Contains(string(raw), "NaN") {
		t.Errorf("JSON output contains NaN:\n%s", raw)
	}
	var snap map[string]interface{}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if snap["backends"] == nil {
		t.Error("backends must be [], not null, when no backends given")
	}
}
