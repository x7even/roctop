package main

// Tests for the PDH GPU counter instance parsers and aggregation.
// testdata/pdh_instances.json is a SYNTHETIC capture modeled on the
// documented "pid_<P>_luid_0x<HI>_0x<LO>_phys_<n>_eng_<n>_engtype_<T>"
// format, pending a real `typeperf "\GPU Engine(*)\Utilization
// Percentage" -sc 2` dump from the maintainer's Windows + AMD box.

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"testing"
)

func readPdhFixture(t *testing.T) pdhSample {
	t.Helper()
	var s pdhSample
	if err := json.Unmarshal(readFixture(t, "pdh_instances.json"), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ── Instance-name parsing ────────────────────────────────────────────

func TestParseEngineInstance(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		pid     int
		luid    string
		engtype string
		ok      bool
	}{
		{"plain 3D", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_3_engtype_3D",
			1234, "0x00000000_0x0000c51e", "3D", true},
		{"engtype with underscore", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_4_engtype_Compute_0",
			1234, "0x00000000_0x0000c51e", "Compute_0", true},
		{"engtype with spaces", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_7_engtype_High Priority Compute",
			1234, "0x00000000_0x0000c51e", "High Priority Compute", true},
		{"luid case folded", "pid_8_luid_0x00000000_0x0000ABCD_phys_0_eng_0_engtype_VideoDecode",
			8, "0x00000000_0x0000abcd", "VideoDecode", true},
		{"pid zero parses", "pid_0_luid_0x00000000_0x0000C51E_phys_0_eng_0_engtype_3D",
			0, "0x00000000_0x0000c51e", "3D", true},
		{"missing engtype marker", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_3", 0, "", "", false},
		{"empty engtype", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_3_engtype_", 0, "", "", false},
		{"non-numeric pid", "pid_abc_luid_0x00000000_0x0000C51E_phys_0_eng_3_engtype_3D", 0, "", "", false},
		{"missing eng token", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_engtype_3D", 0, "", "", false},
		{"missing 0x prefix", "pid_1234_luid_00000000_0x0000C51E_phys_0_eng_3_engtype_3D", 0, "", "", false},
		{"empty string", "", 0, "", "", false},
	}
	for _, c := range cases {
		pid, luid, engtype, ok := parseEngineInstance(c.in)
		if pid != c.pid || luid != c.luid || engtype != c.engtype || ok != c.ok {
			t.Errorf("%s: parseEngineInstance(%q) = (%d, %q, %q, %v), want (%d, %q, %q, %v)",
				c.name, c.in, pid, luid, engtype, ok, c.pid, c.luid, c.engtype, c.ok)
		}
	}
}

func TestParseMemInstance(t *testing.T) {
	cases := []struct {
		name string
		in   string
		pid  int
		luid string
		ok   bool
	}{
		{"plain", "pid_1234_luid_0x00000000_0x0000C51E_phys_0",
			1234, "0x00000000_0x0000c51e", true},
		{"case folded", "pid_9012_luid_0x00000000_0x0000D20A_phys_0",
			9012, "0x00000000_0x0000d20a", true},
		{"engine name rejected", "pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_3_engtype_3D", 0, "", false},
		{"adapter name rejected", "luid_0x00000000_0x0000C51E_phys_0", 0, "", false},
		{"non-numeric pid", "pid_x_luid_0x00000000_0x0000C51E_phys_0", 0, "", false},
		{"empty string", "", 0, "", false},
	}
	for _, c := range cases {
		pid, luid, ok := parseMemInstance(c.in)
		if pid != c.pid || luid != c.luid || ok != c.ok {
			t.Errorf("%s: parseMemInstance(%q) = (%d, %q, %v), want (%d, %q, %v)",
				c.name, c.in, pid, luid, ok, c.pid, c.luid, c.ok)
		}
	}
}

func TestParseAdapterMemInstance(t *testing.T) {
	cases := []struct {
		name string
		in   string
		luid string
		ok   bool
	}{
		{"plain", "luid_0x00000000_0x0000C51E_phys_0", "0x00000000_0x0000c51e", true},
		{"process name rejected", "pid_1234_luid_0x00000000_0x0000C51E_phys_0", "", false},
		{"missing phys", "luid_0x00000000_0x0000C51E", "", false},
		{"missing 0x prefix", "luid_00000000_0x0000C51E_phys_0", "", false},
		{"empty string", "", "", false},
	}
	for _, c := range cases {
		luid, ok := parseAdapterMemInstance(c.in)
		if luid != c.luid || ok != c.ok {
			t.Errorf("%s: parseAdapterMemInstance(%q) = (%q, %v), want (%q, %v)",
				c.name, c.in, luid, ok, c.luid, c.ok)
		}
	}
}

// ── Aggregation ──────────────────────────────────────────────────────

var pdhTestLuids = map[string]int{
	"0x00000000_0x0000c51e": 0, // adapter A
	"0x00000000_0x0000d20a": 1, // adapter B
}

func pdhTestName(pid int) string { return "proc" + strconv.Itoa(pid) }

func TestPdhProcessesFixture(t *testing.T) {
	s := readPdhFixture(t)
	procs := pdhProcesses(s, pdhTestLuids, pdhTestName)

	byPid := make(map[int]ProcessData)
	for _, p := range procs {
		byPid[p.PID] = p
	}
	if len(procs) != 3 {
		t.Fatalf("got %d processes (%v), want 3 (pid 0 and unmapped-LUID pids excluded)", len(procs), byPid)
	}
	for _, banned := range []int{0, 4444} {
		if _, found := byPid[banned]; found {
			t.Errorf("pid %d should have been skipped", banned)
		}
	}

	cases := []struct {
		pid    int
		busy   float64 // NaN = expect NaN
		vram   int64
		gpuIDs []int
	}{
		// gpu0 engtypes: 3D=30+20=50, Copy=10, High Priority Compute=5
		// → max 50 (sum-of-engtypes would wrongly give 65); gpu1 3D=0
		// adds no busy but still registers the adapter in GpuIDs
		// (fdinfo parity: presence, not activity).
		{1234, 50, 268435456, []int{0, 1}},
		// gpu0 Compute_0=40+70=110 → clamped 100; gpu1 3D=25 → 125
		// total (135 would mean the clamp is missing). VRAM sums both
		// mapped adapters; the unmapped-LUID 999999 is excluded.
		{5678, 125, 1610612736, []int{0, 1}},
		// Memory-only process: busy stays NaN.
		{9012, math.NaN(), 134217728, []int{1}},
	}
	for _, c := range cases {
		p, found := byPid[c.pid]
		if !found {
			t.Errorf("pid %d missing from result", c.pid)
			continue
		}
		if math.IsNaN(c.busy) {
			if !math.IsNaN(p.GpuBusy) {
				t.Errorf("pid %d: GpuBusy = %v, want NaN", c.pid, p.GpuBusy)
			}
		} else if p.GpuBusy != c.busy {
			t.Errorf("pid %d: GpuBusy = %v, want %v", c.pid, p.GpuBusy, c.busy)
		}
		if p.VramUsed != c.vram {
			t.Errorf("pid %d: VramUsed = %d, want %d", c.pid, p.VramUsed, c.vram)
		}
		got := append([]int(nil), p.GpuIDs...)
		sort.Ints(got)
		if len(got) != len(c.gpuIDs) {
			t.Errorf("pid %d: GpuIDs = %v, want %v", c.pid, got, c.gpuIDs)
		} else {
			for i := range got {
				if got[i] != c.gpuIDs[i] {
					t.Errorf("pid %d: GpuIDs = %v, want %v", c.pid, got, c.gpuIDs)
					break
				}
			}
		}
		if want := pdhTestName(c.pid); p.Name != want {
			t.Errorf("pid %d: Name = %q, want %q", c.pid, p.Name, want)
		}
	}
}

func TestPdhProcessesNoMappedLuids(t *testing.T) {
	s := readPdhFixture(t)
	if got := pdhProcesses(s, nil, pdhTestName); got != nil {
		t.Errorf("nil luid map: got %v, want nil", got)
	}
	only := map[string]int{"0x00000000_0x00009999": 3}
	if got := pdhProcesses(s, only, pdhTestName); len(got) != 0 {
		t.Errorf("unmatched luid map: got %v, want none", got)
	}
}

func TestPdhAdapterUse(t *testing.T) {
	s := readPdhFixture(t)
	// Adapter A engtype sums across pids (pid 0 included): 3D=62.5,
	// Copy=10, High Priority Compute=5, Compute_0=110 → max 110 → 100.
	if got := pdhAdapterUse(s, "0x00000000_0x0000c51e"); got != 100 {
		t.Errorf("adapter A use = %v, want 100 (clamped)", got)
	}
	// Adapter B: 3D = 0 + 25 = 25.
	if got := pdhAdapterUse(s, "0x00000000_0x0000d20a"); got != 25 {
		t.Errorf("adapter B use = %v, want 25", got)
	}
	if got := pdhAdapterUse(s, "0x00000000_0x0000beef"); !math.IsNaN(got) {
		t.Errorf("unknown adapter use = %v, want NaN", got)
	}
}

func TestPdhAdapterVram(t *testing.T) {
	s := readPdhFixture(t)
	if got := pdhAdapterVram(s, "0x00000000_0x0000c51e"); got != 3221225472 {
		t.Errorf("adapter A vram = %v, want 3221225472", got)
	}
	if got := pdhAdapterVram(s, "0x00000000_0x0000d20a"); got != 1610612736 {
		t.Errorf("adapter B vram = %v, want 1610612736", got)
	}
	if got := pdhAdapterVram(s, "0x00000000_0x0000beef"); !math.IsNaN(got) {
		t.Errorf("unknown adapter vram = %v, want NaN", got)
	}
}
