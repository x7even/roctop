package main

import (
	"context"
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
		"memory.total,memory.used,power.draw,power.limit,fan.speed," +
		"clocks.current.graphics,clocks.current.memory," +
		"pcie.link.gen.current,pcie.link.width.current," +
		"driver_version,vbios_version,pstate,pci.bus_id",
	"--format=csv,noheader,nounits",
}

func (n *nvidiaBackend) CollectData() ([]GpuData, []ProcessData) {
	gpus := n.collectGPUs()
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

func (n *nvidiaBackend) collectGPUs() []GpuData {
	output := runNvidiaSMI(nvidiaGPUQuery...)
	if output == "" {
		return nil
	}

	var gpus []GpuData
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ", ")
		if len(fields) < 18 {
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
	gpu.PowerMax = parseFloat(f[8], 300)
	if gpu.PowerMax == 0 {
		gpu.PowerMax = 300
	}

	gpu.FanPercent = parseFloat(f[9], 0)

	gpu.Sclk = parseInt(f[10], 0)
	gpu.Mclk = parseInt(f[11], 0)

	gpu.PcieSpeed = pcieGenToSpeed(parseInt(f[12], 0))
	gpu.PcieWidth = parseInt(f[13], 0)

	gpu.DriverVersion = strings.TrimSpace(f[14])
	gpu.Vbios = strings.TrimSpace(f[15])
	gpu.PerfLevel = strings.TrimSpace(f[16])
	gpu.PcieBus = strings.TrimSpace(f[17])

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

func (n *nvidiaBackend) collectProcesses(gpus []GpuData) []ProcessData {
	output := runNvidiaSMI(
		"--query-compute-apps=pid,used_gpu_memory",
		"--format=csv,noheader,nounits",
	)

	procs := make(map[int]*ProcessData)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		fields := strings.Split(line, ", ")
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
		} else {
			procs[pid] = &ProcessData{
				PID:      pid,
				Name:     procName(pid),
				VramUsed: vramBytes,
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
