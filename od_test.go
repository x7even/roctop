package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// odFixture loads a testdata power table verbatim, including any NUL padding
// the driver wrote, which is why the fixtures are copied from sysfs rather
// than pasted as string literals.
func odFixture(t *testing.T, name string) string {
	t.Helper()
	return string(readFixture(t, name))
}

// TestParseODRdna4 covers the shape roctop was built against: a Radeon AI PRO
// R9700 (gfx1201) with a -20 mV undervolt and a 1300 MHz memory ceiling
// applied by gpucap.
func TestParseODRdna4(t *testing.T) {
	t.Parallel()
	got := parseOD(odFixture(t, "pp_od_clk_voltage_rdna4.txt"))

	if !got.Avail() {
		t.Fatal("Avail() = false, want true")
	}
	want := odTable{
		HasV: true, HasS: true, HasM: true,
		VoltOffset: -20, VoltLo: -200, VoltHi: 0,
		SclkOffset: 0, SclkLo: -500, SclkHi: 1000,
		MclkMin: 97, MclkMax: 1300, MclkLo: 97, MclkHi: 1500,
	}
	if got != want {
		t.Errorf("parseOD() =\n %+v\nwant\n %+v", got, want)
	}
}

// TestParseODRdna3 pins the partial case: RDNA3 exposes a voltage offset but
// expresses core and memory clocks as absolute min/max rather than offsets, so
// only HasV may be set. Presence flags, not zero values, carry that.
func TestParseODRdna3(t *testing.T) {
	t.Parallel()
	got := parseOD(odFixture(t, "pp_od_clk_voltage_rdna3.txt"))

	if !got.HasV {
		t.Error("HasV = false, want true (OD_VDDGFX_OFFSET is present)")
	}
	if got.VoltOffset != 0 {
		t.Errorf("VoltOffset = %d, want 0", got.VoltOffset)
	}
	if got.VoltLo != -200 || got.VoltHi != 0 {
		t.Errorf("voltage range = %d..%d, want -200..0", got.VoltLo, got.VoltHi)
	}
	// A zero offset with HasV set is "no offset applied", which must not be
	// confused with "this card has no voltage offset".
	if got.HasS {
		t.Error("HasS = true, want false (OD_SCLK is absolute, not an offset)")
	}
	if got.HasM {
		t.Error("HasM = true, want false (OD_MCLK lists only the ceiling)")
	}
}

// TestParseODVega20 covers a pre-offset driver: per-level V/F curve tables and
// no offsets at all. Nothing must be reported as tunable, in particular not
// from the OD_RANGE rows, which do carry MCLK bounds.
func TestParseODVega20(t *testing.T) {
	t.Parallel()
	got := parseOD(odFixture(t, "pp_od_clk_voltage_vega20.txt"))

	if got.Avail() {
		t.Errorf("Avail() = true, want false; got %+v", got)
	}
	if got != (odTable{}) {
		t.Errorf("parseOD() = %+v, want the zero table", got)
	}
}

func TestParseODEmptyAndGarbage(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"empty":   "",
		"nuls":    "\x00\x00\x00",
		"garbage": "not a power table at all\n",
		"rangeOnly": "OD_RANGE:\nSCLK_OFFSET: -500Mhz 1000Mhz\n" +
			"VDDGFX_OFFSET: -200mv 0mv\nMCLK: 97Mhz 1500Mhz\n",
	} {
		if got := parseOD(in); got.Avail() {
			t.Errorf("parseOD(%s).Avail() = true, want false; got %+v", name, got)
		}
	}
}

// TestParseODStripsNULs guards the driver's section padding: a table that is
// correct apart from interleaved NULs must still parse.
func TestParseODStripsNULs(t *testing.T) {
	t.Parallel()
	padded := "OD_VDDGFX_OFFSET:\n-30mV\n\x00\x00OD_RANGE:\nVDDGFX_OFFSET: -200mv 0mv\n\x00"
	got := parseOD(padded)
	if !got.HasV || got.VoltOffset != -30 {
		t.Errorf("parseOD(padded) HasV=%v VoltOffset=%d, want true/-30", got.HasV, got.VoltOffset)
	}
}

// TestParseODCaseAndIndent covers the two things that vary between kernels:
// the driver mixes "Mhz"/"MHz" and "mv"/"mV", and some versions indent the
// OD_RANGE rows.
func TestParseODCaseAndIndent(t *testing.T) {
	t.Parallel()
	in := "OD_SCLK_OFFSET:\n  +25MHZ\n" +
		"OD_MCLK:\n0: 97MHZ\n1: 1400mhz\n" +
		"OD_RANGE:\n    SCLK_OFFSET:  -500mhz   1000MHz\n    MCLK:  97mhz  1500MHZ\n"
	got := parseOD(in)
	if !got.HasS || got.SclkOffset != 25 || got.SclkLo != -500 || got.SclkHi != 1000 {
		t.Errorf("sclk: Has=%v off=%d range=%d..%d, want true/25/-500..1000",
			got.HasS, got.SclkOffset, got.SclkLo, got.SclkHi)
	}
	if !got.HasM || got.MclkMax != 1400 || got.MclkHi != 1500 {
		t.Errorf("mclk: Has=%v max=%d hi=%d, want true/1400/1500", got.HasM, got.MclkMax, got.MclkHi)
	}
}

// TestParseODRangeMissingCollapses: with no OD_RANGE row, the range must
// collapse onto the current value rather than stay at zero, so nothing
// downstream reports headroom the driver never advertised.
func TestParseODRangeMissingCollapses(t *testing.T) {
	t.Parallel()
	got := parseOD("OD_VDDGFX_OFFSET:\n-50mV\nOD_SCLK_OFFSET:\n100Mhz\n")
	if got.VoltLo != -50 || got.VoltHi != -50 {
		t.Errorf("voltage range = %d..%d, want -50..-50", got.VoltLo, got.VoltHi)
	}
	if got.SclkLo != 100 || got.SclkHi != 100 {
		t.Errorf("sclk range = %d..%d, want 100..100", got.SclkLo, got.SclkHi)
	}
}

// TestReadODMissingFile is the common case on a stock kernel: amdgpu without
// the OverDrive bit in ppfeaturemask does not create the file at all.
func TestReadODMissingFile(t *testing.T) {
	t.Parallel()
	got := readOD(filepath.Join(t.TempDir(), "no-such-card"))
	if got.Avail() {
		t.Errorf("readOD(missing).Avail() = true, want false")
	}
}

func TestReadODFromDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := odFixture(t, "pp_od_clk_voltage_rdna4.txt")
	if err := os.WriteFile(filepath.Join(dir, "pp_od_clk_voltage"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readOD(dir)
	if !got.HasV || got.VoltOffset != -20 || got.MclkMax != 1300 {
		t.Errorf("readOD() = %+v, want the RDNA4 fixture's values", got)
	}
}

// ── parseDpmFreqMax ──────────────────────────────────────────────────

func TestParseDpmFreqMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// RDNA4 sclk table: the starred "S:" row is the live deep-sleep clock,
	// so the ceiling is the highest listed level, not the starred one.
	sclk := write("pp_dpm_sclk", "S: 4Mhz *\n1: 500Mhz \n2: 2350Mhz \n")
	if got := parseDpmFreqMax(sclk); got != 2350 {
		t.Errorf("parseDpmFreqMax(sclk) = %d, want 2350", got)
	}
	mclk := write("pp_dpm_mclk", "0: 96Mhz *\n1: 456Mhz \n2: 1300Mhz \n")
	if got := parseDpmFreqMax(mclk); got != 1300 {
		t.Errorf("parseDpmFreqMax(mclk) = %d, want 1300", got)
	}
	if got := parseDpmFreqMax(write("empty", "")); got != 0 {
		t.Errorf("parseDpmFreqMax(empty) = %d, want 0", got)
	}
	if got := parseDpmFreqMax(write("junk", "no clocks here\n")); got != 0 {
		t.Errorf("parseDpmFreqMax(junk) = %d, want 0", got)
	}
	if got := parseDpmFreqMax(filepath.Join(dir, "absent")); got != 0 {
		t.Errorf("parseDpmFreqMax(missing) = %d, want 0", got)
	}
}

// TestCollectAmdSysfsMetricsOD wires the parser into the collector: a faked
// card directory must yield the OD table and the two clock ceilings.
func TestCollectAmdSysfsMetricsOD(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"pp_od_clk_voltage": odFixture(t, "pp_od_clk_voltage_rdna4.txt"),
		"pp_dpm_sclk":       "S: 4Mhz *\n1: 500Mhz \n2: 2350Mhz \n",
		"pp_dpm_mclk":       "0: 96Mhz *\n1: 1300Mhz \n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	gpu := newGpuData(0, "sysfs")
	collectAmdSysfsMetrics(&gpu, amdSysfsDev{deviceDir: dir})

	if gpu.SclkMax != 2350 || gpu.MclkMax != 1300 {
		t.Errorf("clock ceilings = %d/%d, want 2350/1300", gpu.SclkMax, gpu.MclkMax)
	}
	if !gpu.OD.HasV || gpu.OD.VoltOffset != -20 {
		t.Errorf("OD voltage = %+v, want a -20mV offset", gpu.OD)
	}
	if gpu.OD.MclkMax != 1300 {
		t.Errorf("OD.MclkMax = %d, want 1300", gpu.OD.MclkMax)
	}
}

// TestCollectAmdSysfsPowerCapContext covers the hwmon cap files, including the
// zero-means-unavailable rule that keeps a 0 W default out of the display.
func TestCollectAmdSysfsPowerCapContext(t *testing.T) {
	t.Parallel()
	hwmon := t.TempDir()
	for name, body := range map[string]string{
		"power1_cap":         "210000000\n",
		"power1_cap_default": "300000000\n",
		"power1_cap_min":     "210000000\n",
		"power1_cap_max":     "330000000\n",
	} {
		if err := os.WriteFile(filepath.Join(hwmon, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	gpu := newGpuData(0, "sysfs")
	readHwmonMetrics(&gpu, hwmon)
	if gpu.PowerMax != 210 || gpu.PowerCapDefault != 300 ||
		gpu.PowerCapMin != 210 || gpu.PowerCapMax != 330 {
		t.Errorf("cap/default/min/max = %v/%v/%v/%v, want 210/300/210/330",
			gpu.PowerMax, gpu.PowerCapDefault, gpu.PowerCapMin, gpu.PowerCapMax)
	}

	// An empty hwmon dir must leave every cap value NaN, not 0.
	sparse := newGpuData(1, "sysfs")
	readHwmonMetrics(&sparse, t.TempDir())
	for name, v := range map[string]float64{
		"PowerCapDefault": sparse.PowerCapDefault,
		"PowerCapMin":     sparse.PowerCapMin,
		"PowerCapMax":     sparse.PowerCapMax,
	} {
		if v == 0 {
			t.Errorf("%s = 0 on a card with no hwmon cap files, want NaN", name)
		}
	}

	// A file reporting 0 is "not supported" on amdgpu, not a 0 W limit.
	zero := t.TempDir()
	if err := os.WriteFile(filepath.Join(zero, "power1_cap_default"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	z := newGpuData(2, "sysfs")
	readHwmonMetrics(&z, zero)
	if z.PowerCapDefault == 0 {
		t.Error("PowerCapDefault = 0 for a file reporting 0, want NaN")
	}
}

// TestReadODNoPanicOnDirectory guards against a sysfs path that exists but is
// not a readable file.
func TestReadODNoPanicOnDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pp_od_clk_voltage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readOD(dir); got.Avail() {
		t.Error("readOD(dir-as-file).Avail() = true, want false")
	}
}

// TestFixtureIsVerbatimSysfs documents why the RDNA4 fixture is a copy rather
// than a literal: it must keep the driver's exact spelling, which mixes cases.
func TestFixtureIsVerbatimSysfs(t *testing.T) {
	t.Parallel()
	body := odFixture(t, "pp_od_clk_voltage_rdna4.txt")
	if !strings.Contains(body, "Mhz") || !strings.Contains(body, "MHz") {
		t.Error("fixture no longer carries both Mhz and MHz spellings; it has been hand-edited")
	}
	if !strings.Contains(body, "mv") || !strings.Contains(body, "mV") {
		t.Error("fixture no longer carries both mv and mV spellings; it has been hand-edited")
	}
}
