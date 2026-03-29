package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const sparkIndent = 6

var (
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5000")).Bold(true)
	labelStyle = lipgloss.NewStyle().Faint(true)
	boldStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	cyanStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff")).Bold(true)
)

var panelBorder = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#1a3a5a")).
	PaddingLeft(1).
	PaddingRight(1)

func renderGpuPanel(gpu GpuData, hist *GpuHistory, width int, infoMode bool) string {
	// content width = panel width - 2 (border) - 2 (padding)
	cw := width - 4
	if cw < 20 {
		cw = 20
	}

	var lines []string
	if infoMode {
		lines = renderInfoLines(gpu, cw)
		for len(lines) < 11 {
			lines = append(lines, "")
		}
	} else {
		lines = renderMetricLines(gpu, hist, cw)
	}

	content := strings.Join(lines, "\n")
	return panelBorder.Width(width - 2).Render(content)
}

// ── Metrics view ──────────────────────────────────────────────────────

func renderMetricLines(gpu GpuData, hist *GpuHistory, cw int) []string {
	const labelW = 5
	const pctW = 7 // " 100.0%"

	gbInfo := fmt.Sprintf(" %s/%s", fmtGB(gpu.VramUsed), fmtGB(gpu.VramTotal))
	pwrSfx := fmt.Sprintf(" %.0fW/%.0fW", gpu.PowerAvg, gpu.PowerMax)
	tmpSfx := fmt.Sprintf(" %.0f°C · FAN %.0f%% %drpm · CLK %s · MEM %s",
		gpu.TempJunc, gpu.FanPercent, gpu.FanRPM, fmtMHz(gpu.Sclk), fmtMHz(gpu.Mclk))
	sparkW := max(8, cw-sparkIndent)

	bw := func(suffixLen int) int {
		v := cw - labelW - suffixLen
		if v < 8 {
			return 8
		}
		return v
	}

	// Title
	title := cyanStyle.Render(fmt.Sprintf("GPU %d", gpu.CardID)) +
		" · " + boldStyle.Render(gpu.Name)
	if gpu.ThrottleStatus != 0 {
		reasons := "UNKNOWN"
		if len(gpu.ThrottleReasons) > 0 {
			reasons = strings.Join(gpu.ThrottleReasons, ", ")
		}
		title += "  " + warnStyle.Render("⚠ THROTTLED: "+reasons)
	}

	// USE bar
	useLine := labelStyle.Render("USE  ") +
		renderBar(gpu.GpuUse, 100, bw(pctW), utilGradient) +
		boldStyle.Render(fmt.Sprintf(" %5.1f%%", gpu.GpuUse))

	// USE sparkline (3 rows)
	useRows := renderMultilineSparkline(hist.GpuUse.Values(), sparkW, 3, 0, 100, utilGradient, 100)
	useLabel := dimStyle.Render(fmt.Sprintf("%4.0f%% ", gpu.GpuUse))
	blankPfx := strings.Repeat(" ", sparkIndent)

	// VRAM bar
	vramLine := labelStyle.Render("VRAM ") +
		renderBar(gpu.VramPercent, 100, bw(pctW+len(gbInfo)), utilGradient) +
		boldStyle.Render(fmt.Sprintf(" %5.1f%%", gpu.VramPercent)) +
		dimStyle.Render(gbInfo)

	// PWR bar
	pwrLine := labelStyle.Render("PWR  ") +
		renderBar(gpu.PowerAvg, gpu.PowerMax, bw(len(pwrSfx)), powerGradient) +
		boldStyle.Render(pwrSfx)

	// PWR sparkline (3 rows)
	pwrRows := renderMultilineSparkline(hist.Power.Values(), sparkW, 3, 0, gpu.PowerMax, powerGradient, gpu.PowerMax)
	pwrLabel := dimStyle.Render(fmt.Sprintf("%4.0fW ", gpu.PowerAvg))

	// TEMP bar
	tempLine := labelStyle.Render("TEMP ") +
		renderBar(gpu.TempJunc, 110, bw(len(tmpSfx)), tempGradient) +
		boldStyle.Render(fmt.Sprintf(" %.0f°C", gpu.TempJunc)) +
		dimStyle.Render(" · FAN ") +
		boldStyle.Render(fmt.Sprintf("%.0f%%", gpu.FanPercent)) +
		dimStyle.Render(fmt.Sprintf(" %drpm", gpu.FanRPM)) +
		dimStyle.Render(" · CLK ") +
		boldStyle.Render(fmtMHz(gpu.Sclk)) +
		dimStyle.Render(" · MEM ") +
		boldStyle.Render(fmtMHz(gpu.Mclk))

	return []string{
		title,
		useLine,
		useLabel + useRows[0],
		blankPfx + useRows[1],
		blankPfx + useRows[2],
		vramLine,
		pwrLine,
		pwrLabel + pwrRows[0],
		blankPfx + pwrRows[1],
		blankPfx + pwrRows[2],
		tempLine,
	}
}

// ── Info view ─────────────────────────────────────────────────────────

func renderInfoLines(gpu GpuData, cw int) []string {
	colW := cw/2 - 14
	if colW < 8 {
		colW = 8
	}

	title := cyanStyle.Render(fmt.Sprintf("GPU %d", gpu.CardID)) +
		" · " + boldStyle.Render(gpu.Name) +
		"  " + dimStyle.Render("press ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700")).Bold(true).Render("i") +
		dimStyle.Render(" to return to metrics")

	kv := func(label, value string) string {
		if value == "" {
			value = "N/A"
		}
		runes := []rune(value)
		if len(runes) > colW {
			value = string(runes[:colW-1]) + "…"
		}
		return labelStyle.Render(fmt.Sprintf("%-10s", label+":")) +
			" " + boldStyle.Render(fmt.Sprintf("%-*s", colW, value))
	}

	kvRow := func(l1, v1, l2, v2 string) string {
		return kv(l1, v1) + "  " + kv(l2, v2)
	}

	vendor := gpu.Vendor
	if vendor == "" {
		vendor = "AMD"
	}

	pcieVal := gpu.PcieBus
	if gpu.PcieWidth > 0 {
		pcieVal += fmt.Sprintf(" x%d", gpu.PcieWidth)
	}
	if gpu.PcieSpeed != "" {
		pcieVal += " " + gpu.PcieSpeed
	}
	if gpu.PcieRootPort != "" {
		pcieVal += "  root " + gpu.PcieRootPort
	}

	memVal := fmtGB(gpu.VramTotal)
	if gpu.MemVendor != "" {
		memVal = gpu.MemVendor + " " + memVal
	}

	perfLevel := gpu.PerfLevel
	if perfLevel == "" {
		perfLevel = "auto"
	}

	var throttleLine string
	if gpu.ThrottleStatus != 0 {
		reasons := "ACTIVE"
		if len(gpu.ThrottleReasons) > 0 {
			reasons = strings.Join(gpu.ThrottleReasons, ", ")
		}
		reasonRunes := []rune(reasons)
		maxReasonW := colW - 2 // leave room for "⚠ " prefix
		if len(reasonRunes) > maxReasonW {
			reasons = string(reasonRunes[:maxReasonW-1]) + "…"
		}
		throttleLine = labelStyle.Render(fmt.Sprintf("%-10s", "Throttle:")) +
			" " + warnStyle.Render(fmt.Sprintf("%-*s", colW, "⚠ "+reasons)) +
			"  " + labelStyle.Render(fmt.Sprintf("%-10s", "Voltage:")) +
			" " + boldStyle.Render(fmt.Sprintf("%.0fmV", gpu.Voltage))
	} else {
		throttleLine = kvRow("Throttle", "none", "Voltage", fmt.Sprintf("%.0fmV", gpu.Voltage))
	}

	return []string{
		title,
		kvRow("Vendor", vendor, "GFX", gpu.GfxVersion),
		kvRow("VBIOS", gpu.Vbios, "PCIe", strings.TrimSpace(pcieVal)),
		kvRow("Memory", strings.TrimSpace(memVal), "Max Power", fmt.Sprintf("%.0fW", gpu.PowerMax)),
		kvRow("Driver", gpu.DriverVersion, "Perf", perfLevel),
		throttleLine,
		kvRow("Unique ID", gpu.UniqueID, "SKU", gpu.SKU),
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
