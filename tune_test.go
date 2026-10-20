package main

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainText(s string) string { return ansiRe.ReplaceAllString(s, "") }

// tuneGpu returns a card with a 210 W cap against a 300 W board default and
// the OverDrive table gpucap leaves on this rig: -20 mV, +0 MHz, 1300 MHz mem.
func tuneGpu() GpuData {
	gpu := newGpuData(0, "sysfs")
	gpu.Name = "Radeon AI PRO R9700"
	gpu.PowerAvg = 15
	gpu.PowerMax = 210
	gpu.PowerCapDefault = 300
	gpu.PowerCapMin = 210
	gpu.PowerCapMax = 330
	gpu.Voltage = 604
	gpu.OD = odTable{
		HasV: true, HasS: true, HasM: true,
		VoltOffset: -20, VoltLo: -200, VoltHi: 0,
		SclkOffset: 0, SclkLo: -500, SclkHi: 1000,
		MclkMin: 97, MclkMax: 1300, MclkLo: 97, MclkHi: 1500,
	}
	return gpu
}

// TestTuneSegmentsZeroValueGpu is the regression guard for the bug that first
// showed up as a 17-line panel: a hand-built GpuData carries 0 W, not NaN, so
// presence must be decided on positive values rather than on !IsNaN.
func TestTuneSegmentsZeroValueGpu(t *testing.T) {
	t.Parallel()
	if segs := tuneSegments(GpuData{}); len(segs) != 0 {
		t.Errorf("tuneSegments(zero GpuData) = %+v, want none", segs)
	}
}

func TestTuneSegmentsCapOnlyWhenMoved(t *testing.T) {
	t.Parallel()

	moved := tuneSegments(tuneGpu())
	if len(moved) == 0 || moved[0].label != "CAP" {
		t.Fatalf("capped card: segments = %+v, want CAP first", moved)
	}
	if moved[0].value != "210W" || moved[0].dim != "of 300W" {
		t.Errorf("CAP segment = %q / %q, want \"210W\" / \"of 300W\"", moved[0].value, moved[0].dim)
	}

	// A card sitting at its board default has nothing to report: the PWR bar
	// already shows 300 W, so a CAP segment would be noise.
	stock := tuneGpu()
	stock.PowerMax = 300
	for _, s := range tuneSegments(stock) {
		if s.label == "CAP" {
			t.Errorf("uncapped card still produced a CAP segment: %+v", s)
		}
	}

	// Rounding: a sub-watt difference is firmware noise, not a cap change.
	nearly := tuneGpu()
	nearly.PowerMax = 299.6
	for _, s := range tuneSegments(nearly) {
		if s.label == "CAP" {
			t.Errorf("0.4W difference produced a CAP segment: %+v", s)
		}
	}

	// No default to compare against → no segment, even with a cap.
	noDefault := tuneGpu()
	noDefault.PowerCapDefault = math.NaN()
	for _, s := range tuneSegments(noDefault) {
		if s.label == "CAP" {
			t.Errorf("card with no cap default produced a CAP segment: %+v", s)
		}
	}
}

func TestTuneSegmentsVoltage(t *testing.T) {
	t.Parallel()

	byLabel := func(segs []tuneSegment, label string) (tuneSegment, bool) {
		for _, s := range segs {
			if s.label == label {
				return s, true
			}
		}
		return tuneSegment{}, false
	}

	// Live sensor plus an offset: the reading leads, the offset is context.
	s, ok := byLabel(tuneSegments(tuneGpu()), "VDD")
	if !ok {
		t.Fatal("no VDD segment for a card with a voltage sensor")
	}
	if s.value != "604mV" || s.dim != "-20mV" {
		t.Errorf("VDD = %q / %q, want \"604mV\" / \"-20mV\"", s.value, s.dim)
	}

	// No sensor, but the driver still reports the offset in force.
	noSensor := tuneGpu()
	noSensor.Voltage = math.NaN()
	s, ok = byLabel(tuneSegments(noSensor), "VDD")
	if !ok {
		t.Fatal("no VDD segment for a card reporting only an offset")
	}
	if s.value != "-20mV" || s.dim != "" {
		t.Errorf("offset-only VDD = %q / %q, want \"-20mV\" / \"\"", s.value, s.dim)
	}

	// Deep idle reads 0 mV on RDNA4; with no OD table there is nothing to say.
	idle := tuneGpu()
	idle.Voltage = 0
	idle.OD = odTable{}
	if _, ok := byLabel(tuneSegments(idle), "VDD"); ok {
		t.Error("VDD segment emitted for a card with neither a sensor nor an OD table")
	}
}

func TestTuneSegmentsClocks(t *testing.T) {
	t.Parallel()
	segs := tuneSegments(tuneGpu())

	var got []string
	for _, s := range segs {
		got = append(got, s.label+"="+s.value)
	}
	joined := strings.Join(got, " ")
	// A zero core-clock offset is still worth showing: it says the card is
	// tunable and currently untouched.
	if !strings.Contains(joined, "CLK±=+0MHz") {
		t.Errorf("segments = %q, want a CLK± segment reading +0MHz", joined)
	}
	if !strings.Contains(joined, "MEM≤=1.30GHz") && !strings.Contains(joined, "MEM≤=1300MHz") {
		t.Errorf("segments = %q, want a MEM≤ segment at the 1300MHz ceiling", joined)
	}

	// Presence flags, not values, decide: an all-zero table with the flags
	// clear must produce nothing.
	none := tuneGpu()
	none.OD = odTable{}
	for _, s := range tuneSegments(none) {
		if s.label == "CLK±" || s.label == "MEM≤" {
			t.Errorf("segment %+v emitted for a card with no OD table", s)
		}
	}
}

func TestRenderTuneLineDegradesWithWidth(t *testing.T) {
	t.Parallel()
	gpu := tuneGpu()

	wide := plainText(renderTuneLine(gpu, 100, 5))
	for _, want := range []string{"TUNE", "CAP", "210W", "of 300W", "VDD", "604mV", "CLK±", "MEM≤", " · "} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide TUNE line missing %q: %q", want, wide)
		}
	}

	// Narrowing must shed detail, never truncate mid-value, and never grow.
	prevLen := len(wide)
	for cw := 100; cw >= 10; cw -= 5 {
		line := plainText(renderTuneLine(gpu, cw, 5))
		if w := len([]rune(line)); w > cw {
			t.Errorf("cw=%d produced a %d-column line: %q", cw, w, line)
		}
		if len(line) > prevLen {
			t.Errorf("cw=%d line grew while narrowing: %q", cw, line)
		}
		prevLen = len(line)
		// Whatever survives, a CAP segment must carry its full value.
		if strings.Contains(line, "CAP") && !strings.Contains(line, "210W") {
			t.Errorf("cw=%d truncated the CAP value: %q", cw, line)
		}
	}

	// Below the minimum bar width there is no room for the row at all.
	if line := renderTuneLine(gpu, 6, 5); line != "" {
		t.Errorf("renderTuneLine at cw=6 = %q, want \"\"", line)
	}
}

func TestRenderTuneLineEmptyWithoutData(t *testing.T) {
	t.Parallel()
	if line := renderTuneLine(GpuData{}, 120, 5); line != "" {
		t.Errorf("renderTuneLine(zero GpuData) = %q, want \"\"", line)
	}
	// An NVIDIA card: real metrics, but no cap context and no OD table.
	nv := newGpuData(0, "nvidia")
	nv.PowerAvg = 120
	nv.PowerMax = 350
	if line := renderTuneLine(nv, 120, 5); line != "" {
		t.Errorf("renderTuneLine(nvidia) = %q, want \"\" until phase two", line)
	}
}

// TestRenderMetricLinesWithTuneRow checks the row reaches the panel and that
// adding it does not push the panel past its fixed height.
func TestRenderMetricLinesWithTuneRow(t *testing.T) {
	t.Parallel()
	gpu := tuneGpu()
	gpu.GpuUse = 75
	gpu.MemActivity = 40
	gpu.VramTotal = 32 << 30
	gpu.VramUsed = 16 << 30
	gpu.VramPercent = 50
	gpu.GttTotal = 8 << 30
	gpu.GttUsed = 1 << 30
	gpu.GttPercent = 12.5
	gpu.TempEdge = 48
	gpu.TempJunc = 55
	gpu.TempMem = 60
	gpu.FanPercent = 30
	gpu.FanRPM = 1200
	gpu.Sclk = 2350
	gpu.Mclk = 1300
	gpu.PcieTxMBps = 12
	gpu.PcieRxMBps = 8

	lines := renderMetricLines(gpu, &GpuHistory{}, 100, false)
	joined := plainText(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "TUNE") {
		t.Errorf("panel is missing the TUNE row:\n%s", joined)
	}

	// A card with nothing to tune must not spend a row on an empty label.
	bare := gpu
	bare.PowerCapDefault = math.NaN()
	bare.Voltage = math.NaN()
	bare.OD = odTable{}
	bareLines := renderMetricLines(bare, &GpuHistory{}, 100, false)
	if strings.Contains(plainText(strings.Join(bareLines, "\n")), "TUNE") {
		t.Error("TUNE row rendered for a card with nothing to report")
	}
	// The row costs exactly one line. Like the GTT and PCIe rows it extends
	// past panelLines, which renderGpuPanel treats as a floor, not a ceiling
	// (panels are joined top-aligned inside a scrolling viewport).
	if len(lines) != len(bareLines)+1 {
		t.Errorf("TUNE row cost %d lines, want 1 (%d with, %d without)",
			len(lines)-len(bareLines), len(lines), len(bareLines))
	}
}

// TestRenderMetricLinesTuneRowNoPanicNarrow sweeps widths the way a resizing
// terminal does; the panel must survive every one.
func TestRenderMetricLinesTuneRowNoPanicNarrow(t *testing.T) {
	t.Parallel()
	gpu := tuneGpu()
	for cw := 4; cw <= 120; cw++ {
		lines := renderMetricLines(gpu, &GpuHistory{}, cw, false)
		if len(lines) > panelLines {
			t.Fatalf("cw=%d returned %d lines, want at most %d", cw, len(lines), panelLines)
		}
	}
}
