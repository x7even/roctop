package main

// amd-smi support for the rocm backend. rocm-smi is deprecated in ROCm 6.x+
// and some installs ship only amd-smi; when rocm-smi is absent the rocm
// backend runs on the runners and parsers in this file instead (discovery/
// identity, per-tick process listing, a whole-tick "amd-smi metric" pass,
// one-time static info). Parsers are pure functions over captured JSON so
// they are fixture-testable on GPU-less CI; runners are thin exec wrappers.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const amdSMI = "amd-smi"

// amdSmiCmd is the command used to exec amd-smi. selectAmdTool overwrites it
// with the absolute path findAmdSmi resolved (required on Windows, where
// amd-smi may live outside PATH and Go 1.19+ refuses relative-path execs).
var amdSmiCmd = amdSMI

// newAmdSMITool wires the amd-smi runners+parsers into the amdTool role set
// consumed by rocmBackend. The sysfs-less fallback path (Windows, or Linux
// installs where the amdgpu sysfs mapping failed) runs a whole-tick
// "amd-smi metric" pass on top of discovery, mirroring the legacy rocm-smi
// metrics collector.
//
// RAS/ECC totals come from "amd-smi metric --ecc"; the RAS block breakdown
// that "rocm-smi --showrasinfo all" provides has no direct amd-smi JSON
// equivalent ("amd-smi static" only reports block enable state, not counts),
// so only the totals are populated.
func newAmdSMITool() amdTool {
	return amdTool{
		name:       amdSMI,
		discover:   amdSmiDiscover,
		processes:  collectAmdSmiProcesses,
		staticInfo: amdSmiStaticInfo,
		collectFull: func() ([]GpuData, []ProcessData) {
			gpus := amdSmiDiscover()
			applyAmdSmiMetrics(runAmdSmiJSON("metric"), gpus)
			return gpus, collectAmdSmiProcesses()
		},
	}
}

// ── Runner ───────────────────────────────────────────────────────────

func runAmdSmiJSON(args ...string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := append(append([]string{}, args...), "--json")
	cmd := exec.CommandContext(ctx, amdSmiCmd, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		logf("amd-smi %s: %s", strings.Join(args, " "), detail)
		return nil
	}
	return out
}

// ── Generic value helpers ────────────────────────────────────────────
//
// amd-smi JSON values vary across versions: a field may be a plain number,
// a string with a unit suffix ("32 GT/s"), an "N/A" placeholder, or a
// nested {"value": N, "unit": "MB"} object. All helpers parse defensively.

// amdSmiFloat extracts a numeric value from any of the shapes above,
// returning the value, its unit ("" when none), and whether a number was
// found.
func amdSmiFloat(v interface{}) (val float64, unit string, ok bool) {
	switch t := v.(type) {
	case float64:
		return t, "", true
	case json.Number:
		f, err := t.Float64()
		return f, "", err == nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" || strings.EqualFold(s, "N/A") {
			return 0, "", false
		}
		fields := strings.Fields(s)
		f, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, "", false
		}
		if len(fields) > 1 {
			unit = fields[1]
		}
		return f, unit, true
	case map[string]interface{}:
		inner, exists := t["value"]
		if !exists {
			return 0, "", false
		}
		f, innerUnit, innerOK := amdSmiFloat(inner)
		if !innerOK {
			return 0, "", false
		}
		if u, isStr := t["unit"].(string); isStr && !strings.EqualFold(u, "N/A") {
			innerUnit = strings.TrimSpace(u)
		}
		return f, innerUnit, true
	}
	return 0, "", false
}

// amdSmiBytes converts a numeric field carrying a memory unit to bytes.
func amdSmiBytes(v interface{}) (int64, bool) {
	f, unit, ok := amdSmiFloat(v)
	if !ok {
		return 0, false
	}
	var mult float64
	switch strings.ToUpper(unit) {
	case "", "B":
		mult = 1
	case "KB", "KIB":
		mult = 1 << 10
	case "MB", "MIB":
		mult = 1 << 20
	case "GB", "GIB":
		mult = 1 << 30
	case "TB", "TIB":
		mult = 1 << 40
	default:
		return 0, false
	}
	return int64(f * mult), true
}

// amdSmiMap returns d[key] as a map, or nil when absent or of another type.
func amdSmiMap(d map[string]interface{}, key string) map[string]interface{} {
	if d == nil {
		return nil
	}
	m, _ := d[key].(map[string]interface{})
	return m
}

// amdSmiStr returns d[key] as a trimmed string, mapping absent values and
// the "N/A" placeholder to "".
func amdSmiStr(d map[string]interface{}, key string) string {
	if d == nil {
		return ""
	}
	v, exists := d[key]
	if !exists {
		return ""
	}
	s, isStr := v.(string)
	if !isStr {
		s = fmt.Sprintf("%v", v)
	}
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "N/A") {
		return ""
	}
	return s
}

// parseAmdSmiEntries normalizes an amd-smi JSON payload to a slice of
// per-GPU maps. Depending on subcommand and version the top level is either
// an array of per-GPU objects ("amd-smi process", "amd-smi list") or an
// object wrapping that array in "gpu_data" ("amd-smi static", "amd-smi
// metric").
func parseAmdSmiEntries(raw []byte) []map[string]interface{} {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}

	var list []interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		var obj map[string]interface{}
		if err := json.Unmarshal(raw, &obj); err != nil {
			logf("amd-smi: JSON parse error: %s", err.Error())
			return nil
		}
		list, _ = obj["gpu_data"].([]interface{})
	}

	entries := make([]map[string]interface{}, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]interface{}); ok {
			entries = append(entries, m)
		}
	}
	return entries
}

// amdSmiGpuID returns the "gpu" index of an entry, or -1 when absent.
func amdSmiGpuID(entry map[string]interface{}) int {
	if f, _, ok := amdSmiFloat(entry["gpu"]); ok {
		return int(f)
	}
	return -1
}

// ── Discovery / identity ─────────────────────────────────────────────

// amdSmiDiscover enumerates GPUs via "amd-smi static --json". As with the
// rocm-smi discovery pass, only identity fields are populated; per-tick
// metrics come from sysfs.
func amdSmiDiscover() []GpuData {
	return parseAmdSmiStatic(runAmdSmiJSON("static"))
}

// reAmdSmiSKUPart extracts the SKU from a VBIOS part number: rocm-smi
// derives "APM107573" from "113-APM107573-100" the same way.
var reAmdSmiSKUPart = regexp.MustCompile(`^\d+-(.+?)(?:-\d+)?$`)

func parseAmdSmiStatic(raw []byte) []GpuData {
	entries := parseAmdSmiEntries(raw)
	gpus := make([]GpuData, 0, len(entries))
	for i, e := range entries {
		id := amdSmiGpuID(e)
		if id < 0 {
			id = i
		}
		g := newGpuData(id, "rocm")

		asic := amdSmiMap(e, "asic")
		g.Name = amdSmiStr(asic, "market_name")
		if g.Name == "" {
			g.Name = fmt.Sprintf("GPU %d", id)
		}
		g.Vendor = amdSmiStr(asic, "vendor_name")
		g.GfxVersion = amdSmiStr(asic, "target_graphics_version")
		g.PcieBus = amdSmiStr(amdSmiMap(e, "bus"), "bdf")

		if size, ok := amdSmiBytes(amdSmiMap(e, "vram")["size"]); ok {
			g.VramTotal = size
		}
		if max, _, ok := amdSmiFloat(amdSmiPowerLimit(amdSmiMap(e, "limit"))); ok {
			g.PowerMax = max
		}

		if part := amdSmiFirmwarePart(e); part != "" {
			if m := reAmdSmiSKUPart.FindStringSubmatch(part); m != nil {
				g.SKU = m[1]
			}
		}

		gpus = append(gpus, g)
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i].CardID < gpus[j].CardID })
	return gpus
}

// amdSmiPowerLimit returns the board max power limit value from a static
// "limit" block: flat "max_power" on some versions, nested under "ppt0" as
// "max_power_limit" on amd-smi 26.x.
func amdSmiPowerLimit(limit map[string]interface{}) interface{} {
	if limit == nil {
		return nil
	}
	if v, exists := limit["max_power"]; exists {
		return v
	}
	if v, exists := limit["max_power_limit"]; exists {
		return v
	}
	return amdSmiMap(limit, "ppt0")["max_power_limit"]
}

// amdSmiFirmwarePart returns the VBIOS part number of a static entry. Newer
// amd-smi versions report the firmware block as "ifwi", older ones as
// "vbios"; both carry name/build_date/part_number/version.
func amdSmiFirmwarePart(entry map[string]interface{}) string {
	for _, key := range []string{"vbios", "ifwi"} {
		if fw := amdSmiMap(entry, key); fw != nil {
			if part := amdSmiStr(fw, "part_number"); part != "" {
				return part
			}
			if v := amdSmiStr(fw, "version"); v != "" {
				return v
			}
		}
	}
	return ""
}

// ── Static info ──────────────────────────────────────────────────────

// amdSmiStaticInfo fills one-time static fields for rocm-backend GPUs from
// "amd-smi static --json" plus ECC error totals from "amd-smi metric --ecc".
func amdSmiStaticInfo(gpus []GpuData) {
	applyAmdSmiStatic(runAmdSmiJSON("static"), gpus)
	applyAmdSmiEcc(runAmdSmiJSON("metric", "--ecc"), gpus)
}

// amdSmiGpuIndex maps an amd-smi per-GPU entry to its index in gpus,
// preferring the PCI bus address (the invariant across tools) and falling
// back to the amd-smi GPU index against CardID. Only rocm-backend GPUs are
// candidates: other backends can share the same CardID integers. Returns -1
// when unmatched.
func amdSmiGpuIndex(entry map[string]interface{}, gpus []GpuData) int {
	if bdf := normalizePCI(amdSmiStr(amdSmiMap(entry, "bus"), "bdf")); bdf != "" {
		for i, g := range gpus {
			if g.Backend == "rocm" && normalizePCI(g.PcieBus) == bdf {
				return i
			}
		}
	}
	if id := amdSmiGpuID(entry); id >= 0 {
		for i, g := range gpus {
			if g.Backend == "rocm" && g.CardID == id {
				return i
			}
		}
	}
	return -1
}

func applyAmdSmiStatic(raw []byte, gpus []GpuData) {
	for _, e := range parseAmdSmiEntries(raw) {
		idx := amdSmiGpuIndex(e, gpus)
		if idx < 0 {
			continue
		}

		gpus[idx].Vbios = amdSmiFirmwarePart(e)
		// Lowercased to match rocm-smi's reporting of the same fields
		// ("samsung", "0x64ac...").
		gpus[idx].MemVendor = strings.ToLower(amdSmiStr(amdSmiMap(e, "vram"), "vendor"))
		gpus[idx].UniqueID = strings.ToLower(amdSmiStr(amdSmiMap(e, "asic"), "asic_serial"))
		gpus[idx].DriverVersion = amdSmiStr(amdSmiMap(e, "driver"), "version")
	}
}

func applyAmdSmiEcc(raw []byte, gpus []GpuData) {
	for _, e := range parseAmdSmiEntries(raw) {
		idx := amdSmiGpuIndex(e, gpus)
		if idx < 0 {
			continue
		}
		ecc := amdSmiMap(e, "ecc")
		if ecc == nil {
			continue
		}
		if v, _, ok := amdSmiFloat(ecc["total_correctable_count"]); ok {
			gpus[idx].RasCorrectable = int64(v)
		}
		if v, _, ok := amdSmiFloat(ecc["total_uncorrectable_count"]); ok {
			gpus[idx].RasUncorrectable = int64(v)
		}
	}
}

// ── Metrics ──────────────────────────────────────────────────────────

// applyAmdSmiMetrics fills the per-tick dynamic metrics of rocm-backend GPUs
// from an "amd-smi metric --json" payload. It backs the paths that cannot
// read amdgpu sysfs: Windows, and Linux installs where the sysfs mapping
// failed. Every assignment is guarded by its helper's ok flag, so fields the
// tool reports as "N/A" (or omits) keep their "unavailable" sentinels.
func applyAmdSmiMetrics(raw []byte, gpus []GpuData) {
	for _, e := range parseAmdSmiEntries(raw) {
		idx := amdSmiGpuIndex(e, gpus)
		if idx < 0 {
			continue
		}
		g := &gpus[idx]

		usage := amdSmiMap(e, "usage")
		if v, _, ok := amdSmiFloat(usage["gfx_activity"]); ok {
			g.GpuUse = v
		}
		if v, _, ok := amdSmiFloat(usage["mem_activity"]); ok {
			g.MemActivity = v
		}
		if v, _, ok := amdSmiFloat(usage["umc_activity"]); ok {
			g.UmcActivity = v
		}

		temperature := amdSmiMap(e, "temperature")
		if v, _, ok := amdSmiFloat(temperature["edge"]); ok {
			g.TempEdge = v
		}
		if v, _, ok := amdSmiFloat(temperature["hotspot"]); ok {
			g.TempJunc = v
		}
		if v, _, ok := amdSmiFloat(temperature["mem"]); ok {
			g.TempMem = v
		}

		power := amdSmiMap(e, "power")
		if v, _, ok := amdSmiFloat(power["socket_power"]); ok {
			g.PowerAvg = v
		}
		// throttle_status is a numeric bitmask on boards that report it;
		// tools that emit "N/A" (or a string) fail amdSmiFloat and keep
		// the zero "not throttled" sentinel.
		if v, _, ok := amdSmiFloat(power["throttle_status"]); ok {
			g.ThrottleStatus = int(v)
			g.ThrottleReasons = throttleReasons(g.ThrottleStatus)
		}

		// amd-smi metric emits per-engine clock objects ("gfx_0", "mem_0",
		// "vclk_0", ...) whose current clock is under "clk". Iterate in
		// sorted key order so the first ok value per family wins
		// deterministically.
		clock := amdSmiMap(e, "clock")
		clockKeys := make([]string, 0, len(clock))
		for k := range clock {
			clockKeys = append(clockKeys, k)
		}
		sort.Strings(clockKeys)
		haveSclk, haveMclk := false, false
		for _, k := range clockKeys {
			mhz, ok := amdSmiClockMHz(clock[k])
			if !ok {
				continue
			}
			switch {
			case !haveSclk && strings.HasPrefix(k, "gfx"):
				g.Sclk = mhz
				haveSclk = true
			case !haveMclk && strings.HasPrefix(k, "mem"):
				g.Mclk = mhz
				haveMclk = true
			}
		}

		memUsage := amdSmiMap(e, "mem_usage")
		if total, ok := amdSmiBytes(memUsage["total_vram"]); ok {
			g.VramTotal = total
		}
		if used, ok := amdSmiBytes(memUsage["used_vram"]); ok {
			g.VramUsed = used
			// Percent only when this tick actually reported usage: an
			// "N/A" used_vram must keep the 0/0 "no data" sentinels even
			// though discovery already filled VramTotal.
			if g.VramTotal > 0 {
				g.VramPercent = float64(used) / float64(g.VramTotal) * 100
			}
		}

		fan := amdSmiMap(e, "fan")
		if v, _, ok := amdSmiFloat(fan["rpm"]); ok {
			g.FanRPM = int(v)
		}
		if v, _, ok := amdSmiFloat(fan["usage"]); ok {
			g.FanPercent = v
		} else if speed, _, okSpeed := amdSmiFloat(fan["speed"]); okSpeed {
			if max, _, okMax := amdSmiFloat(fan["max"]); okMax && max > 0 {
				g.FanPercent = speed / max * 100
			}
		}

		pcie := amdSmiMap(e, "pcie")
		if v, _, ok := amdSmiFloat(pcie["width"]); ok {
			g.PcieWidth = int(v)
		}
		if v, unit, ok := amdSmiFloat(pcie["speed"]); ok {
			// amd-smi 24.x emitted MT/s; normalize to the "%.1fGT/s"
			// format the rocm-smi path produces.
			if strings.EqualFold(unit, "MT/s") {
				v /= 1000
			}
			g.PcieSpeed = fmt.Sprintf("%.1fGT/s", v)
		}
	}
}

// amdSmiClockMHz extracts a clock value in MHz from one "amd-smi metric"
// clock entry: an object whose current clock lives under "clk" on current
// versions, or a plain number on older ones.
func amdSmiClockMHz(v interface{}) (int, bool) {
	if m, isMap := v.(map[string]interface{}); isMap {
		if clk, exists := m["clk"]; exists {
			v = clk
		}
	}
	f, unit, ok := amdSmiFloat(v)
	if !ok {
		return 0, false
	}
	if strings.EqualFold(unit, "GHz") {
		f *= 1000
	}
	return int(f), true
}

// ── Process listing ──────────────────────────────────────────────────

// collectAmdSmiProcesses runs the per-tick "amd-smi process --json" pass.
func collectAmdSmiProcesses() []ProcessData {
	return parseAmdSmiProcesses(runAmdSmiJSON("process"))
}

// parseAmdSmiProcesses parses "amd-smi process --json": an array of
// {"gpu": N, "process_list": [{"process_info": {...}}]} entries, where
// process_info is the string "No running processes detected" on idle GPUs.
//
// amd-smi 26.x lists every KFD process under every GPU with process-wide
// memory totals (not a per-GPU split), so VRAM is deduplicated by PID
// (max across listings, not summed) and GpuIDs collects each GPU index the
// process was listed under.
func parseAmdSmiProcesses(raw []byte) []ProcessData {
	byPID := make(map[int]*ProcessData)

	for _, e := range parseAmdSmiEntries(raw) {
		gpuID := amdSmiGpuID(e)
		list, ok := e["process_list"].([]interface{})
		if !ok {
			continue
		}
		for _, item := range list {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			// process_info is a string on idle GPUs; skip those.
			info, ok := m["process_info"].(map[string]interface{})
			if !ok {
				continue
			}
			pidF, _, ok := amdSmiFloat(info["pid"])
			if !ok {
				continue
			}
			pid := int(pidF)

			p := byPID[pid]
			if p == nil {
				name := amdSmiStr(info, "name")
				// amd-smi reports full executable paths; keep the basename
				// like rocm-smi's short process names.
				if i := strings.LastIndexByte(name, '/'); i >= 0 {
					name = name[i+1:]
				}
				p = &ProcessData{PID: pid, Name: name}
				byPID[pid] = p
			}

			if vram, ok := amdSmiBytes(amdSmiMap(info, "memory_usage")["vram_mem"]); ok && vram > p.VramUsed {
				p.VramUsed = vram
			}
			if gpuID >= 0 {
				seen := false
				for _, g := range p.GpuIDs {
					if g == gpuID {
						seen = true
						break
					}
				}
				if !seen {
					p.GpuIDs = append(p.GpuIDs, gpuID)
				}
			}
		}
	}

	result := make([]ProcessData, 0, len(byPID))
	for _, p := range byPID {
		sort.Ints(p.GpuIDs)
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].VramUsed != result[j].VramUsed {
			return result[i].VramUsed > result[j].VramUsed
		}
		return result[i].PID < result[j].PID
	})
	return result
}
