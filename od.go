package main

// AMD OverDrive state: GFX voltage offset, core clock offset and memory clock
// limits, read from pp_od_clk_voltage.
//
// This is the read half of what tools like gpucap write. roctop never writes
// here — it only reports what the card is currently set to, so an undervolt or
// a raised memory clock applied outside roctop is visible alongside the live
// voltage and clocks it already shows.
//
// The file exists only when amdgpu was loaded with the OverDrive bit (0x4000)
// set in ppfeaturemask; without it every setting reads as unavailable.
//
// Layout on RDNA3/RDNA4 (SMU13/SMU14), as the driver prints it:
//
//	OD_SCLK_OFFSET:
//	0Mhz
//	OD_MCLK:
//	0: 97Mhz
//	1: 1300MHz
//	OD_RANGE:
//	SCLK_OFFSET:    -500Mhz       1000Mhz
//	MCLK:      97Mhz       1500Mhz
//	VDDGFX_OFFSET:    -200mv          0mv
//	OD_VDDGFX_OFFSET:
//	-20mV
//
// Older ASICs print per-level V/F tables (OD_SCLK: with indexed rows) instead.
// Those carry no offsets, so they parse as unavailable rather than half-filled.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// odTable is the parsed contents of pp_od_clk_voltage. Each hasX flag says
// whether the card listed that setting at all — zero is a real, common value
// for both offsets, so presence cannot be inferred from the value.
type odTable struct {
	HasV, HasS, HasM bool
	VoltOffset       int // GFX voltage offset, mV (negative = undervolt)
	VoltLo, VoltHi   int // allowed voltage offset range, mV
	SclkOffset       int // core clock offset, MHz
	SclkLo, SclkHi   int // allowed core clock offset range, MHz
	MclkMin, MclkMax int // current memory clock floor/ceiling, MHz
	MclkLo, MclkHi   int // allowed memory clock range, MHz
}

// Avail reports whether the card listed any tunable at all.
func (t odTable) Avail() bool { return t.HasV || t.HasS || t.HasM }

// The driver mixes "Mhz"/"MHz" and "mv"/"mV" within one file, and some kernels
// indent the OD_RANGE rows, so every pattern is case-insensitive and tolerates
// leading whitespace.
var (
	reOdVolt      = regexp.MustCompile(`(?mi)^\s*OD_VDDGFX_OFFSET:\s*\n\s*([-+]?\d+)\s*mv`)
	reOdVoltRange = regexp.MustCompile(`(?mi)^\s*VDDGFX_OFFSET:\s*([-+]?\d+)\s*mv\s+([-+]?\d+)\s*mv`)
	reOdSclk      = regexp.MustCompile(`(?mi)^\s*OD_SCLK_OFFSET:\s*\n\s*([-+]?\d+)\s*mhz`)
	reOdSclkRange = regexp.MustCompile(`(?mi)^\s*SCLK_OFFSET:\s*([-+]?\d+)\s*mhz\s+([-+]?\d+)\s*mhz`)
	reOdMclk      = regexp.MustCompile(`(?mi)^\s*OD_MCLK:\s*\n\s*0:\s*(\d+)\s*mhz\s*\n\s*1:\s*(\d+)\s*mhz`)
	reOdMclkRange = regexp.MustCompile(`(?mi)^\s*MCLK:\s*(\d+)\s*mhz\s+(\d+)\s*mhz`)
)

func atoiOr0(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

// parseOD parses pp_od_clk_voltage. A setting whose OD_RANGE row is missing
// gets a range collapsed onto its current value, which is what the renderer
// needs so it never claims headroom the driver did not advertise.
func parseOD(table string) odTable {
	table = strings.ReplaceAll(table, "\x00", "") // the driver pads sections with NULs
	var t odTable
	if m := reOdVolt.FindStringSubmatch(table); m != nil {
		t.HasV, t.VoltOffset = true, atoiOr0(m[1])
		t.VoltLo, t.VoltHi = t.VoltOffset, t.VoltOffset
		if r := reOdVoltRange.FindStringSubmatch(table); r != nil {
			t.VoltLo, t.VoltHi = atoiOr0(r[1]), atoiOr0(r[2])
		}
	}
	if m := reOdSclk.FindStringSubmatch(table); m != nil {
		t.HasS, t.SclkOffset = true, atoiOr0(m[1])
		t.SclkLo, t.SclkHi = t.SclkOffset, t.SclkOffset
		if r := reOdSclkRange.FindStringSubmatch(table); r != nil {
			t.SclkLo, t.SclkHi = atoiOr0(r[1]), atoiOr0(r[2])
		}
	}
	if m := reOdMclk.FindStringSubmatch(table); m != nil {
		t.HasM, t.MclkMin, t.MclkMax = true, atoiOr0(m[1]), atoiOr0(m[2])
		t.MclkLo, t.MclkHi = t.MclkMin, t.MclkMax
		if r := reOdMclkRange.FindStringSubmatch(table); r != nil {
			t.MclkLo, t.MclkHi = atoiOr0(r[1]), atoiOr0(r[2])
		}
	}
	return t
}

// readOD reads and parses deviceDir/pp_od_clk_voltage. A missing file (the
// usual case: OverDrive not enabled in ppfeaturemask) yields a zero table.
func readOD(deviceDir string) odTable {
	b, err := os.ReadFile(deviceDir + "/pp_od_clk_voltage")
	if err != nil {
		return odTable{}
	}
	return parseOD(string(b))
}
