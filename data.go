package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxHistory = 512

// ── Backend interface ───────────────────────────────────────────────

type GpuBackend interface {
	CollectData() ([]GpuData, []ProcessData)
	Name() string
}

// ── ROCm backend ────────────────────────────────────────────────────

// rocmBackend discovers GPUs via rocm-smi once at construction, then reads
// all per-tick metrics straight from sysfs (microseconds instead of four
// rocm-smi execs). rocm-smi remains in use for process listing per tick,
// the one-time static info pass, and as a whole-tick fallback when a GPU
// cannot be mapped to a sysfs card directory.
type rocmBackend struct {
	mu        sync.Mutex // guards cards and sysfsMode; bubbletea can run overlapping collection goroutines
	cards     []rocmCard
	sysfsMode bool // true when every discovered GPU is mapped to sysfs
}

// rocmCard couples one rocm-smi GPU's identity (captured at discovery) with
// its sysfs directories.
type rocmCard struct {
	identity GpuData // static identity fields from the discovery pass
	dev      amdSysfsDev
}

func newRocmBackend() *rocmBackend {
	b := &rocmBackend{}
	b.discover()
	return b
}

// discover runs one full rocm-smi pass to enumerate GPUs and maps each to
// its /sys/class/drm/cardN/device directory via the PCI bus address.
func (r *rocmBackend) discover() {
	data := runJSON(rocmSMIFlags...)
	if data == nil {
		return
	}
	gpus, _ := parseRocmAll(data)
	cards := make([]rocmCard, 0, len(gpus))
	mapped := true
	for _, g := range gpus {
		c := rocmCard{identity: g, dev: findAmdSysfsDev(drmClassDir, g.PcieBus)}
		if c.dev.deviceDir == "" {
			mapped = false
			logf("rocm: no sysfs card dir for GPU %d (%s); using rocm-smi metrics", g.CardID, g.PcieBus)
		}
		cards = append(cards, c)
	}
	r.cards = cards
	r.sysfsMode = len(cards) > 0 && mapped
}

// sysfsOK reports whether every cached sysfs mapping is still readable.
func (r *rocmBackend) sysfsOK() bool {
	for _, c := range r.cards {
		if c.dev.deviceDir == "" {
			return false
		}
		if _, err := os.Stat(c.dev.deviceDir + "/gpu_busy_percent"); err != nil {
			return false
		}
	}
	return len(r.cards) > 0
}

func (r *rocmBackend) Name() string { return "rocm" }

const rocmSMI = "rocm-smi"

var rocmSMIFlags = []string{
	"--showuse",
	"--showmeminfo", "vram",
	"--showmemuse",
	"-t",
	"--showpower",
	"--showmaxpower",
	"--showfan",
	"--showclocks",
	"--showvoltage",
	"--showproductname",
	"--showperflevel",
	"--showbus",
	"--showpids",
}

// Throttle status bit descriptions (AMD GPU throttle bitmask)
var throttleBits = map[int]string{
	0:  "POWER_LIMIT",
	1:  "THERMAL",
	2:  "CURRENT",
	3:  "VOLTAGE",
	4:  "GPU_CON",
	5:  "SOC",
	16: "PPT0",
	17: "PPT1",
	18: "PPT2",
	19: "PPT3",
	20: "FIT",
	21: "GFX_DUTY_CYCLE",
	22: "VR_TEMP",
}

type GpuData struct {
	CardID   int
	Backend  string
	Name     string
	TempEdge float64
	TempJunc float64
	TempMem  float64

	GpuUse      float64
	MemActivity float64
	UmcActivity float64

	VramTotal   int64
	VramUsed    int64
	VramPercent float64

	GttTotal   int64
	GttUsed    int64
	GttPercent float64

	PowerAvg float64
	PowerMax float64

	FanRPM     int
	FanPercent float64

	Sclk int
	Mclk int

	Voltage float64

	ThrottleStatus  int
	ThrottleReasons []string

	PcieBus      string
	PcieSpeed    string
	PcieWidth    int
	PcieRootPort string

	Vendor        string
	SKU           string
	GfxVersion    string
	Vbios         string
	MemVendor     string
	DriverVersion string
	PerfLevel     string
	UniqueID      string

	RasCorrectable   int64
	RasUncorrectable int64

	// PCIe bandwidth — three source priority:
	//  1. rocm-smi --showbw: cumulative byte counters (TX/RX split); model diffs them.
	//  2. pcie_bw sysfs:     per-read byte deltas (TX/RX split); kernel resets on each read.
	//  3. gpu_metrics v1.4+: instantaneous combined rate; set directly as PcieTxMBps.
	PcieTxBytes   int64   // rocm-smi cumulative bytes sent; -1 = unavailable
	PcieRxBytes   int64   // rocm-smi cumulative bytes received; -1 = unavailable
	PcieBwTxDelta int64   // pcie_bw TX byte delta since last read; -1 = unavailable
	PcieBwRxDelta int64   // pcie_bw RX byte delta since last read; -1 = unavailable
	PcieTxMBps    float64 // final TX rate MB/s (NaN = unavailable; combined when PcieRxMBps NaN)
	PcieRxMBps    float64 // final RX rate MB/s (NaN = unavailable)
}

type ProcessData struct {
	PID      int
	Name     string
	GpuIDs   []int
	VramUsed int64
}

type RingBuffer struct {
	data  [maxHistory]float64
	index int
	count int
}

func (r *RingBuffer) Push(v float64) {
	r.data[r.index] = v
	r.index = (r.index + 1) % maxHistory
	if r.count < maxHistory {
		r.count++
	}
}

func (r *RingBuffer) Values() []float64 {
	if r.count == 0 {
		return nil
	}
	out := make([]float64, r.count)
	start := (r.index - r.count + maxHistory) % maxHistory
	for i := 0; i < r.count; i++ {
		out[i] = r.data[(start+i)%maxHistory]
	}
	return out
}

type GpuHistory struct {
	GpuUse     RingBuffer
	Power      RingBuffer
	TempJnc    RingBuffer
	PcieTx     RingBuffer // TX rate MB/s (or combined BW when RX unavailable)
	PcieRx     RingBuffer // RX rate MB/s (only populated when TX/RX split available)
	PcieTxPeak float64    // all-time peak TX MB/s
	PcieRxPeak float64    // all-time peak RX MB/s
	PowerPeak  float64    // all-time peak power draw (W)
}

func (g GpuData) HistKey() string {
	return g.Backend + ":" + strconv.Itoa(g.CardID)
}

func backendOrder(name string) int {
	switch name {
	case "rocm":
		return 0
	case "nvidia":
		return 1
	case "sysfs":
		return 2
	default:
		return 9
	}
}

func backendNames(backends []GpuBackend) string {
	var names []string
	for _, b := range backends {
		names = append(names, b.Name())
	}
	return strings.Join(names, "+")
}

func mergeProcesses(procs []ProcessData) []ProcessData {
	byPID := make(map[int]*ProcessData)
	for _, p := range procs {
		if existing, ok := byPID[p.PID]; ok {
			existing.VramUsed += p.VramUsed
			for _, gid := range p.GpuIDs {
				found := false
				for _, eg := range existing.GpuIDs {
					if eg == gid {
						found = true
						break
					}
				}
				if !found {
					existing.GpuIDs = append(existing.GpuIDs, gid)
				}
			}
		} else {
			copy := p
			byPID[p.PID] = &copy
		}
	}
	result := make([]ProcessData, 0, len(byPID))
	for _, p := range byPID {
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].VramUsed > result[j].VramUsed
	})
	return result
}

// normalizePCI extracts and lowercases the DDDD:BB:DD.F portion for
// reliable comparison. rocm-smi returns uppercase hex (e.g. "0000:C3:00.0")
// while sysfs uevent uses lowercase, so case folding is required.
func normalizePCI(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	m := reBDF.FindString(addr)
	if m != "" {
		return strings.ToLower(m)
	}
	return ""
}

// ── Parsing helpers ──────────────────────────────────────────────────

var reNonNumeric = regexp.MustCompile(`[^\d.\-]`)
var reMhz = regexp.MustCompile(`(?i)(\d+)\s*mhz`)
var reBDF = regexp.MustCompile(`[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]`)

func parseFloat(s string, def float64) float64 {
	cleaned := reNonNumeric.ReplaceAllString(s, "")
	if cleaned == "" {
		return def
	}
	v, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return def
	}
	return v
}

func parseInt(s string, def int) int {
	return int(parseFloat(s, float64(def)))
}

func parseInt64(s string, def int64) int64 {
	return int64(parseFloat(s, float64(def)))
}

func parseMHz(s string) int {
	m := reMhz.FindStringSubmatch(s)
	if m != nil {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	return parseInt(s, 0)
}

func throttleReasons(status int) []string {
	if status == 0 {
		return nil
	}
	var reasons []string
	for bit, name := range throttleBits {
		if status&(1<<bit) != 0 {
			reasons = append(reasons, name)
		}
	}
	sort.Strings(reasons)
	return reasons
}

func getString(d map[string]interface{}, key string) string {
	if v, ok := d[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// ── JSON runner ──────────────────────────────────────────────────────

func runJSON(extraFlags ...string) map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := append([]string{"--json"}, extraFlags...)
	cmd := exec.CommandContext(ctx, rocmSMI, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		logf("rocm-smi %s: %s", strings.Join(extraFlags, " "), detail)
		return nil
	}

	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		logf("rocm-smi %s: JSON parse error: %s", strings.Join(extraFlags, " "), err.Error())
		return nil
	}
	return result
}

// ── Main metrics parser ──────────────────────────────────────────────

func parseGPU(cardID int, d map[string]interface{}) GpuData {
	gpu := GpuData{
		CardID:        cardID,
		PowerMax:      math.NaN(),
		PcieTxBytes:   -1,
		PcieRxBytes:   -1,
		PcieBwTxDelta: -1,
		PcieBwRxDelta: -1,
		PcieTxMBps:    math.NaN(),
		PcieRxMBps:    math.NaN(),
	}

	series := getString(d, "Card Series")
	model := getString(d, "Card Model")
	if model == "" {
		model = getString(d, "Card model")
	}
	if series != "" {
		gpu.Name = strings.TrimSpace(series)
	} else if model != "" {
		gpu.Name = strings.TrimSpace(model)
	} else {
		gpu.Name = fmt.Sprintf("GPU %d", cardID)
	}

	gpu.Vendor = strings.TrimSpace(getString(d, "Card Vendor"))
	gpu.SKU = strings.TrimSpace(getString(d, "Card SKU"))
	gpu.GfxVersion = strings.TrimSpace(getString(d, "GFX Version"))

	gpu.TempEdge = parseFloat(getString(d, "Temperature (Sensor edge) (C)"), 0)
	gpu.TempJunc = parseFloat(getString(d, "Temperature (Sensor junction) (C)"), 0)
	gpu.TempMem = parseFloat(getString(d, "Temperature (Sensor memory) (C)"), 0)

	gpu.GpuUse = parseFloat(getString(d, "GPU use (%)"), 0)
	gpu.MemActivity = parseFloat(getString(d, "GPU Memory Read/Write Activity (%)"), 0)
	gpu.UmcActivity = parseFloat(getString(d, "Memory Activity"), 0)

	gpu.VramTotal = parseInt64(getString(d, "VRAM Total Memory (B)"), 0)
	gpu.VramUsed = parseInt64(getString(d, "VRAM Total Used Memory (B)"), 0)
	if gpu.VramTotal > 0 {
		gpu.VramPercent = float64(gpu.VramUsed) / float64(gpu.VramTotal) * 100
	} else {
		gpu.VramPercent = parseFloat(getString(d, "GPU Memory Allocated (VRAM%)"), 0)
	}

	gpu.PowerAvg = parseFloat(getString(d, "Average Graphics Package Power (W)"), 0)
	gpu.PowerMax = parseFloat(getString(d, "Max Graphics Package Power (W)"), 0)
	if gpu.PowerMax == 0 {
		gpu.PowerMax = math.NaN()
	}

	gpu.FanPercent = parseFloat(getString(d, "Fan speed (%)"), 0)
	gpu.FanRPM = parseInt(getString(d, "Fan RPM"), 0)

	for key, val := range d {
		kl := strings.ToLower(key)
		valStr := fmt.Sprintf("%v", val)
		if strings.Contains(kl, "sclk") && strings.Contains(kl, "clock speed") {
			gpu.Sclk = parseMHz(valStr)
		} else if strings.Contains(kl, "mclk") && strings.Contains(kl, "clock speed") {
			gpu.Mclk = parseMHz(valStr)
		}
	}

	gpu.Voltage = parseFloat(getString(d, "Voltage (mV)"), 0)
	gpu.PerfLevel = strings.TrimSpace(getString(d, "Performance Level"))
	gpu.PcieBus = strings.TrimSpace(getString(d, "PCI Bus"))

	return gpu
}

func parseProcesses(system map[string]interface{}) []ProcessData {
	procs := make(map[int]*ProcessData)

	for key, val := range system {
		if !strings.HasPrefix(key, "PID") {
			continue
		}
		pid, err := strconv.Atoi(key[3:])
		if err != nil {
			continue
		}
		parts := strings.Split(fmt.Sprintf("%v", val), ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 3 {
			continue
		}
		name := parts[0]
		// parts[1] is the NUMBER of GPUs the process uses (a count, not a
		// GPU index), so it must not populate GpuIDs. Real attribution comes
		// from "rocm-smi --showpidgpus" (see parsePidGpus/applyPidGpus).
		vram, _ := strconv.ParseInt(parts[2], 10, 64)

		if p, ok := procs[pid]; ok {
			p.VramUsed += vram
		} else {
			procs[pid] = &ProcessData{
				PID:      pid,
				Name:     name,
				VramUsed: vram,
			}
		}
	}

	result := make([]ProcessData, 0, len(procs))
	for _, p := range procs {
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].VramUsed > result[j].VramUsed
	})
	return result
}

var rePidGpus = regexp.MustCompile(`^PID\s+(\d+)\s+is using\s+(\d+)\s+DRM device\(s\)`)

// parsePidGpus parses the text output of "rocm-smi --showpidgpus" into a
// map of PID -> DRM device (GPU) indices. The JSON mode emits nothing for
// the GPU list, so the plain-text output must be parsed. It looks like:
//
//	====== GPUs Indexed by PID ======
//	PID 52243 is using 2 DRM device(s):
//	0 1
//	================================
//
// The index list is space-separated and may wrap across multiple lines.
// A PID using 0 devices prints only the "PID ... is using 0 DRM device(s)"
// line with no list.
func parsePidGpus(output string) map[int][]int {
	result := make(map[int][]int)
	currentPID := -1
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := rePidGpus.FindStringSubmatch(trimmed); m != nil {
			pid, err := strconv.Atoi(m[1])
			if err != nil {
				currentPID = -1
				continue
			}
			currentPID = pid
			result[pid] = []int{}
			continue
		}
		if currentPID < 0 || trimmed == "" {
			continue
		}
		// Device index lines: space-separated integers, possibly wrapped
		// across several lines. Any non-numeric line (e.g. the closing
		// "====" spacer) terminates the current PID's list.
		fields := strings.Fields(trimmed)
		ids := make([]int, 0, len(fields))
		valid := true
		for _, f := range fields {
			id, err := strconv.Atoi(f)
			if err != nil {
				valid = false
				break
			}
			ids = append(ids, id)
		}
		if valid {
			result[currentPID] = append(result[currentPID], ids...)
		} else {
			currentPID = -1
		}
	}
	return result
}

// applyPidGpuMap fills each process's GpuIDs from the PID -> GPU indices
// map produced by parsePidGpus. PIDs absent from the map keep empty GpuIDs
// (renderProcessTable shows "?" for those).
func applyPidGpuMap(procs []ProcessData, pidGpus map[int][]int) {
	for i := range procs {
		if ids, ok := pidGpus[procs[i].PID]; ok && len(ids) > 0 {
			procs[i].GpuIDs = ids
		}
	}
}

// applyPidGpus runs "rocm-smi --showpidgpus" (plain text — JSON mode emits
// no GPU list) and fills each process's GpuIDs with its DRM device indices.
func applyPidGpus(procs []ProcessData) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rocmSMI, "--showpidgpus")
	out, err := cmd.Output()
	if err != nil {
		logf("rocm-smi --showpidgpus: %v", err)
		return
	}
	applyPidGpuMap(procs, parsePidGpus(string(out)))
}

// ── Supplemental data collectors ─────────────────────────────────────

// cardIndexByID maps CardID -> index into gpus for all GPUs in the slice.
func cardIndexByID(gpus []GpuData) map[int]int {
	byID := make(map[int]int)
	for i, g := range gpus {
		byID[g.CardID] = i
	}
	return byID
}

// rocmCardIndexByID maps CardID -> index into gpus, keyed only by ROCm GPUs.
// rocm-smi card keys are CardID-only and other backends can share the same
// CardID integers, so mixing backends would populate the wrong GPU struct.
func rocmCardIndexByID(gpus []GpuData) map[int]int {
	byID := make(map[int]int)
	for i, g := range gpus {
		if g.Backend == "rocm" {
			byID[g.CardID] = i
		}
	}
	return byID
}

// forEachCard iterates rocm-smi JSON output, calling fn for each "cardN" key
// whose CardID is present in byID, passing the mapped gpus index and the
// card's value as a map.
func forEachCard(data map[string]interface{}, byID map[int]int, fn func(idx int, d map[string]interface{})) {
	for key, val := range data {
		if !strings.HasPrefix(strings.ToLower(key), "card") {
			continue
		}
		cardID, err := strconv.Atoi(key[4:])
		if err != nil {
			continue
		}
		idx, ok := byID[cardID]
		if !ok {
			continue
		}
		d, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		fn(idx, d)
	}
}

func applyMetrics(gpus []GpuData) {
	data := runJSON("--showmetrics")
	if data == nil {
		return
	}

	forEachCard(data, cardIndexByID(gpus), func(idx int, d map[string]interface{}) {
		ts := getString(d, "throttle_status")
		if ts == "" {
			ts = getString(d, "Throttle status")
		}
		if ts != "" {
			gpus[idx].ThrottleStatus = parseInt(ts, 0)
			gpus[idx].ThrottleReasons = throttleReasons(gpus[idx].ThrottleStatus)
		}

		width := getString(d, "pcie_link_width")
		if width == "" {
			width = getString(d, "PCIe Link Width")
		}
		if width != "" {
			gpus[idx].PcieWidth = parseInt(width, gpus[idx].PcieWidth)
		}

		speed := getString(d, "pcie_link_speed")
		if speed != "" {
			v, err := strconv.Atoi(strings.TrimSpace(speed))
			if err == nil && v > 0 {
				gts := float64(v) / 10
				gpus[idx].PcieSpeed = fmt.Sprintf("%.1fGT/s", gts)
			}
		}
	})
}

var rePcieBwSent = regexp.MustCompile(`bytes_sent:\s*(\d+)`)
var rePcieBwRecv = regexp.MustCompile(`bytes_received:\s*(\d+)`)

// parsePcieBwValue parses the rocm-smi --showbw value string.
// Example: "bytes_sent: 12345678, bytes_received: 87654321, mtu: 256"
// Returns -1 for either field when not present or on parse failure.
func parsePcieBwValue(s string) (tx, rx int64) {
	tx, rx = -1, -1
	if m := rePcieBwSent.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			tx = v
		}
	}
	if m := rePcieBwRecv.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			rx = v
		}
	}
	return
}

// applyGTT calls rocm-smi --showmeminfo gtt in a separate pass so it does not
// clobber the --showmeminfo vram keys (rocm-smi only returns the last meminfo
// type when multiple --showmeminfo flags are combined in one call).
func applyGTT(gpus []GpuData) {
	data := runJSON("--showmeminfo", "gtt")
	if data == nil {
		return
	}
	forEachCard(data, cardIndexByID(gpus), func(idx int, d map[string]interface{}) {
		total := parseInt64(getString(d, "GTT Total Memory (B)"), 0)
		used := parseInt64(getString(d, "GTT Total Used Memory (B)"), 0)
		if total > 0 {
			gpus[idx].GttTotal = total
			gpus[idx].GttUsed = used
			gpus[idx].GttPercent = float64(used) / float64(total) * 100
		}
	})
}

// applyBandwidth calls rocm-smi --showbw and populates PcieTxBytes/PcieRxBytes
// as raw cumulative byte counters. Rates are computed later by the model once
// two consecutive readings are available.
func applyBandwidth(gpus []GpuData) {
	data := runJSON("--showbw")
	if data == nil {
		return
	}
	forEachCard(data, cardIndexByID(gpus), func(idx int, d map[string]interface{}) {
		bwStr := getString(d, "pcie_bw")
		if bwStr == "" {
			return
		}
		tx, rx := parsePcieBwValue(bwStr)
		if tx >= 0 {
			gpus[idx].PcieTxBytes = tx
			gpus[idx].PcieRxBytes = rx
		}
	})

	// Fallback to the kernel pcie_bw sysfs file for GPUs where rocm-smi
	// --showbw is unsupported. The file exposes packet counts since the last
	// read; multiplying by max-payload-size gives byte deltas.
	for i := range gpus {
		if gpus[i].PcieTxBytes < 0 && gpus[i].PcieBus != "" {
			rx, tx := readPcieBwFile(gpus[i].PcieBus)
			gpus[i].PcieBwRxDelta = rx
			gpus[i].PcieBwTxDelta = tx
		}
	}
}

func pcieRootPort(pcieBus string) string {
	if pcieBus == "" || !reBDF.MatchString(pcieBus) {
		return ""
	}
	sysfs := fmt.Sprintf("/sys/bus/pci/devices/%s", pcieBus)
	real, err := filepath.EvalSymlinks(sysfs)
	if err != nil {
		return ""
	}
	parts := reBDF.FindAllString(real, -1)
	if len(parts) == 0 {
		return ""
	}
	gpuBDF := strings.TrimPrefix(pcieBus, "0000:")
	rootPort := strings.TrimPrefix(parts[0], "0000:")
	if rootPort != gpuBDF {
		return rootPort
	}
	return ""
}

var reGPURAS = regexp.MustCompile(`^GPU\[(\d+)\]`)

// parseRASInfo parses the text output of "rocm-smi --showrasinfo all" and
// accumulates correctable and uncorrectable error counts into gpus in-place.
func parseRASInfo(output string, gpus []GpuData) {
	byID := rocmCardIndexByID(gpus)

	currentIdx := -1
	for _, line := range strings.Split(output, "\n") {
		if m := reGPURAS.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			id, err := strconv.Atoi(m[1])
			if err != nil {
				currentIdx = -1
				continue
			}
			if idx, ok := byID[id]; ok {
				currentIdx = idx
			} else {
				currentIdx = -1
			}
			continue
		}
		if currentIdx < 0 {
			continue
		}
		// Block data lines look like:
		//   "  UMC  ENABLED  0  0"  or  "  UMC  DISABLED  3145680  3145680"
		// DISABLED blocks may still carry error counts.
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		corr, err1 := strconv.ParseInt(fields[2], 10, 64)
		uncorr, err2 := strconv.ParseInt(fields[3], 10, 64)
		if err1 == nil && err2 == nil {
			gpus[currentIdx].RasCorrectable += corr
			gpus[currentIdx].RasUncorrectable += uncorr
		}
	}
}

// collectRASInfo runs "rocm-smi --showrasinfo all" and populates RAS error
// counts for all ROCm GPUs in the slice.
func collectRASInfo(gpus []GpuData) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rocmSMI, "--showrasinfo", "all")
	out, err := cmd.Output()
	if err != nil {
		logf("rocm-smi --showrasinfo all: %v", err)
		return
	}
	parseRASInfo(string(out), gpus)
}

func collectStaticInfo(gpus []GpuData) {
	// Key only ROCm GPUs by CardID: the rocm-smi calls below return keys
	// like "card0"/"card3" which are CardID-only.
	byID := rocmCardIndexByID(gpus)

	// VBIOS
	forEachCard(runJSON("--showvbios"), byID, func(idx int, d map[string]interface{}) {
		gpus[idx].Vbios = strings.TrimSpace(getString(d, "VBIOS version"))
	})

	// Memory vendor
	forEachCard(runJSON("--showmemvendor"), byID, func(idx int, d map[string]interface{}) {
		gpus[idx].MemVendor = strings.TrimSpace(getString(d, "GPU memory vendor"))
	})

	// Unique ID
	forEachCard(runJSON("--showuniqueid"), byID, func(idx int, d map[string]interface{}) {
		gpus[idx].UniqueID = strings.TrimSpace(getString(d, "Unique ID"))
	})

	// PCIe root port
	for i := range gpus {
		if gpus[i].PcieBus != "" && gpus[i].PcieRootPort == "" {
			gpus[i].PcieRootPort = pcieRootPort(gpus[i].PcieBus)
		}
	}

	// Driver version
	drvData := runJSON("--showdriverversion")
	drv := ""
	for key, val := range drvData {
		if d, ok := val.(map[string]interface{}); ok {
			if v := getString(d, "Driver version"); v != "" {
				drv = strings.TrimSpace(v)
			}
		} else if strings.ToLower(key) == "driver version" {
			drv = strings.TrimSpace(fmt.Sprintf("%v", val))
		}
	}
	if drv != "" {
		for i := range gpus {
			gpus[i].DriverVersion = drv
		}
	}

	// RAS / ECC error counts
	collectRASInfo(gpus)
}

// ── Main collection entry point ──────────────────────────────────────

func collectGpuData(backends []GpuBackend) ([]GpuData, []ProcessData) {
	// Backends are independent processes/files with no shared state, so each
	// CollectData runs in its own goroutine. Results land in per-backend slots
	// to keep aggregation deterministic ahead of the sort below.
	type backendResult struct {
		gpus  []GpuData
		procs []ProcessData
	}
	results := make([]backendResult, len(backends))
	var wg sync.WaitGroup
	for i, b := range backends {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].gpus, results[i].procs = b.CollectData()
		}()
	}
	wg.Wait()

	var allGpus []GpuData
	var allProcs []ProcessData
	for _, r := range results {
		allGpus = append(allGpus, r.gpus...)
		allProcs = append(allProcs, r.procs...)
	}
	sort.SliceStable(allGpus, func(i, j int) bool {
		if allGpus[i].Backend != allGpus[j].Backend {
			return backendOrder(allGpus[i].Backend) < backendOrder(allGpus[j].Backend)
		}
		return allGpus[i].CardID < allGpus[j].CardID
	})
	allProcs = mergeProcesses(allProcs)
	return allGpus, allProcs
}

func (r *rocmBackend) CollectData() ([]GpuData, []ProcessData) {
	// Serialize collections: each bubbletea fetch command runs in its own
	// goroutine, so a manual refresh (or a slow fallback pass) can overlap
	// the next tick; discover() rewrites cards/sysfsMode and must not race
	// with another goroutine reading them.
	r.mu.Lock()
	defer r.mu.Unlock()

	// Sysfs fast path: refresh the cached mapping only when a read fails,
	// then fall back to the full rocm-smi pass for this tick if the mapping
	// still cannot be established.
	if r.sysfsMode && !r.sysfsOK() {
		logf("rocm: sysfs mapping stale, rediscovering")
		r.discover()
	}
	if !r.sysfsMode || !r.sysfsOK() {
		return r.collectViaRocmSMI()
	}

	gpus := make([]GpuData, 0, len(r.cards))
	for _, c := range r.cards {
		g := newGpuData(c.identity.CardID, "rocm")
		g.Name = c.identity.Name
		g.Vendor = c.identity.Vendor
		g.SKU = c.identity.SKU
		g.GfxVersion = c.identity.GfxVersion
		g.PcieBus = c.identity.PcieBus
		collectAmdSysfsMetrics(&g, c.dev)
		gpus = append(gpus, g)
	}
	return gpus, collectRocmProcesses()
}

// parseRocmAll parses the main rocm-smi JSON payload ("cardN" + "system"
// keys) into GPU and process lists.
func parseRocmAll(data map[string]interface{}) ([]GpuData, []ProcessData) {
	var gpus []GpuData
	var procs []ProcessData

	for key, val := range data {
		kl := strings.ToLower(key)
		d, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		if strings.HasPrefix(kl, "card") {
			cardID, err := strconv.Atoi(key[4:])
			if err != nil {
				continue
			}
			g := parseGPU(cardID, d)
			g.Backend = "rocm"
			gpus = append(gpus, g)
		} else if kl == "system" {
			procs = parseProcesses(d)
		}
	}

	sort.Slice(gpus, func(i, j int) bool {
		return gpus[i].CardID < gpus[j].CardID
	})

	return gpus, procs
}

// collectRocmProcesses runs the per-tick process listing (--showpids plus
// the conditional --showpidgpus attribution pass).
func collectRocmProcesses() []ProcessData {
	data := runJSON("--showpids")
	if data == nil {
		return nil
	}
	var procs []ProcessData
	for key, val := range data {
		if strings.ToLower(key) != "system" {
			continue
		}
		if d, ok := val.(map[string]interface{}); ok {
			procs = parseProcesses(d)
		}
	}
	if len(procs) > 0 {
		applyPidGpus(procs)
	}
	return procs
}

// collectViaRocmSMI is the legacy full-exec collection path (four rocm-smi
// invocations), used when GPUs cannot be mapped to sysfs card directories.
func (r *rocmBackend) collectViaRocmSMI() ([]GpuData, []ProcessData) {
	data := runJSON(rocmSMIFlags...)
	if data == nil {
		return nil, nil
	}

	gpus, procs := parseRocmAll(data)

	if len(gpus) > 0 {
		applyMetrics(gpus)
		applyGTT(gpus)
		applyBandwidth(gpus)
	}

	if len(procs) > 0 {
		applyPidGpus(procs)
	}

	return gpus, procs
}
