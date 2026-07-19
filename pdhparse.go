package main

// PDH GPU counter instance parsing and aggregation. Untagged so the
// logic stays testable on Linux; only pdh_windows.go feeds it real data.
//
// Windows exposes per-process GPU usage through PDH counter sets whose
// instance names encode pid, adapter LUID and engine type:
//
//	GPU Engine:         pid_1234_luid_0x00000000_0x0000C51E_phys_0_eng_3_engtype_3D
//	GPU Process Memory: pid_1234_luid_0x00000000_0x0000C51E_phys_0
//	GPU Adapter Memory: luid_0x00000000_0x0000C51E_phys_0
//
// The engtype suffix is driver-defined and kept opaque: drivers emit
// "3D", "Copy", "Compute_0", "VideoDecode" and even "High Priority
// Compute" — underscores and spaces included — so it is everything
// after the first "_engtype_" marker. LUIDs are the two hex tokens
// joined with "_" and lowercased; instance names vary in hex casing
// between counter sets, so all comparisons happen on the folded form.

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// ── Sample types ─────────────────────────────────────────────────────

// pdhInstance is one formatted counter value keyed by its PDH instance
// name.
type pdhInstance struct {
	Name string
	Val  float64
}

// pdhSample holds one tick's formatted counter arrays for the three GPU
// counter sets the wddm backend queries.
type pdhSample struct {
	Engine     []pdhInstance // \GPU Engine(*)\Utilization Percentage
	ProcMem    []pdhInstance // \GPU Process Memory(*)\Dedicated Usage
	AdapterMem []pdhInstance // \GPU Adapter Memory(*)\Dedicated Usage
}

// ── Instance-name parsing ────────────────────────────────────────────

// parsePidLuidTokens validates the shared "pid_<P>_luid_0x<HI>_0x<LO>_
// phys_<n>" prefix (already split on "_") and returns the pid and the
// lowercased LUID.
func parsePidLuidTokens(tok []string) (pid int, luid string, ok bool) {
	if len(tok) < 7 || tok[0] != "pid" || tok[2] != "luid" || tok[5] != "phys" {
		return 0, "", false
	}
	pid, err := strconv.Atoi(tok[1])
	if err != nil || pid < 0 {
		return 0, "", false
	}
	if !strings.HasPrefix(tok[3], "0x") || !strings.HasPrefix(tok[4], "0x") {
		return 0, "", false
	}
	return pid, strings.ToLower(tok[3] + "_" + tok[4]), true
}

// parseEngineInstance splits a GPU Engine instance name into pid, LUID
// and opaque engine type. Malformed names return ok=false.
func parseEngineInstance(name string) (pid int, luid, engtype string, ok bool) {
	const marker = "_engtype_"
	i := strings.Index(name, marker)
	if i < 0 {
		return 0, "", "", false
	}
	engtype = name[i+len(marker):]
	tok := strings.Split(name[:i], "_")
	if engtype == "" || len(tok) != 9 || tok[7] != "eng" {
		return 0, "", "", false
	}
	pid, luid, ok = parsePidLuidTokens(tok)
	if !ok {
		return 0, "", "", false
	}
	return pid, luid, engtype, true
}

// parseMemInstance splits a GPU Process Memory instance name
// ("pid_<P>_luid_<HI>_<LO>_phys_<n>") into pid and lowercased LUID.
func parseMemInstance(name string) (pid int, luid string, ok bool) {
	tok := strings.Split(name, "_")
	if len(tok) != 7 {
		return 0, "", false
	}
	return parsePidLuidTokens(tok)
}

// parseAdapterMemInstance splits a GPU Adapter Memory instance name
// ("luid_<HI>_<LO>_phys_<n>") into the lowercased LUID.
func parseAdapterMemInstance(name string) (luid string, ok bool) {
	tok := strings.Split(name, "_")
	if len(tok) != 5 || tok[0] != "luid" || tok[3] != "phys" {
		return "", false
	}
	if !strings.HasPrefix(tok[1], "0x") || !strings.HasPrefix(tok[2], "0x") {
		return "", false
	}
	return strings.ToLower(tok[1] + "_" + tok[2]), true
}

// ── Aggregation ──────────────────────────────────────────────────────

// pdhProcesses turns one sample into ProcessData rows, restricted to
// the adapters in luidToGpu (lowercased LUID → GPU index). It mirrors
// the fdinfo collector's semantics: per (pid, LUID) the utilization of
// each engine type is summed, the busiest engine type wins, that value
// is clamped to 100, and the per-adapter results are summed into one
// GpuBusy figure. VramUsed sums Dedicated Usage across mapped adapters.
// GpuBusy is NaN for processes that only appear in the memory counter
// set. pid 0 (idle/system accounting) is skipped; like the fdinfo
// collector — which lists a GPU for every open DRM client — every
// mapped adapter a process appears on is recorded in GpuIDs, even at
// zero utilization and zero memory, so idle-but-attached processes
// don't flicker out of the table.
func pdhProcesses(s pdhSample, luidToGpu map[string]int, nameFn func(int) string) []ProcessData {
	if len(luidToGpu) == 0 {
		return nil
	}

	// Per (pid, LUID): engine-type → summed utilization across eng_<n>.
	type pidLuid struct {
		pid  int
		luid string
	}
	engSums := make(map[pidLuid]map[string]float64)
	for _, inst := range s.Engine {
		pid, luid, engtype, ok := parseEngineInstance(inst.Name)
		if !ok || pid == 0 {
			continue
		}
		if _, mapped := luidToGpu[luid]; !mapped {
			continue
		}
		key := pidLuid{pid, luid}
		m := engSums[key]
		if m == nil {
			m = make(map[string]float64)
			engSums[key] = m
		}
		m[engtype] += inst.Val
	}

	type pidAgg struct {
		busy float64 // NaN until an engine sample contributes
		vram int64
		gpus map[int]bool
	}
	byPid := make(map[int]*pidAgg)
	agg := func(pid int) *pidAgg {
		a := byPid[pid]
		if a == nil {
			a = &pidAgg{busy: math.NaN(), gpus: make(map[int]bool)}
			byPid[pid] = a
		}
		return a
	}

	for key, engs := range engSums {
		var top float64
		for _, v := range engs {
			if v > top {
				top = v
			}
		}
		top = math.Min(top, 100)
		a := agg(key.pid)
		if math.IsNaN(a.busy) {
			a.busy = 0
		}
		a.busy += top
		a.gpus[luidToGpu[key.luid]] = true
	}

	for _, inst := range s.ProcMem {
		pid, luid, ok := parseMemInstance(inst.Name)
		if !ok || pid == 0 {
			continue
		}
		gpuID, mapped := luidToGpu[luid]
		if !mapped {
			continue
		}
		a := agg(pid)
		a.vram += int64(inst.Val)
		a.gpus[gpuID] = true
	}

	procs := make([]ProcessData, 0, len(byPid))
	for pid, a := range byPid {
		if len(a.gpus) == 0 {
			continue
		}
		ids := make([]int, 0, len(a.gpus))
		for id := range a.gpus {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		procs = append(procs, ProcessData{
			PID:      pid,
			Name:     nameFn(pid),
			GpuIDs:   ids,
			VramUsed: a.vram,
			GpuBusy:  a.busy,
		})
	}
	return procs
}

// pdhAdapterUse computes one adapter's utilization the way Task Manager
// does: sum each engine type across all pids (pid 0 included) for the
// given lowercased LUID, take the busiest engine type, clamp to 100.
// NaN when the sample has no engine instances for that adapter.
func pdhAdapterUse(s pdhSample, luid string) float64 {
	sums := make(map[string]float64)
	seen := false
	for _, inst := range s.Engine {
		_, iluid, engtype, ok := parseEngineInstance(inst.Name)
		if !ok || iluid != luid {
			continue
		}
		sums[engtype] += inst.Val
		seen = true
	}
	if !seen {
		return math.NaN()
	}
	var top float64
	for _, v := range sums {
		if v > top {
			top = v
		}
	}
	return math.Min(top, 100)
}

// pdhAdapterVram sums the adapter's Dedicated Usage bytes for the given
// lowercased LUID. NaN when the sample has no instances for it.
func pdhAdapterVram(s pdhSample, luid string) float64 {
	total := math.NaN()
	for _, inst := range s.AdapterMem {
		iluid, ok := parseAdapterMemInstance(inst.Name)
		if !ok || iluid != luid {
			continue
		}
		if math.IsNaN(total) {
			total = 0
		}
		total += inst.Val
	}
	return total
}
