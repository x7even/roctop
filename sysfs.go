package main

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type sysfsBackend struct {
	cards []sysfsCard
}

type sysfsCard struct {
	name      string // e.g., "card0"
	deviceDir string // e.g., "/sys/class/drm/card0/device"
	hwmonDir  string // e.g., "/sys/class/drm/card0/device/hwmon/hwmon3"
	vendor    string // "AMD" or "Intel"
	pciBus    string
	cardIndex int
}

func (s *sysfsBackend) Name() string { return "sysfs" }

func newSysfsBackend(excludePCI map[string]bool) *sysfsBackend {
	matches, err := filepath.Glob("/sys/class/drm/card[0-9]*")
	if err != nil {
		return nil
	}

	var cards []sysfsCard
	for _, cardPath := range matches {
		name := filepath.Base(cardPath)
		// Skip render nodes like "card0-DP-1"
		if strings.Contains(name, "-") {
			continue
		}

		deviceDir := cardPath + "/device"

		vendorHex := readStringFile(deviceDir + "/vendor")
		var vendor string
		switch vendorHex {
		case "0x1002":
			vendor = "AMD"
		case "0x8086":
			vendor = "Intel"
		default:
			continue
		}

		// Get PCI bus address
		pciBus := parsePCISlotName(deviceDir + "/uevent")
		if pciBus == "" {
			// Try resolving the symlink
			real, err := filepath.EvalSymlinks(deviceDir)
			if err == nil {
				m := reBDF.FindString(real)
				if m != "" {
					pciBus = m
				}
			}
		}

		// Skip GPUs already claimed by rocm-smi or nvidia-smi
		if pciBus != "" && excludePCI[normalizePCI(pciBus)] {
			continue
		}

		// Find hwmon directory
		hwmonDir := ""
		hwmonMatches, _ := filepath.Glob(deviceDir + "/hwmon/hwmon*")
		if len(hwmonMatches) > 0 {
			hwmonDir = hwmonMatches[0]
		}

		idx, _ := strconv.Atoi(strings.TrimPrefix(name, "card"))

		cards = append(cards, sysfsCard{
			name:      name,
			deviceDir: deviceDir,
			hwmonDir:  hwmonDir,
			vendor:    vendor,
			pciBus:    pciBus,
			cardIndex: idx,
		})
	}

	if len(cards) == 0 {
		return nil
	}

	sort.Slice(cards, func(i, j int) bool {
		return cards[i].cardIndex < cards[j].cardIndex
	})

	return &sysfsBackend{cards: cards}
}

func (s *sysfsBackend) CollectData() ([]GpuData, []ProcessData) {
	var gpus []GpuData
	for _, card := range s.cards {
		gpu := GpuData{
			CardID:  card.cardIndex,
			Backend: "sysfs",
			Vendor:  card.vendor,
			PcieBus: card.pciBus,
		}

		// GPU name
		gpu.Name = readStringFile(card.deviceDir + "/product_name")
		if gpu.Name == "" {
			gpu.Name = card.vendor + " iGPU"
		}

		// Utilization (NaN if file missing)
		gpu.GpuUse = readFloatFileNaN(card.deviceDir + "/gpu_busy_percent")

		// Memory
		gpu.VramTotal = readInt64File(card.deviceDir+"/mem_info_vram_total", 0)
		gpu.VramUsed = readInt64File(card.deviceDir+"/mem_info_vram_used", 0)
		if gpu.VramTotal > 0 {
			gpu.VramPercent = float64(gpu.VramUsed) / float64(gpu.VramTotal) * 100
		}

		// Clocks
		gpu.Sclk = parseDpmFreq(card.deviceDir + "/pp_dpm_sclk")
		gpu.Mclk = parseDpmFreq(card.deviceDir + "/pp_dpm_mclk")

		// Intel fallback for clocks
		if card.vendor == "Intel" && gpu.Sclk == 0 {
			v := readFloatFileNaN(card.deviceDir + "/gt/gt0/rps_cur_freq_mhz")
			if !math.IsNaN(v) {
				gpu.Sclk = int(v)
			}
		}

		// hwmon metrics
		if card.hwmonDir != "" {
			// Temperature (millidegrees → degrees)
			tempMilli := readFloatFileNaN(card.hwmonDir + "/temp1_input")
			if !math.IsNaN(tempMilli) {
				gpu.TempEdge = tempMilli / 1000
				gpu.TempJunc = gpu.TempEdge
			} else {
				gpu.TempEdge = math.NaN()
				gpu.TempJunc = math.NaN()
			}

			// Power (microwatts → watts)
			powerMicro := readFloatFileNaN(card.hwmonDir + "/power1_average")
			if !math.IsNaN(powerMicro) {
				gpu.PowerAvg = powerMicro / 1_000_000
			} else {
				gpu.PowerAvg = math.NaN()
			}
			powerCapMicro := readFloatFileNaN(card.hwmonDir + "/power1_cap")
			if !math.IsNaN(powerCapMicro) {
				gpu.PowerMax = powerCapMicro / 1_000_000
				if gpu.PowerMax == 0 {
					gpu.PowerMax = 30
				}
			} else {
				gpu.PowerMax = math.NaN()
			}

			// Voltage (millivolts)
			gpu.Voltage = readFloatFile(card.hwmonDir+"/in0_input", 0)

			// Fan
			fanRPM := readFloatFileNaN(card.hwmonDir + "/fan1_input")
			if !math.IsNaN(fanRPM) {
				gpu.FanRPM = int(fanRPM)
			}
			pwm := readFloatFileNaN(card.hwmonDir + "/pwm1")
			if !math.IsNaN(pwm) && pwm > 0 {
				gpu.FanPercent = pwm / 255 * 100
			}
		} else {
			gpu.TempEdge = math.NaN()
			gpu.TempJunc = math.NaN()
			gpu.PowerAvg = math.NaN()
			gpu.PowerMax = math.NaN()
		}

		gpus = append(gpus, gpu)
	}

	return gpus, nil
}

// ── sysfs file helpers ──────────────────────────────────────────────

func readStringFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readFloatFileNaN returns NaN if the file doesn't exist or can't be parsed.
func readFloatFileNaN(path string) float64 {
	s := readStringFile(path)
	if s == "" {
		return math.NaN()
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

func readFloatFile(path string, def float64) float64 {
	s := readStringFile(path)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func readInt64File(path string, def int64) int64 {
	s := readStringFile(path)
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return def
	}
	return v
}

// parseDpmFreq reads pp_dpm_sclk/mclk and returns the current freq marked with *.
func parseDpmFreq(path string) int {
	data := readStringFile(path)
	if data == "" {
		return 0
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "*") {
			m := reMhz.FindStringSubmatch(line)
			if m != nil {
				v, _ := strconv.Atoi(m[1])
				return v
			}
		}
	}
	return 0
}

// parsePCISlotName reads a uevent file and extracts PCI_SLOT_NAME.
func parsePCISlotName(ueventPath string) string {
	data := readStringFile(ueventPath)
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
			return strings.TrimPrefix(line, "PCI_SLOT_NAME=")
		}
	}
	return ""
}
