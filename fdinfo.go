package main

// DRM fdinfo per-process GPU statistics.
//
// The kernel exposes per-client GPU usage in /proc/<pid>/fdinfo/<fd> for
// every open DRM file descriptor (Documentation/gpu/drm-usage-stats.rst).
// This is the primary AMD process source: unlike "rocm-smi --showpids" it
// needs no exec, and it also sees GRAPHICS clients (desktop apps), not just
// KFD compute processes.
//
// Discovery follows the nvtop approach: readlink every /proc/<pid>/fd/*
// (cheap) and read fdinfo only for descriptors that point at /dev/dri/*.
// Processes owned by other users fail the readlink with EACCES and are
// silently skipped — the rocm backend then falls back to --showpids.
//
// KFD caveat: for compute processes the amdgpu fdinfo accounts a BO in
// every GPU VM it is mapped into. With XGMI peer mappings (e.g. vLLM
// tensor parallel) each worker's model weights appear at full size on all
// peer GPUs' clients, inflating the per-PID sum several-fold. The KFD proc
// sysfs (/sys/class/kfd/kfd/proc/<pid>/vram_<gpuid>) publishes the true
// per-device allocation, so when it reports any VRAM for a PID it
// overrides the fdinfo aggregate. Graphics-only clients have no KFD entry
// and keep their fdinfo numbers.

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fdinfoClient is the parsed form of one amdgpu fdinfo blob.
type fdinfoClient struct {
	ClientID  string
	Pdev      string // normalized DDDD:BB:DD.F
	VramBytes int64
	GttBytes  int64
	EngineNs  map[string]uint64 // drm-engine-<name> busy nanoseconds; nil when absent
}

// parseFdinfoMem parses a fdinfo memory value such as "28899092 KiB",
// "112 MiB" or "0" into bytes. Returns -1 when absent or malformed.
func parseFdinfoMem(val string) int64 {
	f := strings.Fields(val)
	if len(f) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	mult := int64(1)
	if len(f) > 1 {
		switch f[1] {
		case "KiB":
			mult = 1 << 10
		case "MiB":
			mult = 1 << 20
		case "GiB":
			mult = 1 << 30
		}
	}
	return n * mult
}

// parseFdinfo parses one /proc/<pid>/fdinfo/<fd> blob. It accepts both
// "key:\tvalue" and "key: value" separators (real kernels emit both, even
// within one file). Returns ok=false unless the fd belongs to the amdgpu
// driver and carries a drm-client-id.
func parseFdinfo(content string) (fdinfoClient, bool) {
	var c fdinfoClient
	driver := ""
	vram, memVram, gtt, memGtt := int64(-1), int64(-1), int64(-1), int64(-1)

	for _, line := range strings.Split(content, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "drm-driver":
			driver = val
		case "drm-client-id":
			c.ClientID = val
		case "drm-pdev":
			c.Pdev = normalizePCI(val)
		case "drm-total-vram":
			vram = parseFdinfoMem(val)
		case "drm-memory-vram":
			memVram = parseFdinfoMem(val)
		case "drm-total-gtt":
			gtt = parseFdinfoMem(val)
		case "drm-memory-gtt":
			memGtt = parseFdinfoMem(val)
		default:
			name, isEngine := strings.CutPrefix(key, "drm-engine-")
			if !isEngine || strings.HasPrefix(name, "capacity-") {
				continue
			}
			f := strings.Fields(val) // "123456789 ns"
			if len(f) == 0 {
				continue
			}
			ns, err := strconv.ParseUint(f[0], 10, 64)
			if err != nil {
				continue
			}
			if c.EngineNs == nil {
				c.EngineNs = make(map[string]uint64)
			}
			c.EngineNs[name] = ns
		}
	}

	if driver != "amdgpu" || c.ClientID == "" {
		return fdinfoClient{}, false
	}
	// drm-total-* is current; drm-memory-* is its legacy alias.
	c.VramBytes = firstMem(vram, memVram)
	c.GttBytes = firstMem(gtt, memGtt)
	return c, true
}

func firstMem(primary, fallback int64) int64 {
	if primary >= 0 {
		return primary
	}
	if fallback >= 0 {
		return fallback
	}
	return 0
}

// scanDrmClients finds processes holding /dev/dri/* or /dev/kfd descriptors
// and parses the fdinfo of their DRM fds. The map contains every candidate
// PID (a KFD-only PID maps to an empty slice); clients are deduped by
// drm-client-id so dup()ed descriptors are counted once. Permission errors
// (other users' processes) are silently skipped.
func scanDrmClients(procRoot string) map[int][]fdinfoClient {
	out := make(map[int][]fdinfoClient)
	procDirs, _ := filepath.Glob(procRoot + "/[0-9]*")
	for _, pd := range procDirs {
		pid, err := strconv.Atoi(filepath.Base(pd))
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(pd + "/fd")
		if err != nil {
			continue // permission denied or process gone
		}
		hasDrm := false
		seen := make(map[string]bool)
		var clients []fdinfoClient
		for _, fd := range fds {
			link, err := os.Readlink(pd + "/fd/" + fd.Name())
			if err != nil {
				continue
			}
			if link == "/dev/kfd" {
				hasDrm = true
				continue
			}
			if !strings.HasPrefix(link, "/dev/dri/") {
				continue
			}
			hasDrm = true
			data, err := os.ReadFile(pd + "/fdinfo/" + fd.Name())
			if err != nil {
				continue
			}
			c, ok := parseFdinfo(string(data))
			if !ok || seen[c.ClientID] {
				continue
			}
			seen[c.ClientID] = true
			clients = append(clients, c)
		}
		if hasDrm {
			out[pid] = clients
		}
	}
	return out
}

// ── KFD per-process VRAM (fdinfo peer-mapping correction) ───────────

// kfdGpuPdevMap builds gpu_id → normalized PCI address from the KFD
// topology (location_id encodes bus<<8 | device<<3 | function).
func kfdGpuPdevMap(nodesDir string) map[string]string {
	out := make(map[string]string)
	nodes, _ := filepath.Glob(nodesDir + "/[0-9]*")
	for _, n := range nodes {
		gid := readStringFile(n + "/gpu_id")
		if gid == "" || gid == "0" { // node 0 is the CPU
			continue
		}
		var domain, loc int64 = 0, -1
		for _, line := range strings.Split(readStringFile(n+"/properties"), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				continue
			}
			switch f[0] {
			case "location_id":
				loc = v
			case "domain":
				domain = v
			}
		}
		if loc < 0 {
			continue
		}
		out[gid] = normalizePCI(
			fmtBDF(domain, (loc>>8)&0xff, (loc>>3)&0x1f, loc&0x7))
	}
	return out
}

func fmtBDF(domain, bus, dev, fn int64) string {
	hex := func(v, width int64) string {
		s := strconv.FormatInt(v, 16)
		for int64(len(s)) < width {
			s = "0" + s
		}
		return s
	}
	return hex(domain, 4) + ":" + hex(bus, 2) + ":" + hex(dev, 2) + "." + hex(fn, 1)
}

// readKfdProcVram sums /sys/class/kfd/kfd/proc/<pid>/vram_<gpuid> over the
// GPUs known to this backend. Returns total bytes and the set of GPU
// indices with nonzero usage; total 0 means "no KFD data" (graphics-only
// client or KFD proc entry absent).
func readKfdProcVram(kfdProcRoot string, pid int, gpuPdev map[string]string, pdevToGpu map[string]int) (int64, map[int]bool) {
	matches, _ := filepath.Glob(kfdProcRoot + "/" + strconv.Itoa(pid) + "/vram_*")
	var total int64
	gpus := make(map[int]bool)
	for _, m := range matches {
		gid := strings.TrimPrefix(filepath.Base(m), "vram_")
		pdev, ok := gpuPdev[gid]
		if !ok {
			continue
		}
		gpuIdx, ok := pdevToGpu[pdev]
		if !ok {
			continue
		}
		if v := readInt64File(m, 0); v > 0 {
			total += v
			gpus[gpuIdx] = true
		}
	}
	return total, gpus
}

// ── Stateful collector ───────────────────────────────────────────────

// engineSample remembers one client's engine busy counters for rate
// computation across ticks (same pattern as the PCIe byte-counter state).
type engineSample struct {
	engines map[string]uint64
	t       time.Time
}

// fdinfoCollector turns raw fdinfo scans into ProcessData. Each AMD
// backend owns one instance; prev holds per-"pid:client-id" engine
// counters between ticks.
type fdinfoCollector struct {
	procRoot    string // "/proc"
	kfdProcRoot string // "/sys/class/kfd/kfd/proc"
	kfdTopoRoot string // "/sys/class/kfd/kfd/topology/nodes"

	mu   sync.Mutex
	prev map[string]engineSample
}

func newFdinfoCollector() *fdinfoCollector {
	return &fdinfoCollector{
		procRoot:    "/proc",
		kfdProcRoot: "/sys/class/kfd/kfd/proc",
		kfdTopoRoot: "/sys/class/kfd/kfd/topology/nodes",
	}
}

// maxEngineRatio returns the busiest engine's Δbusy-ns/Δwall ratio between
// two samples, or NaN when no engine is comparable.
func maxEngineRatio(prevE, curE map[string]uint64, dt float64) float64 {
	best := math.NaN()
	for name, cur := range curE {
		p, ok := prevE[name]
		if !ok || cur < p {
			continue
		}
		r := float64(cur-p) / 1e9 / dt
		if math.IsNaN(best) || r > best {
			best = r
		}
	}
	return best
}

// collect scans /proc once and returns one ProcessData per GPU-using
// process, restricted to the GPUs in pdevToGpu (normalized PCI address →
// GPU index). VRAM is aggregated per PID across that backend's GPUs;
// clients on unknown pdevs (another backend's GPUs) are ignored so
// multi-backend systems compose via mergeProcesses. GpuBusy is NaN unless
// drm-engine-* counters were seen in two consecutive scans.
func (f *fdinfoCollector) collect(pdevToGpu map[string]int, nameFn func(int) string) []ProcessData {
	if f == nil || len(pdevToGpu) == 0 {
		return nil // zero-value backends (tests) and cardless collections
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	pidClients := scanDrmClients(f.procRoot)
	gpuPdev := kfdGpuPdevMap(f.kfdTopoRoot)
	next := make(map[string]engineSample)

	var procs []ProcessData
	for pid, clients := range pidClients {
		var vram int64
		gpus := make(map[int]bool)
		busy := math.NaN()

		for _, c := range clients {
			gpuID, ok := pdevToGpu[c.Pdev]
			if !ok {
				continue
			}
			vram += c.VramBytes
			gpus[gpuID] = true

			if len(c.EngineNs) == 0 {
				continue
			}
			key := strconv.Itoa(pid) + ":" + c.ClientID
			next[key] = engineSample{engines: c.EngineNs, t: now}
			p, ok := f.prev[key]
			if !ok {
				continue
			}
			dt := now.Sub(p.t).Seconds()
			if dt <= 0 {
				continue
			}
			if r := maxEngineRatio(p.engines, c.EngineNs, dt); !math.IsNaN(r) {
				if math.IsNaN(busy) {
					busy = 0
				}
				busy += math.Min(r, 1) * 100
			}
		}

		// KFD compute processes: prefer the exact per-device accounting
		// (fdinfo double-counts XGMI peer mappings, see file comment).
		if kfdVram, kfdGpus := readKfdProcVram(f.kfdProcRoot, pid, gpuPdev, pdevToGpu); kfdVram > 0 {
			vram = kfdVram
			gpus = kfdGpus
		}

		if len(gpus) == 0 {
			continue
		}
		ids := make([]int, 0, len(gpus))
		for id := range gpus {
			ids = append(ids, id)
		}
		procs = append(procs, ProcessData{
			PID:      pid,
			Name:     nameFn(pid),
			GpuIDs:   ids,
			VramUsed: vram,
			GpuBusy:  busy,
		})
	}

	f.prev = next
	return procs
}
