package main

// amd-smi support for the rocm backend. rocm-smi is deprecated in ROCm 6.x+
// and some installs ship only amd-smi; when rocm-smi is absent the rocm
// backend runs on the runners and parsers in this file instead (discovery/
// identity, per-tick process listing, one-time static info). Parsers are
// pure functions over captured JSON so they are fixture-testable on GPU-less
// CI; runners are thin exec wrappers.

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

// newAmdSMITool wires the amd-smi runners+parsers into the amdTool role set
// consumed by rocmBackend. There is no amd-smi equivalent of the legacy
// whole-tick rocm-smi metrics pass here, so the sysfs-less fallback degrades
// to identity + processes (metrics stay at their "unavailable" sentinels).
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
			return amdSmiDiscover(), collectAmdSmiProcesses()
		},
	}
}

// ── Runner ───────────────────────────────────────────────────────────

func runAmdSmiJSON(args ...string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := append(append([]string{}, args...), "--json")
	cmd := exec.CommandContext(ctx, amdSMI, full...)
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
