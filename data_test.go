package main

import (
	"testing"
)

// ── RingBuffer ────────────────────────────────────────────────────────

func TestRingBufferEmpty(t *testing.T) {
	var rb RingBuffer
	if got := rb.Values(); got != nil {
		t.Errorf("expected nil from empty buffer, got %v", got)
	}
}

func TestRingBufferNoWrap(t *testing.T) {
	var rb RingBuffer
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	got := rb.Values()
	want := []float64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRingBufferWrapAround(t *testing.T) {
	var rb RingBuffer
	// Fill past capacity; only last maxHistory values should be visible.
	for i := 0; i < maxHistory+5; i++ {
		rb.Push(float64(i))
	}
	got := rb.Values()
	if len(got) != maxHistory {
		t.Fatalf("len %d, want %d", len(got), maxHistory)
	}
	// First visible value should be 5 (oldest after wrap).
	if got[0] != 5 {
		t.Errorf("got[0] = %v, want 5", got[0])
	}
	// Last visible value should be maxHistory+4.
	if got[maxHistory-1] != float64(maxHistory+4) {
		t.Errorf("got[last] = %v, want %v", got[maxHistory-1], float64(maxHistory+4))
	}
}

func TestRingBufferOrderPreserved(t *testing.T) {
	var rb RingBuffer
	for i := 0; i < maxHistory; i++ {
		rb.Push(float64(i))
	}
	got := rb.Values()
	for i, v := range got {
		if v != float64(i) {
			t.Errorf("[%d] got %v, want %v", i, v, float64(i))
		}
	}
}

// ── parseFloat ───────────────────────────────────────────────────────

func TestParseFloat(t *testing.T) {
	cases := []struct {
		input string
		def   float64
		want  float64
	}{
		{"123.45", 0, 123.45},
		{"  42.0 W ", 0, 42.0},
		{"0", 0, 0},
		{"", 99, 99},
		{"N/A", 7, 7},
		{"-10.5", 0, -10.5},
	}
	for _, c := range cases {
		got := parseFloat(c.input, c.def)
		if got != c.want {
			t.Errorf("parseFloat(%q, %v) = %v, want %v", c.input, c.def, got, c.want)
		}
	}
}

// ── parseInt ─────────────────────────────────────────────────────────

func TestParseInt(t *testing.T) {
	cases := []struct {
		input string
		def   int
		want  int
	}{
		{"42", 0, 42},
		{"1800 RPM", 0, 1800},
		{"", 5, 5},
		{"N/A", 3, 3},
	}
	for _, c := range cases {
		got := parseInt(c.input, c.def)
		if got != c.want {
			t.Errorf("parseInt(%q, %v) = %v, want %v", c.input, c.def, got, c.want)
		}
	}
}

// ── parseInt64 ───────────────────────────────────────────────────────

func TestParseInt64(t *testing.T) {
	cases := []struct {
		input string
		def   int64
		want  int64
	}{
		{"8589934592", 0, 8589934592}, // 8 GiB in bytes
		{"", 10, 10},
		{"N/A", 0, 0},
	}
	for _, c := range cases {
		got := parseInt64(c.input, c.def)
		if got != c.want {
			t.Errorf("parseInt64(%q, %v) = %v, want %v", c.input, c.def, got, c.want)
		}
	}
}

// ── parseMHz ─────────────────────────────────────────────────────────

func TestParseMHz(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"1800Mhz", 1800},
		{"1800 MHz", 1800},
		{"2100MHZ", 2100},
		{"1234", 1234},
		{"", 0},
	}
	for _, c := range cases {
		got := parseMHz(c.input)
		if got != c.want {
			t.Errorf("parseMHz(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

// ── throttleReasons ──────────────────────────────────────────────────

func TestThrottleReasonsZero(t *testing.T) {
	if got := throttleReasons(0); got != nil {
		t.Errorf("expected nil for status 0, got %v", got)
	}
}

func TestThrottleReasonsSingleBit(t *testing.T) {
	// Bit 0 = POWER_LIMIT
	got := throttleReasons(1)
	if len(got) != 1 || got[0] != "POWER_LIMIT" {
		t.Errorf("got %v, want [POWER_LIMIT]", got)
	}
}

func TestThrottleReasonsMultipleBits(t *testing.T) {
	// Bits 0 (POWER_LIMIT) and 1 (THERMAL)
	got := throttleReasons(3)
	if len(got) != 2 {
		t.Fatalf("got %d reasons, want 2: %v", len(got), got)
	}
	// Results are sorted, so POWER_LIMIT < THERMAL alphabetically
	if got[0] != "POWER_LIMIT" || got[1] != "THERMAL" {
		t.Errorf("got %v, want [POWER_LIMIT THERMAL]", got)
	}
}

func TestThrottleReasonsHighBit(t *testing.T) {
	// Bit 22 = VR_TEMP
	got := throttleReasons(1 << 22)
	if len(got) != 1 || got[0] != "VR_TEMP" {
		t.Errorf("got %v, want [VR_TEMP]", got)
	}
}

// ── parseGPU ─────────────────────────────────────────────────────────

func TestParseGPUBasicFields(t *testing.T) {
	d := map[string]interface{}{
		"Card Series":                              "Radeon RX 7900 XTX",
		"Card Vendor":                              "Advanced Micro Devices, Inc. [AMD/ATI]",
		"Card SKU":                                 "D413",
		"GFX Version":                             "gfx1100",
		"Temperature (Sensor edge) (C)":           "62.0",
		"Temperature (Sensor junction) (C)":       "68.0",
		"Temperature (Sensor memory) (C)":         "60.0",
		"GPU use (%)":                             "75.0",
		"GPU Memory Read/Write Activity (%)":      "50.0",
		"VRAM Total Memory (B)":                   "25753026560",
		"VRAM Total Used Memory (B)":              "12876513280",
		"Average Graphics Package Power (W)":      "220.5",
		"Max Graphics Package Power (W)":          "355.0",
		"Fan speed (%)":                           "45.0",
		"Fan RPM":                                 "1800",
		"Current sclk clock speed:":               "2500Mhz",
		"Current mclk clock speed:":               "1000Mhz",
		"Voltage (mV)":                            "850",
		"Performance Level":                       "auto",
		"PCI Bus":                                 "0000:03:00.0",
	}
	gpu := parseGPU(2, d)

	if gpu.CardID != 2 {
		t.Errorf("CardID = %d, want 2", gpu.CardID)
	}
	if gpu.Name != "Radeon RX 7900 XTX" {
		t.Errorf("Name = %q", gpu.Name)
	}
	if gpu.TempEdge != 62.0 {
		t.Errorf("TempEdge = %v, want 62.0", gpu.TempEdge)
	}
	if gpu.TempJunc != 68.0 {
		t.Errorf("TempJunc = %v, want 68.0", gpu.TempJunc)
	}
	if gpu.TempMem != 60.0 {
		t.Errorf("TempMem = %v, want 60.0", gpu.TempMem)
	}
	if gpu.GpuUse != 75.0 {
		t.Errorf("GpuUse = %v, want 75.0", gpu.GpuUse)
	}
	if gpu.MemActivity != 50.0 {
		t.Errorf("MemActivity = %v, want 50.0", gpu.MemActivity)
	}
	if gpu.VramTotal != 25753026560 {
		t.Errorf("VramTotal = %v", gpu.VramTotal)
	}
	if gpu.VramUsed != 12876513280 {
		t.Errorf("VramUsed = %v", gpu.VramUsed)
	}
	wantPct := float64(12876513280) / float64(25753026560) * 100
	if gpu.VramPercent < wantPct-0.01 || gpu.VramPercent > wantPct+0.01 {
		t.Errorf("VramPercent = %v, want ~%v", gpu.VramPercent, wantPct)
	}
	if gpu.PowerAvg != 220.5 {
		t.Errorf("PowerAvg = %v, want 220.5", gpu.PowerAvg)
	}
	if gpu.PowerMax != 355.0 {
		t.Errorf("PowerMax = %v, want 355.0", gpu.PowerMax)
	}
	if gpu.FanPercent != 45.0 {
		t.Errorf("FanPercent = %v", gpu.FanPercent)
	}
	if gpu.FanRPM != 1800 {
		t.Errorf("FanRPM = %d, want 1800", gpu.FanRPM)
	}
	if gpu.Voltage != 850.0 {
		t.Errorf("Voltage = %v, want 850.0", gpu.Voltage)
	}
	if gpu.PerfLevel != "auto" {
		t.Errorf("PerfLevel = %q", gpu.PerfLevel)
	}
}

func TestParseGPUFallbackName(t *testing.T) {
	// When Card Series is empty, fall back to Card Model.
	d := map[string]interface{}{
		"Card Series": "",
		"Card Model":  "Radeon RX 6800",
	}
	gpu := parseGPU(0, d)
	if gpu.Name != "Radeon RX 6800" {
		t.Errorf("Name = %q, want 'Radeon RX 6800'", gpu.Name)
	}
}

func TestParseGPUDefaultName(t *testing.T) {
	// No name fields at all.
	gpu := parseGPU(5, map[string]interface{}{})
	if gpu.Name != "GPU 5" {
		t.Errorf("Name = %q, want 'GPU 5'", gpu.Name)
	}
}

func TestParseGPUPowerMaxDefault(t *testing.T) {
	// Missing max power should default to 300.
	d := map[string]interface{}{}
	gpu := parseGPU(0, d)
	if gpu.PowerMax != 300 {
		t.Errorf("PowerMax = %v, want 300", gpu.PowerMax)
	}
}

func TestParseGPUVramPercentFallback(t *testing.T) {
	// When VramTotal is 0, fall back to the percentage key.
	d := map[string]interface{}{
		"GPU Memory Allocated (VRAM%)": "33.0",
	}
	gpu := parseGPU(0, d)
	if gpu.VramPercent != 33.0 {
		t.Errorf("VramPercent = %v, want 33.0", gpu.VramPercent)
	}
}

// ── parseProcesses ───────────────────────────────────────────────────

func TestParseProcessesSingle(t *testing.T) {
	system := map[string]interface{}{
		"PID1234": "python3, 0, 1073741824",
	}
	procs := parseProcesses(system)
	if len(procs) != 1 {
		t.Fatalf("got %d procs, want 1", len(procs))
	}
	p := procs[0]
	if p.PID != 1234 {
		t.Errorf("PID = %d, want 1234", p.PID)
	}
	if p.Name != "python3" {
		t.Errorf("Name = %q", p.Name)
	}
	if len(p.GpuIDs) != 1 || p.GpuIDs[0] != 0 {
		t.Errorf("GpuIDs = %v, want [0]", p.GpuIDs)
	}
	if p.VramUsed != 1073741824 {
		t.Errorf("VramUsed = %d", p.VramUsed)
	}
}

func TestParseProcessesMultiGPUAggregation(t *testing.T) {
	// rocm-smi actually uses unique keys like "PID999" per (pid,gpu) pair.
	// Test two distinct processes to confirm sort ordering.
	system := map[string]interface{}{
		"PID42":  "trainer, 0, 1000000000",
		"PID420": "trainer, 1, 500000000",
	}
	procs := parseProcesses(system)
	if len(procs) != 2 {
		t.Fatalf("got %d procs, want 2", len(procs))
	}
	// Sorted by VRAM descending: PID42 first.
	if procs[0].PID != 42 {
		t.Errorf("expected PID 42 first (higher VRAM), got %d", procs[0].PID)
	}
}

func TestParseProcessesSortedByVram(t *testing.T) {
	system := map[string]interface{}{
		"PID1": "small, 0, 100000000",
		"PID2": "large, 0, 900000000",
		"PID3": "mid,   0, 500000000",
	}
	procs := parseProcesses(system)
	if len(procs) != 3 {
		t.Fatalf("got %d procs, want 3", len(procs))
	}
	if procs[0].PID != 2 || procs[1].PID != 3 || procs[2].PID != 1 {
		t.Errorf("sort order wrong: %v", []int{procs[0].PID, procs[1].PID, procs[2].PID})
	}
}

func TestParseProcessesSkipsNonPID(t *testing.T) {
	system := map[string]interface{}{
		"PID10":  "foo, 0, 100",
		"system": "some metadata",
		"":       "empty key",
	}
	procs := parseProcesses(system)
	if len(procs) != 1 {
		t.Fatalf("got %d procs, want 1", len(procs))
	}
}
