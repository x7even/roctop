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
	"time"
)

type nvidiaBackend struct{}

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
	n.collectBandwidth(gpus)
	procs := n.collectProcesses(gpus)
	return gpus, procs
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
		Vendor:        "NVIDIA",
		Backend:       "nvidia",
		PcieTxBytes:   -1,
		PcieRxBytes:   -1,
		PcieBwTxDelta: -1,
		PcieBwRxDelta: -1,
		PcieTxMBps:    math.NaN(),
		PcieRxMBps:    math.NaN(),
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
	gpu.PowerMax = parseFloat(f[8], 0)
	if gpu.PowerMax == 0 {
		gpu.PowerMax = parseFloat(f[9], 0) // fall back to power.max_limit
	}
	if gpu.PowerMax == 0 {
		gpu.PowerMax = math.NaN() // let panel handle unknown limit
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

func (n *nvidiaBackend) collectProcesses(gpus []GpuData) []ProcessData {
	procs := make(map[int]*ProcessData)

	for _, gpu := range gpus {
		output := runNvidiaSMI(
			"-i", strconv.Itoa(gpu.CardID),
			"--query-compute-apps=pid,used_gpu_memory",
			"--format=csv,noheader,nounits",
		)
		if output == "" {
			continue
		}

		r := csv.NewReader(strings.NewReader(output))
		r.TrimLeadingSpace = true

		for {
			fields, err := r.Read()
			if err != nil {
				break
			}
			if len(fields) < 2 {
				continue
			}

			pid := parseInt(fields[0], 0)
			if pid == 0 {
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
					if gid == gpu.CardID {
						found = true
						break
					}
				}
				if !found {
					p.GpuIDs = append(p.GpuIDs, gpu.CardID)
				}
			} else {
				procs[pid] = &ProcessData{
					PID:      pid,
					Name:     procName(pid),
					GpuIDs:   []int{gpu.CardID},
					VramUsed: vramBytes,
				}
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
