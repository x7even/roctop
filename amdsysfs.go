package main

// Shared sysfs metric collection for AMD GPUs.
//
// Both the rocm backend (per-tick fast path) and the sysfs backend read the
// same amdgpu sysfs files; this file holds the single implementation so the
// two backends do not fork copies. Reading sysfs takes microseconds versus
// hundreds of milliseconds for a rocm-smi exec, so the rocm backend prefers
// this path for every per-tick metric and keeps rocm-smi only for process
// listing, one-time static info, and GPU discovery.

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const drmClassDir = "/sys/class/drm"

// amdSysfsDev locates the sysfs directories for one AMD GPU.
type amdSysfsDev struct {
	deviceDir string // /sys/class/drm/cardN/device; "" when unmapped
	hwmonDir  string // deviceDir/hwmon/hwmonM; "" when absent
	pciBus    string // normalized (lowercase) DDDD:BB:DD.F
}

// findHwmonDir returns the first hwmon directory under deviceDir, or "".
func findHwmonDir(deviceDir string) string {
	matches, _ := filepath.Glob(deviceDir + "/hwmon/hwmon*")
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// findAmdSysfsDev maps a PCI bus address (as reported by rocm-smi) to the
// matching /sys/class/drm/cardN/device directory by comparing against each
// card's uevent PCI_SLOT_NAME. Returns a dev with empty deviceDir when no
// card matches.
func findAmdSysfsDev(drmRoot, pciBus string) amdSysfsDev {
	want := normalizePCI(pciBus)
	dev := amdSysfsDev{pciBus: want}
	if want == "" {
		return dev
	}
	matches, _ := filepath.Glob(drmRoot + "/card[0-9]*")
	for _, cardPath := range matches {
		// Skip connector nodes like "card0-DP-1".
		if strings.Contains(filepath.Base(cardPath), "-") {
			continue
		}
		deviceDir := cardPath + "/device"
		if normalizePCI(parsePCISlotName(deviceDir+"/uevent")) == want {
			dev.deviceDir = deviceDir
			dev.hwmonDir = findHwmonDir(deviceDir)
			return dev
		}
	}
	return dev
}

// newGpuData returns a GpuData with the "unavailable" sentinels set the same
// way parseGPU initializes them, ready for collectAmdSysfsMetrics to fill.
func newGpuData(cardID int, backend string) GpuData {
	return GpuData{
		CardID:        cardID,
		Backend:       backend,
		TempEdge:      math.NaN(),
		TempJunc:      math.NaN(),
		TempMem:       math.NaN(),
		GpuUse:        math.NaN(),
		MemActivity:   math.NaN(),
		PowerAvg:      math.NaN(),
		PowerMax:      math.NaN(),
		PcieTxBytes:   -1,
		PcieRxBytes:   -1,
		PcieBwTxDelta: -1,
		PcieBwRxDelta: -1,
		PcieTxMBps:    math.NaN(),
		PcieRxMBps:    math.NaN(),
	}
}

// collectAmdSysfsMetrics fills every per-tick dynamic metric of gpu from
// sysfs. Missing files leave NaN/0/-1 sentinels that the renderer already
// handles. Identity fields (Name, Vendor, ...) are the caller's job.
func collectAmdSysfsMetrics(gpu *GpuData, dev amdSysfsDev) {
	d := dev.deviceDir

	// Utilization
	gpu.GpuUse = readFloatFileNaN(d + "/gpu_busy_percent")
	gpu.MemActivity = readFloatFileNaN(d + "/mem_busy_percent")

	// Memory
	gpu.VramTotal = readInt64File(d+"/mem_info_vram_total", 0)
	gpu.VramUsed = readInt64File(d+"/mem_info_vram_used", 0)
	if gpu.VramTotal > 0 {
		gpu.VramPercent = float64(gpu.VramUsed) / float64(gpu.VramTotal) * 100
	}
	gpu.GttTotal = readInt64File(d+"/mem_info_gtt_total", 0)
	gpu.GttUsed = readInt64File(d+"/mem_info_gtt_used", 0)
	if gpu.GttTotal > 0 {
		gpu.GttPercent = float64(gpu.GttUsed) / float64(gpu.GttTotal) * 100
	}

	// Clocks
	gpu.Sclk = parseDpmFreq(d + "/pp_dpm_sclk")
	gpu.Mclk = parseDpmFreq(d + "/pp_dpm_mclk")

	// Performance level
	gpu.PerfLevel = readStringFile(d + "/power_dpm_force_performance_level")

	// PCIe link ("16.0 GT/s PCIe" / "16")
	gpu.PcieSpeed = parseLinkSpeed(readStringFile(d + "/current_link_speed"))
	gpu.PcieWidth = int(readFloatFile(d+"/current_link_width", 0))

	// gpu_metrics blob: UMC activity + throttle status
	umc, throttle := readGpuMetricsFields(d)
	gpu.UmcActivity = umc
	gpu.ThrottleStatus = int(throttle)
	gpu.ThrottleReasons = throttleReasons(gpu.ThrottleStatus)

	// hwmon: temps, power, fan, voltage
	if dev.hwmonDir != "" {
		readHwmonMetrics(gpu, dev.hwmonDir)
	}

	// PCIe bandwidth fallbacks (rocm-smi --showbw counters are not read on
	// this path; PcieTxBytes stays -1 so the model uses these instead).
	// Priority 1: pcie_bw sysfs — TX/RX packet deltas (GCN-era GPUs).
	// Priority 2: gpu_metrics v1.4+ — instantaneous combined rate.
	if rx, tx := readPcieBwFile(dev.pciBus); rx >= 0 {
		gpu.PcieBwRxDelta = rx
		gpu.PcieBwTxDelta = tx
	} else if bw := readGpuMetricsBandwidth(d); !math.IsNaN(bw) {
		gpu.PcieTxMBps = bw // combined; PcieRxMBps stays NaN → panel shows "BW"
	}
}

// readHwmonMetrics fills temperature, power, fan, and voltage from an amdgpu
// hwmon directory.
func readHwmonMetrics(gpu *GpuData, hwmonDir string) {
	gpu.TempEdge, gpu.TempJunc, gpu.TempMem = readHwmonTemps(hwmonDir)
	// Older ASICs expose only the edge sensor; mirror it into junction so the
	// main temperature bar still shows data.
	if math.IsNaN(gpu.TempJunc) && !math.IsNaN(gpu.TempEdge) {
		gpu.TempJunc = gpu.TempEdge
	}

	// Power (microwatts → watts); power1_input is the fallback name used on
	// APUs and newer kernels where the average sensor is absent.
	powerMicro := readFloatFileNaN(hwmonDir + "/power1_average")
	if math.IsNaN(powerMicro) {
		powerMicro = readFloatFileNaN(hwmonDir + "/power1_input")
	}
	if !math.IsNaN(powerMicro) {
		gpu.PowerAvg = powerMicro / 1_000_000
	}
	powerCapMicro := readFloatFileNaN(hwmonDir + "/power1_cap")
	if !math.IsNaN(powerCapMicro) && powerCapMicro > 0 {
		gpu.PowerMax = powerCapMicro / 1_000_000
	}

	// Fan
	fanRPM := readFloatFileNaN(hwmonDir + "/fan1_input")
	if !math.IsNaN(fanRPM) {
		gpu.FanRPM = int(fanRPM)
	}
	pwm := readFloatFileNaN(hwmonDir + "/pwm1")
	if !math.IsNaN(pwm) {
		gpu.FanPercent = pwm / 255 * 100
	}

	// Voltage (millivolts)
	gpu.Voltage = readFloatFile(hwmonDir+"/in0_input", 0)
}

// readHwmonTemps reads temp[1-3]_input (millidegrees) and maps them to
// edge/junction/mem via the temp*_label files, falling back to the
// conventional positions (1=edge, 2=junction, 3=mem) when labels are absent.
func readHwmonTemps(hwmonDir string) (edge, junc, mem float64) {
	edge, junc, mem = math.NaN(), math.NaN(), math.NaN()
	for i := 1; i <= 3; i++ {
		base := fmt.Sprintf("%s/temp%d", hwmonDir, i)
		v := readFloatFileNaN(base + "_input")
		if math.IsNaN(v) {
			continue
		}
		v /= 1000
		switch readStringFile(base + "_label") {
		case "edge":
			edge = v
		case "junction":
			junc = v
		case "mem":
			mem = v
		default:
			switch i {
			case 1:
				if math.IsNaN(edge) {
					edge = v
				}
			case 2:
				if math.IsNaN(junc) {
					junc = v
				}
			case 3:
				if math.IsNaN(mem) {
					mem = v
				}
			}
		}
	}
	return
}

// parseLinkSpeed converts a PCI current_link_speed string such as
// "16.0 GT/s PCIe" into the compact "16.0GT/s" form used by the panel.
// Returns "" when the input is empty or malformed.
func parseLinkSpeed(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	v := parseFloat(fields[0], 0)
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1fGT/s", v)
}

// gpu_metrics field offsets valid for format_revision=1 with
// content_revision 1-3 only (see the struct layout documented above
// readGpuMetricsBandwidth). v1.0 and v1.4+ blobs lay these fields out
// differently: on v1.4/v1.5 (MI300-class), offset 18 is vcn_activity[1]
// and offset 68 falls inside the pcie_bandwidth_acc accumulator.
const (
	gpuMetricsUmcOff      = 18 // average_umc_activity (u16), v1.1-v1.3
	gpuMetricsThrottleOff = 68 // throttle_status (u32), v1.1-v1.3
	gpuMetricsU16NA       = 0xffff
	gpuMetricsU32NA       = 0xffffffff
)

// readGpuMetricsFields extracts average_umc_activity and throttle_status
// from the gpu_metrics blob. Fields that are absent (blob too small or
// wrong revision) or carry the N/A sentinel are returned as zero, matching
// what rocm-smi reports for unsupported metrics.
func readGpuMetricsFields(deviceDir string) (umc float64, throttle uint32) {
	data, err := os.ReadFile(deviceDir + "/gpu_metrics")
	if err != nil || len(data) < 4 {
		return 0, 0
	}
	size := binary.LittleEndian.Uint16(data[0:2])
	// Only format_revision=1, content_revision 1-3 use these offsets
	// (mirrors the content_revision gate in readGpuMetricsBandwidth).
	if data[2] != 1 || data[3] < 1 || data[3] > 3 || int(size) != len(data) {
		return 0, 0
	}
	if len(data) >= gpuMetricsUmcOff+2 {
		if v := binary.LittleEndian.Uint16(data[gpuMetricsUmcOff : gpuMetricsUmcOff+2]); v != gpuMetricsU16NA {
			umc = float64(v)
		}
	}
	if len(data) >= gpuMetricsThrottleOff+4 {
		if v := binary.LittleEndian.Uint32(data[gpuMetricsThrottleOff : gpuMetricsThrottleOff+4]); v != gpuMetricsU32NA {
			throttle = v
		}
	}
	return umc, throttle
}
