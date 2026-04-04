package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxHistory = 120

// ── Backend interface ───────────────────────────────────────────────

type GpuBackend interface {
	CollectData() ([]GpuData, []ProcessData)
	Name() string
}

var activeBackends []GpuBackend

// ── ROCm backend ────────────────────────────────────────────────────

type rocmBackend struct{}

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
	CardID  int
	Backend string
	Name    string
	TempEdge float64
	TempJunc float64
	TempMem  float64

	GpuUse      float64
	MemActivity float64
	UmcActivity float64

	VramTotal   int64
	VramUsed    int64
	VramPercent float64

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
}

type ProcessData struct {
	PID     int
	Name    string
	GpuIDs  []int
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
	GpuUse  RingBuffer
	Power   RingBuffer
	TempJnc RingBuffer
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

func backendNames() string {
	var names []string
	for _, b := range activeBackends {
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
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil
	}
	return result
}

// ── Main metrics parser ──────────────────────────────────────────────

func parseGPU(cardID int, d map[string]interface{}) GpuData {
	gpu := GpuData{CardID: cardID, PowerMax: 300.0}

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
	gpu.PowerMax = parseFloat(getString(d, "Max Graphics Package Power (W)"), 300)
	if gpu.PowerMax == 0 {
		gpu.PowerMax = 300
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
		gpuID, _ := strconv.Atoi(parts[1])
		vram, _ := strconv.ParseInt(parts[2], 10, 64)

		if p, ok := procs[pid]; ok {
			found := false
			for _, g := range p.GpuIDs {
				if g == gpuID {
					found = true
					break
				}
			}
			if !found && gpuID >= 0 {
				p.GpuIDs = append(p.GpuIDs, gpuID)
			}
			p.VramUsed += vram
		} else {
			gpuIDs := []int{}
			if gpuID >= 0 {
				gpuIDs = []int{gpuID}
			}
			procs[pid] = &ProcessData{
				PID:      pid,
				Name:     name,
				GpuIDs:   gpuIDs,
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

// ── Supplemental data collectors ─────────────────────────────────────

func applyMetrics(gpus []GpuData) {
	data := runJSON("--showmetrics")
	if data == nil {
		return
	}

	byID := make(map[int]int)
	for i, g := range gpus {
		byID[g.CardID] = i
	}

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

func collectStaticInfo(gpus []GpuData) {
	// Key only ROCm GPUs by CardID. The rocm-smi calls below return keys
	// like "card0"/"card3" which are CardID-only. Other backends can share
	// the same CardID integers, so mixing them here would cause the wrong
	// GPU struct to be populated.
	byID := make(map[int]int)
	for i, g := range gpus {
		if g.Backend == "rocm" {
			byID[g.CardID] = i
		}
	}

	// VBIOS
	for key, val := range runJSON("--showvbios") {
		if !strings.HasPrefix(strings.ToLower(key), "card") {
			continue
		}
		cardID, err := strconv.Atoi(key[4:])
		if err != nil {
			continue
		}
		if idx, ok := byID[cardID]; ok {
			if d, ok := val.(map[string]interface{}); ok {
				gpus[idx].Vbios = strings.TrimSpace(getString(d, "VBIOS version"))
			}
		}
	}

	// Memory vendor
	for key, val := range runJSON("--showmemvendor") {
		if !strings.HasPrefix(strings.ToLower(key), "card") {
			continue
		}
		cardID, err := strconv.Atoi(key[4:])
		if err != nil {
			continue
		}
		if idx, ok := byID[cardID]; ok {
			if d, ok := val.(map[string]interface{}); ok {
				gpus[idx].MemVendor = strings.TrimSpace(getString(d, "GPU memory vendor"))
			}
		}
	}

	// Unique ID
	for key, val := range runJSON("--showuniqueid") {
		if !strings.HasPrefix(strings.ToLower(key), "card") {
			continue
		}
		cardID, err := strconv.Atoi(key[4:])
		if err != nil {
			continue
		}
		if idx, ok := byID[cardID]; ok {
			if d, ok := val.(map[string]interface{}); ok {
				gpus[idx].UniqueID = strings.TrimSpace(getString(d, "Unique ID"))
			}
		}
	}

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
}

// ── Main collection entry point ──────────────────────────────────────

func collectGpuData() ([]GpuData, []ProcessData) {
	var allGpus []GpuData
	var allProcs []ProcessData
	for _, b := range activeBackends {
		gpus, procs := b.CollectData()
		allGpus = append(allGpus, gpus...)
		allProcs = append(allProcs, procs...)
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
	data := runJSON(rocmSMIFlags...)
	if data == nil {
		return nil, nil
	}

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

	if len(gpus) > 0 {
		applyMetrics(gpus)
	}

	return gpus, procs
}
