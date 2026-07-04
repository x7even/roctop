package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ── Snapshot mode (--once / --once --json) ───────────────────────────
//
// Non-TUI, single-collection output for scripts and bug reports. Both
// renderers are pure functions over already-collected data so they can be
// unit-tested without hardware. PCIe TX/RX rates are deliberately reported
// as unavailable: they require two samples spaced in time, which the
// one-shot snapshot does not take.

// fmtOrNA formats v with format, or returns "N/A" when v is NaN.
func fmtOrNA(format string, v float64) string {
	if math.IsNaN(v) {
		return "N/A"
	}
	return fmt.Sprintf(format, v)
}

// orNA returns s, or "N/A" when s is empty.
func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "N/A"
	}
	return s
}

// gpuIDsString renders a process's GPU list as "0,1,3", or "?" when
// attribution is unavailable (mirrors renderProcessTable).
func gpuIDsString(ids []int) string {
	if len(ids) == 0 {
		return "?"
	}
	sorted := make([]int, len(ids))
	copy(sorted, ids)
	sort.Ints(sorted)
	parts := make([]string, len(sorted))
	for i, g := range sorted {
		parts[i] = strconv.Itoa(g)
	}
	return strings.Join(parts, ",")
}

// renderSnapshotText builds the plain-text snapshot: one block per GPU
// followed by a process table. No ANSI escapes — output must stay clean
// when piped or redirected.
func renderSnapshotText(gpus []GpuData, procs []ProcessData, backends []string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "roctop %s — snapshot (backends: %s)\n", version, orNA(strings.Join(backends, "+")))

	for _, g := range gpus {
		fmt.Fprintf(&b, "\nGPU %d [%s] %s\n", g.CardID, g.Backend, orNA(g.Name))
		fmt.Fprintf(&b, "  Use: %s   MACT: %s\n",
			fmtOrNA("%.0f%%", g.GpuUse), fmtOrNA("%.0f%%", g.MemActivity))
		fmt.Fprintf(&b, "  VRAM: %s / %s (%s)\n",
			fmtGB(g.VramUsed), fmtGB(g.VramTotal), fmtOrNA("%.1f%%", g.VramPercent))
		fmt.Fprintf(&b, "  Power: %s avg / %s cap\n",
			fmtOrNA("%.0fW", g.PowerAvg), fmtOrNA("%.0fW", g.PowerMax))
		fmt.Fprintf(&b, "  Temp: edge %s  junction %s  mem %s\n",
			fmtOrNA("%.0fC", g.TempEdge), fmtOrNA("%.0fC", g.TempJunc), fmtOrNA("%.0fC", g.TempMem))
		fmt.Fprintf(&b, "  Fan: %s (%d RPM)\n", fmtOrNA("%.0f%%", g.FanPercent), g.FanRPM)
		fmt.Fprintf(&b, "  Clocks: sclk %s  mclk %s   Perf: %s\n",
			fmtMHz(g.Sclk), fmtMHz(g.Mclk), orNA(g.PerfLevel))
		link := "N/A"
		if g.PcieSpeed != "" || g.PcieWidth > 0 {
			link = fmt.Sprintf("%s x%d", orNA(g.PcieSpeed), g.PcieWidth)
		}
		fmt.Fprintf(&b, "  PCIe: bus %s  link %s\n", orNA(g.PcieBus), link)
		b.WriteString("  PCIe BW: n/a (requires TUI sampling)\n")
		if len(g.ThrottleReasons) > 0 {
			fmt.Fprintf(&b, "  Throttle: %s\n", strings.Join(g.ThrottleReasons, ", "))
		}
	}

	b.WriteString("\nProcesses:\n")
	if len(procs) == 0 {
		b.WriteString("  (no GPU processes)\n")
	} else {
		fmt.Fprintf(&b, "  %-8s %-24s %-12s %10s\n", "PID", "Name", "GPUs", "VRAM")
		for _, p := range procs {
			fmt.Fprintf(&b, "  %-8d %-24s %-12s %10s\n",
				p.PID, p.Name, gpuIDsString(p.GpuIDs), fmtBytes(p.VramUsed))
		}
	}

	return b.String()
}

// ── JSON snapshot ────────────────────────────────────────────────────

// snapshotGPU is a purpose-built DTO for --json: stable lower_snake field
// names, internal plumbing fields (Pcie*Bytes/Deltas/MBps) omitted, and
// NaN-able floats carried as *float64 so they serialize as null instead
// of breaking json.Marshal.
type snapshotGPU struct {
	CardID             int      `json:"card_id"`
	Backend            string   `json:"backend"`
	Name               string   `json:"name"`
	Vendor             string   `json:"vendor,omitempty"`
	SKU                string   `json:"sku,omitempty"`
	GfxVersion         string   `json:"gfx_version,omitempty"`
	GpuUsePercent      *float64 `json:"gpu_use_percent"`
	MemActivityPercent *float64 `json:"mem_activity_percent"`
	VramUsedBytes      int64    `json:"vram_used_bytes"`
	VramTotalBytes     int64    `json:"vram_total_bytes"`
	VramPercent        *float64 `json:"vram_percent"`
	GttUsedBytes       int64    `json:"gtt_used_bytes,omitempty"`
	GttTotalBytes      int64    `json:"gtt_total_bytes,omitempty"`
	PowerAvgWatts      *float64 `json:"power_avg_watts"`
	PowerCapWatts      *float64 `json:"power_cap_watts"`
	TempEdgeC          *float64 `json:"temp_edge_c"`
	TempJunctionC      *float64 `json:"temp_junction_c"`
	TempMemC           *float64 `json:"temp_mem_c"`
	FanPercent         *float64 `json:"fan_percent"`
	FanRPM             int      `json:"fan_rpm"`
	SclkMHz            int      `json:"sclk_mhz"`
	MclkMHz            int      `json:"mclk_mhz"`
	VoltageMv          *float64 `json:"voltage_mv,omitempty"`
	PerfLevel          string   `json:"perf_level,omitempty"`
	PcieBus            string   `json:"pcie_bus,omitempty"`
	PcieSpeed          string   `json:"pcie_speed,omitempty"`
	PcieWidth          int      `json:"pcie_width,omitempty"`
	ThrottleStatus     int      `json:"throttle_status"`
	ThrottleReasons    []string `json:"throttle_reasons,omitempty"`
	Vbios              string   `json:"vbios,omitempty"`
	MemVendor          string   `json:"mem_vendor,omitempty"`
	DriverVersion      string   `json:"driver_version,omitempty"`
	UniqueID           string   `json:"unique_id,omitempty"`
	RasCorrectable     int64    `json:"ras_correctable"`
	RasUncorrectable   int64    `json:"ras_uncorrectable"`
}

type snapshotProcess struct {
	PID           int    `json:"pid"`
	Name          string `json:"name"`
	GpuIDs        []int  `json:"gpu_ids"`
	VramUsedBytes int64  `json:"vram_used_bytes"`
}

type snapshotJSON struct {
	Version   string            `json:"version"`
	Backends  []string          `json:"backends"`
	Gpus      []snapshotGPU     `json:"gpus"`
	Processes []snapshotProcess `json:"processes"`
}

// nanToNull maps NaN to nil (JSON null) and any other value to a pointer.
func nanToNull(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

// buildSnapshotJSON marshals the snapshot DTO as indented JSON.
func buildSnapshotJSON(gpus []GpuData, procs []ProcessData, backends []string) ([]byte, error) {
	snap := snapshotJSON{
		Version:   version,
		Backends:  backends,
		Gpus:      make([]snapshotGPU, 0, len(gpus)),
		Processes: make([]snapshotProcess, 0, len(procs)),
	}
	if snap.Backends == nil {
		snap.Backends = []string{}
	}
	for _, g := range gpus {
		voltage := nanToNull(g.Voltage)
		if voltage != nil && *voltage == 0 {
			voltage = nil // 0 mV means "not reported"; drop with omitempty
		}
		snap.Gpus = append(snap.Gpus, snapshotGPU{
			CardID:             g.CardID,
			Backend:            g.Backend,
			Name:               g.Name,
			Vendor:             g.Vendor,
			SKU:                g.SKU,
			GfxVersion:         g.GfxVersion,
			GpuUsePercent:      nanToNull(g.GpuUse),
			MemActivityPercent: nanToNull(g.MemActivity),
			VramUsedBytes:      g.VramUsed,
			VramTotalBytes:     g.VramTotal,
			VramPercent:        nanToNull(g.VramPercent),
			GttUsedBytes:       g.GttUsed,
			GttTotalBytes:      g.GttTotal,
			PowerAvgWatts:      nanToNull(g.PowerAvg),
			PowerCapWatts:      nanToNull(g.PowerMax),
			TempEdgeC:          nanToNull(g.TempEdge),
			TempJunctionC:      nanToNull(g.TempJunc),
			TempMemC:           nanToNull(g.TempMem),
			FanPercent:         nanToNull(g.FanPercent),
			FanRPM:             g.FanRPM,
			SclkMHz:            g.Sclk,
			MclkMHz:            g.Mclk,
			VoltageMv:          voltage,
			PerfLevel:          g.PerfLevel,
			PcieBus:            g.PcieBus,
			PcieSpeed:          g.PcieSpeed,
			PcieWidth:          g.PcieWidth,
			ThrottleStatus:     g.ThrottleStatus,
			ThrottleReasons:    g.ThrottleReasons,
			Vbios:              g.Vbios,
			MemVendor:          g.MemVendor,
			DriverVersion:      g.DriverVersion,
			UniqueID:           g.UniqueID,
			RasCorrectable:     g.RasCorrectable,
			RasUncorrectable:   g.RasUncorrectable,
		})
	}
	for _, p := range procs {
		ids := make([]int, len(p.GpuIDs))
		copy(ids, p.GpuIDs)
		sort.Ints(ids)
		snap.Processes = append(snap.Processes, snapshotProcess{
			PID:           p.PID,
			Name:          p.Name,
			GpuIDs:        ids,
			VramUsedBytes: p.VramUsed,
		})
	}
	return json.MarshalIndent(snap, "", "  ")
}
