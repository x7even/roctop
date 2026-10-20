package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nvidiaBackend struct {
	mu            sync.Mutex // guards the static-limit cache; bubbletea can run overlapping collection goroutines
	limits        map[string]nvStatic
	limitFailures int
	// queryFn runs nvidia-smi; nil means the real binary. Injected so the
	// cache and retry logic are testable without an NVIDIA GPU.
	queryFn func(args ...string) string
}

func (n *nvidiaBackend) query(args ...string) string {
	if n.queryFn != nil {
		return n.queryFn(args...)
	}
	return runNvidiaSMI(args...)
}

func (n *nvidiaBackend) Name() string { return "nvidia" }

var nvidiaGPUQuery = []string{
	"--query-gpu=index,name,temperature.gpu,utilization.gpu,utilization.memory," +
		"memory.total,memory.used,power.draw,power.limit,power.max_limit,fan.speed," +
		"clocks.current.graphics,clocks.current.memory," +
		"pcie.link.gen.current,pcie.link.width.current," +
		"driver_version,vbios_version,pstate,pci.bus_id",
	"--format=csv,noheader,nounits",
}

func (n *nvidiaBackend) CollectData() ([]GpuData, []ProcessData) {
	gpus := n.collectGPUs()
	applyNvidiaStatic(gpus, n.staticLimits())
	n.collectBandwidth(gpus)
	procs := n.collectProcesses(gpus)
	return gpus, procs
}

// ── Power limits and clock ceilings ───────────────────────────────────
//
// The board default, the minimum settable cap and the two clock ceilings
// never change while the driver is loaded, so they are queried once and
// cached rather than on every refresh. They live here rather than in
// CollectStaticInfo because --once never runs that pass, and a snapshot
// should carry the same cap context the TUI shows.
//
// This is a second nvidia-smi invocation on purpose: an unknown field name
// makes nvidia-smi fail the *whole* query, so adding these to the per-tick
// query would cost every metric on any driver that lacks one of them.
var nvidiaStaticQuery = []string{
	"--query-gpu=pci.bus_id,power.default_limit,power.min_limit," +
		"clocks.max.graphics,clocks.max.memory",
	"--format=csv,noheader,nounits",
}

// nvStatic holds one card's unchanging limits. Power values are NaN and
// clocks 0 when the driver reports them as [N/A] or [Not Supported].
type nvStatic struct {
	capDefault float64
	capMin     float64
	sclkMax    int
	mclkMax    int
}

// nvidiaStaticAttempts caps the retries after a failed query so a driver
// that rejects one of these field names costs a handful of subprocesses
// rather than one per refresh, forever.
const nvidiaStaticAttempts = 3

// staticLimits returns the cached limits, querying on first use. A failed
// query is retried on later refreshes rather than cached as "unavailable":
// a single nvidia-smi timeout at startup must not blank the readout for
// the life of the process.
func (n *nvidiaBackend) staticLimits() map[string]nvStatic {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.limits != nil {
		return n.limits
	}
	if limits := parseNvidiaStatic(n.query(nvidiaStaticQuery...)); len(limits) > 0 {
		n.limits = limits
		return n.limits
	}
	n.limitFailures++
	if n.limitFailures >= nvidiaStaticAttempts {
		logf("warning: nvidia-smi reported no power limits after %d attempts; "+
			"cap range and clock ceilings will be unavailable", n.limitFailures)
		n.limits = map[string]nvStatic{} // give up; non-nil stops the retries
		return n.limits
	}
	return nil
}

// parseNvidiaStatic parses the CSV of nvidiaStaticQuery, keyed by
// normalized PCI bus address. Rows without a usable bus id are dropped —
// the bus id is the only thing that joins them to a GPU.
func parseNvidiaStatic(output string) map[string]nvStatic {
	if output == "" {
		return nil
	}
	r := csv.NewReader(strings.NewReader(output))
	r.TrimLeadingSpace = true

	limits := make(map[string]nvStatic)
	for {
		f, err := r.Read()
		if err != nil {
			break
		}
		if len(f) < 5 {
			continue
		}
		bus := normalizePCI(f[0])
		if bus == "" {
			continue
		}
		limits[bus] = nvStatic{
			capDefault: sanePowerW(parseFloat(f[1], math.NaN())),
			capMin:     sanePowerW(parseFloat(f[2], math.NaN())),
			sclkMax:    parseInt(f[3], 0),
			mclkMax:    parseInt(f[4], 0),
		}
	}
	if len(limits) == 0 {
		return nil
	}
	return limits
}

// applyNvidiaStatic joins the cached limits onto the collected GPUs by PCI
// bus address, leaving a GPU untouched when its bus is absent.
func applyNvidiaStatic(gpus []GpuData, limits map[string]nvStatic) {
	if len(limits) == 0 {
		return
	}
	for i := range gpus {
		lim, ok := limits[normalizePCI(gpus[i].PcieBus)]
		if !ok {
			continue
		}
		gpus[i].PowerCapDefault = lim.capDefault
		gpus[i].PowerCapMin = lim.capMin
		gpus[i].SclkMax = lim.sclkMax
		gpus[i].MclkMax = lim.mclkMax
	}
}

// sanePowerW rejects the all-ones sentinel some drivers return for an
// unsupported power limit (4294967.50 W, i.e. UINT32_MAX mW) along with
// zero and negative readings. Without this a single bad field scales the
// power bar to megawatts.
func sanePowerW(w float64) float64 {
	if math.IsNaN(w) || w <= 0 || w > 10000 {
		return math.NaN()
	}
	return w
}

func runNvidiaSMI(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nvidia-smi", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

const nvidiaExpectedFields = 19

func (n *nvidiaBackend) collectGPUs() []GpuData {
	output := runNvidiaSMI(nvidiaGPUQuery...)
	if output == "" {
		return nil
	}

	r := csv.NewReader(strings.NewReader(output))
	r.TrimLeadingSpace = true

	var gpus []GpuData
	for {
		fields, err := r.Read()
		if err != nil {
			break
		}
		if len(fields) < nvidiaExpectedFields {
			fmt.Fprintf(os.Stderr, "warning: nvidia-smi returned %d fields, expected %d\n", len(fields), nvidiaExpectedFields)
			continue
		}
		gpu := parseNvidiaGPULine(fields)
		gpus = append(gpus, gpu)
	}

	sort.Slice(gpus, func(i, j int) bool {
		return gpus[i].CardID < gpus[j].CardID
	})
	return gpus
}

func parseNvidiaGPULine(f []string) GpuData {
	gpu := GpuData{
		Vendor:  "NVIDIA",
		Backend: "nvidia",
		// The cap context is filled from the query below and from the cached
		// static limits; NaN is the starting point so a card that reports
		// [N/A] is never shown as a 0 W default. Voltage and the OverDrive
		// offsets stay unset: nvidia-smi exposes neither (core voltage was
		// removed in the 575-580 series, and clock offsets need NVML).
		PowerCapDefault: math.NaN(),
		PowerCapMin:     math.NaN(),
		PowerCapMax:     math.NaN(),
		Voltage:         math.NaN(),
		PcieTxBytes:     -1,
		PcieRxBytes:     -1,
		PcieBwTxDelta:   -1,
		PcieBwRxDelta:   -1,
		PcieTxMBps:      math.NaN(),
		PcieRxMBps:      math.NaN(),
	}

	gpu.CardID = parseInt(f[0], 0)
	gpu.Name = strings.TrimSpace(f[1])

	temp := parseFloat(f[2], 0)
	gpu.TempEdge = temp
	gpu.TempJunc = temp // NVIDIA has one temp; TempJunc drives the display

	gpu.GpuUse = parseFloat(f[3], 0)
	gpu.MemActivity = parseFloat(f[4], 0)

	memTotalMiB := parseFloat(f[5], 0)
	memUsedMiB := parseFloat(f[6], 0)
	gpu.VramTotal = int64(memTotalMiB * 1024 * 1024)
	gpu.VramUsed = int64(memUsedMiB * 1024 * 1024)
	if gpu.VramTotal > 0 {
		gpu.VramPercent = float64(gpu.VramUsed) / float64(gpu.VramTotal) * 100
	}

	gpu.PowerAvg = parseFloat(f[7], 0)
	// Sanitise before falling back: a card reporting the UINT32_MAX sentinel
	// for power.limit should drop through to power.max_limit, not scale the
	// power bar to megawatts.
	gpu.PowerMax = sanePowerW(parseFloat(f[8], math.NaN()))
	gpu.PowerCapMax = sanePowerW(parseFloat(f[9], math.NaN()))
	if math.IsNaN(gpu.PowerMax) {
		gpu.PowerMax = gpu.PowerCapMax // fall back to power.max_limit
	}

	gpu.FanPercent = parseFloat(f[10], 0)

	gpu.Sclk = parseInt(f[11], 0)
	gpu.Mclk = parseInt(f[12], 0)

	gpu.PcieSpeed = pcieGenToSpeed(parseInt(f[13], 0))
	gpu.PcieWidth = parseInt(f[14], 0)

	gpu.DriverVersion = strings.TrimSpace(f[15])
	gpu.Vbios = strings.TrimSpace(f[16])
	gpu.PerfLevel = strings.TrimSpace(f[17])
	gpu.PcieBus = strings.TrimSpace(f[18])

	return gpu
}

func pcieGenToSpeed(gen int) string {
	switch gen {
	case 1:
		return "2.5GT/s"
	case 2:
		return "5.0GT/s"
	case 3:
		return "8.0GT/s"
	case 4:
		return "16.0GT/s"
	case 5:
		return "32.0GT/s"
	default:
		return ""
	}
}

func (n *nvidiaBackend) collectBandwidth(gpus []GpuData) {
	output := runNvidiaSMI("dmon", "-s", "t", "-c", "1")
	if output == "" {
		return
	}

	// Map CardID → slice index so non-contiguous GPU indices
	// (e.g. GPU 0 and GPU 2 with no GPU 1) are resolved correctly.
	byCardID := make(map[int]int, len(gpus))
	for i, g := range gpus {
		byCardID[g.CardID] = i
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		cardID := parseInt(fields[0], -1)
		if cardID < 0 {
			continue
		}
		sliceIdx, ok := byCardID[cardID]
		if !ok {
			continue
		}
		rx := parseFloat(fields[1], math.NaN())
		tx := parseFloat(fields[2], math.NaN())
		if !math.IsNaN(rx) {
			gpus[sliceIdx].PcieRxMBps = rx
		}
		if !math.IsNaN(tx) {
			gpus[sliceIdx].PcieTxMBps = tx
		}
	}
}

// collectProcesses runs a single nvidia-smi query covering all GPUs and
// attributes each process to a CardID via its gpu_bus_id column, avoiding
// one nvidia-smi invocation per GPU.
func (n *nvidiaBackend) collectProcesses(gpus []GpuData) []ProcessData {
	if len(gpus) == 0 {
		return nil
	}
	output := runNvidiaSMI(
		"--query-compute-apps=pid,used_gpu_memory,gpu_bus_id",
		"--format=csv,noheader,nounits",
	)
	if output == "" {
		return nil
	}
	return parseNvidiaProcesses(output, nvidiaBusToCardID(gpus), procName)
}

// nvidiaBusToCardID maps each GPU's normalized PCI bus address to its CardID.
func nvidiaBusToCardID(gpus []GpuData) map[string]int {
	byBus := make(map[string]int, len(gpus))
	for _, g := range gpus {
		if bus := normalizePCI(g.PcieBus); bus != "" {
			byBus[bus] = g.CardID
		}
	}
	return byBus
}

// parseNvidiaProcesses parses the CSV output of
// "nvidia-smi --query-compute-apps=pid,used_gpu_memory,gpu_bus_id
// --format=csv,noheader,nounits". busToCard maps normalized PCI bus
// addresses (nvidia-smi prints e.g. "00000000:C3:00.0") to CardIDs; rows
// with an unknown bus id are skipped. used_gpu_memory is in MiB and may be
// "[N/A]" (treated as 0). A PID appearing on several bus ids is merged into
// one ProcessData spanning those GPUs. nameFn resolves a PID to a process
// name; it is injected so tests need not read /proc.
func parseNvidiaProcesses(output string, busToCard map[string]int, nameFn func(int) string) []ProcessData {
	procs := make(map[int]*ProcessData)

	r := csv.NewReader(strings.NewReader(output))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1

	for {
		fields, err := r.Read()
		if err != nil {
			break
		}
		if len(fields) < 3 {
			continue
		}

		pid := parseInt(fields[0], 0)
		if pid == 0 {
			continue
		}
		cardID, ok := busToCard[normalizePCI(fields[2])]
		if !ok {
			continue
		}
		vramMiB := parseInt64(fields[1], 0)
		vramBytes := vramMiB * 1024 * 1024

		if p, ok := procs[pid]; ok {
			p.VramUsed += vramBytes
			// Append this GPU to GpuIDs if not already present
			// (a process can span multiple GPUs in MIG or NVLink setups).
			found := false
			for _, gid := range p.GpuIDs {
				if gid == cardID {
					found = true
					break
				}
			}
			if !found {
				p.GpuIDs = append(p.GpuIDs, cardID)
			}
		} else {
			procs[pid] = &ProcessData{
				PID:      pid,
				Name:     nameFn(pid),
				GpuIDs:   []int{cardID},
				VramUsed: vramBytes,
				GpuBusy:  math.NaN(),
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

func procName(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}
