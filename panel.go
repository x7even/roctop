package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const sparkIndent = 6
const minBarW = 8

var (
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#d75f5f")).Bold(true)
	labelStyle  = lipgloss.NewStyle().Faint(true)
	boldStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	cyanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff")).Bold(true)
	noDataStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5a1a1a"))
)

var panelBorder = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#1a3a5a")).
	PaddingLeft(1).
	PaddingRight(1)

const panelLines = 16 // 15 base content rows + 1 blank gap; optional GTT/PCIe rows extend beyond this

func renderGpuPanel(gpu GpuData, hist *GpuHistory, width int, infoMode bool, showBackendTag bool) string {
	// content width = panel width - 2 (border) - 2 (padding)
	cw := width - 4
	if cw < 20 {
		cw = 20
	}

	var lines []string
	if infoMode {
		lines = renderInfoLines(gpu, cw, showBackendTag)
	} else {
		lines = renderMetricLines(gpu, hist, cw, showBackendTag)
	}
	for len(lines) < panelLines {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelBorder.Width(width - 2).Render(content)
}

// ── Metrics view ──────────────────────────────────────────────────────

func renderMetricLines(gpu GpuData, hist *GpuHistory, cw int, showBackendTag bool) []string {
	const labelW = 5
	const pctW = 7 // " 100.0%"

	sparkW := max(8, cw-sparkIndent)

	bw := func(suffixLen int) int {
		v := cw - labelW - suffixLen
		if v < 8 {
			return 8
		}
		return v
	}

	blankPfx := strings.Repeat(" ", sparkIndent)

	// Title
	title := cyanStyle.Render(fmt.Sprintf("GPU %d", gpu.CardID))
	if gpu.Backend != "" && showBackendTag {
		title += dimStyle.Render(fmt.Sprintf(" [%s]", gpu.Backend))
	}
	title += " · " + boldStyle.Render(gpu.Name)
	if gpu.ThrottleStatus != 0 {
		reasons := "UNKNOWN"
		if len(gpu.ThrottleReasons) > 0 {
			reasons = strings.Join(gpu.ThrottleReasons, ", ")
		}
		title += "  " + warnStyle.Render("⚠ THROTTLED: "+reasons)
	}

	// USE bar + sparkline
	var useLine string
	var useRows [3]string
	var useLabel string
	if math.IsNaN(gpu.GpuUse) {
		useLine = labelStyle.Render("USE  ") +
			renderBar(0, 100, bw(pctW)) +
			noDataStyle.Render(fmt.Sprintf(" %5.1f%%", 0.0))
		useLabel = noDataStyle.Render(fmt.Sprintf("%4.0f%% ", 0.0))
	} else {
		useLine = labelStyle.Render("USE  ") +
			renderBar(gpu.GpuUse, 100, bw(pctW)) +
			boldStyle.Render(fmt.Sprintf(" %5.1f%%", gpu.GpuUse))
		rows := renderMultilineSparkline(hist.GpuUse.Values(), sparkW, 3, 0, 100, 100)
		copy(useRows[:], rows)
		useLabel = dimStyle.Render(fmt.Sprintf("%4.0f%% ", gpu.GpuUse))
	}

	// VRAM bar
	gbInfo := fmt.Sprintf(" %s/%s", fmtGB(gpu.VramUsed), fmtGB(gpu.VramTotal))
	vramLine := labelStyle.Render("VRAM ") +
		renderBar(gpu.VramPercent, 100, bw(pctW+len(gbInfo))) +
		boldStyle.Render(fmt.Sprintf(" %5.1f%%", gpu.VramPercent)) +
		dimStyle.Render(gbInfo)

	// GTT bar — only shown on iGPU/APU (VRAM < 4 GiB), where GTT is the primary
	// memory pool. Discrete cards always report non-zero GTT used (driver overhead)
	// and a large system-RAM-sized ceiling, both of which are misleading noise.
	var gttLine string
	if gpu.GttTotal > 0 && gpu.VramTotal < 4<<30 {
		gttInfo := fmt.Sprintf(" %s/%s", fmtGB(gpu.GttUsed), fmtGB(gpu.GttTotal))
		gttLine = labelStyle.Render("GTT  ") +
			renderBar(gpu.GttPercent, 100, bw(pctW+len(gttInfo))) +
			boldStyle.Render(fmt.Sprintf(" %5.1f%%", gpu.GttPercent)) +
			dimStyle.Render(gttInfo)
	}

	// MACT bar — memory read/write activity %
	memAct := gpu.MemActivity
	if math.IsNaN(memAct) {
		memAct = 0
	}
	mactValStyle := boldStyle
	if math.IsNaN(gpu.MemActivity) {
		mactValStyle = noDataStyle
	}
	mactLine := labelStyle.Render("MACT ") +
		renderBar(memAct, 100, bw(pctW)) +
		mactValStyle.Render(fmt.Sprintf(" %5.1f%%", memAct))

	// PWR bar + sparkline
	var pwrLine string
	var pwrRows [3]string
	var pwrLabel string
	pwrAvg := gpu.PowerAvg
	pwrMax := gpu.PowerMax
	if math.IsNaN(pwrAvg) {
		pwrAvg = 0
	}
	pwrMaxKnown := !math.IsNaN(pwrMax)
	if !pwrMaxKnown {
		if hist.PowerPeak > 0 {
			pwrMax = hist.PowerPeak
		} else {
			pwrMax = 30 // initial scale before any readings
		}
	}
	var pwrSfx string
	if pwrMaxKnown {
		pwrSfx = fmt.Sprintf(" %.0fW/%.0fW", pwrAvg, pwrMax)
	} else {
		pwrSfx = fmt.Sprintf(" %.0fW/~%.0fW", pwrAvg, pwrMax)
	}
	pwrValStyle := boldStyle
	if math.IsNaN(gpu.PowerAvg) {
		pwrValStyle = noDataStyle
	}
	pwrLine = labelStyle.Render("PWR  ") +
		renderBar(pwrAvg, pwrMax, bw(len(pwrSfx))) +
		pwrValStyle.Render(pwrSfx)
	if math.IsNaN(gpu.PowerAvg) {
		pwrLabel = noDataStyle.Render(fmt.Sprintf("%4.0fW ", 0.0))
	} else {
		rows := renderMultilineSparkline(hist.Power.Values(), sparkW, 3, 0, pwrMax, pwrMax)
		copy(pwrRows[:], rows)
		pwrLabel = dimStyle.Render(fmt.Sprintf("%4.0fW ", pwrAvg))
	}

	// TEMP bar — separator between metrics adapts to available width.
	// Full format uses " · " between sections; compact drops the dot to " ".
	tempVal := gpu.TempJunc
	tempValStyle := boldStyle
	if math.IsNaN(tempVal) {
		tempVal = 0
		tempValStyle = noDataStyle
	}
	sclkStr := fmtMHz(gpu.Sclk)
	mclkStr := fmtMHz(gpu.Mclk)

	sfxFull := fmt.Sprintf(" %.0f°C · FAN %.0f%% %drpm · CLK %s · MEM %s",
		tempVal, gpu.FanPercent, gpu.FanRPM, sclkStr, mclkStr)
	sfxCompact := fmt.Sprintf(" %.0f°C FAN %.0f%% %drpm CLK %s MEM %s",
		tempVal, gpu.FanPercent, gpu.FanRPM, sclkStr, mclkStr)

	sep := " · "
	sfxLen := lipgloss.Width(sfxFull)
	if cw-labelW-sfxLen < minBarW {
		sep = " "
		sfxLen = lipgloss.Width(sfxCompact)
	}
	tempLine := labelStyle.Render("TEMP ") +
		renderBar(tempVal, 110, max(minBarW, cw-labelW-sfxLen)) +
		tempValStyle.Render(fmt.Sprintf(" %.0f°C", tempVal)) +
		dimStyle.Render(sep+"FAN ") +
		boldStyle.Render(fmt.Sprintf("%.0f%%", gpu.FanPercent)) +
		dimStyle.Render(fmt.Sprintf(" %drpm", gpu.FanRPM)) +
		dimStyle.Render(sep+"CLK ") +
		boldStyle.Render(sclkStr) +
		dimStyle.Render(sep+"MEM ") +
		boldStyle.Render(mclkStr)

	// TEMP sparkline — label rows show edge and memory temps when available
	var tempRows [3]string
	var tempLabel, tempLabel1, tempLabel2 string
	if !math.IsNaN(gpu.TempJunc) {
		rows := renderMultilineSparkline(hist.TempJnc.Values(), sparkW, 3, 0, 110, 110)
		copy(tempRows[:], rows)
		tempLabel = dimStyle.Render(fmt.Sprintf("%4.0f° ", gpu.TempJunc))
	} else {
		tempLabel = blankPfx
	}
	if !math.IsNaN(gpu.TempEdge) && gpu.TempEdge > 0 {
		tempLabel1 = dimStyle.Render(fmt.Sprintf("e%3.0f° ", gpu.TempEdge))
	} else {
		tempLabel1 = blankPfx
	}
	if !math.IsNaN(gpu.TempMem) && gpu.TempMem > 0 {
		tempLabel2 = dimStyle.Render(fmt.Sprintf("m%3.0f° ", gpu.TempMem))
	} else {
		tempLabel2 = blankPfx
	}

	// TUNE line — power cap and OverDrive settings in force (omitted when the
	// card reports none of them, i.e. on every non-AMD backend).
	tuneLine := renderTuneLine(gpu, cw, labelW)

	// PCIE bandwidth line (omitted when no data available).
	hasTx := !math.IsNaN(gpu.PcieTxMBps)
	hasRx := !math.IsNaN(gpu.PcieRxMBps)

	lines := []string{
		title,
		useLine,
		useLabel + useRows[0],
		blankPfx + useRows[1],
		blankPfx + useRows[2],
		vramLine,
		mactLine,
		pwrLine,
		pwrLabel + pwrRows[0],
		blankPfx + pwrRows[1],
		blankPfx + pwrRows[2],
		tempLine,
		tempLabel + tempRows[0],
		tempLabel1 + tempRows[1],
		tempLabel2 + tempRows[2],
	}

	if gttLine != "" {
		lines = append(lines, gttLine)
	}

	if tuneLine != "" {
		lines = append(lines, tuneLine)
	}

	switch {
	case hasTx && hasRx:
		cur := labelStyle.Render("PCIE ") +
			dimStyle.Render("TX ") + boldStyle.Render(fmtBandwidth(gpu.PcieTxMBps)) +
			dimStyle.Render("  RX ") + boldStyle.Render(fmtBandwidth(gpu.PcieRxMBps))
		peak := dimStyle.Render("  PEAK ") +
			dimStyle.Render("TX ") + boldStyle.Render(fmtBandwidth(hist.PcieTxPeak)) +
			dimStyle.Render("  RX ") + boldStyle.Render(fmtBandwidth(hist.PcieRxPeak))
		if lipgloss.Width(cur+peak) <= cw {
			lines = append(lines, cur+peak)
		} else {
			lines = append(lines, cur)
			lines = append(lines, labelStyle.Render("PEAK ")+
				dimStyle.Render("TX ")+boldStyle.Render(fmtBandwidth(hist.PcieTxPeak))+
				dimStyle.Render("  RX ")+boldStyle.Render(fmtBandwidth(hist.PcieRxPeak)))
		}
	case hasTx:
		cur := labelStyle.Render("PCIE ") +
			dimStyle.Render("BW ") + boldStyle.Render(fmtBandwidth(gpu.PcieTxMBps))
		peak := dimStyle.Render("  PEAK ") +
			dimStyle.Render("BW ") + boldStyle.Render(fmtBandwidth(hist.PcieTxPeak))
		if lipgloss.Width(cur+peak) <= cw {
			lines = append(lines, cur+peak)
		} else {
			lines = append(lines, cur)
			lines = append(lines, labelStyle.Render("PEAK ")+
				dimStyle.Render("BW ")+boldStyle.Render(fmtBandwidth(hist.PcieTxPeak)))
		}
	}

	return lines
}

// ── TUNE line ─────────────────────────────────────────────────────────

// tuneSegment is one labelled value on the TUNE row. dim carries a secondary
// value (the board default, the applied offset) shown faint after the main one.
type tuneSegment struct {
	label string
	value string
	dim   string
}

// fmtOffsetMHz / fmtOffsetMV render an OverDrive offset with an explicit sign,
// so "+0MHz" and "-20mV" read unambiguously as "offset", not "clock".
func fmtOffsetMHz(mhz int) string { return fmt.Sprintf("%+dMHz", mhz) }
func fmtOffsetMV(mv int) string   { return fmt.Sprintf("%+dmV", mv) }

// tuneSegments lists the power-limit and OverDrive values worth showing for
// gpu, left to right in decreasing importance. It returns nil when the card
// reports none of them, which is how the renderer knows to drop the row.
//
// The power cap appears only when it differs from the board default: the PWR
// bar already shows the cap in force, so what the cap alone cannot tell you is
// whether something has moved it.
func tuneSegments(gpu GpuData) []tuneSegment {
	var segs []tuneSegment

	// Both values must be positive, not merely non-NaN: a zero-valued GpuData
	// (every non-AMD backend, and any caller that builds one by hand) carries
	// 0 W, which must not be reported as a 0 W board default.
	if gpu.PowerMax > 0 && gpu.PowerCapDefault > 0 &&
		math.Abs(gpu.PowerMax-gpu.PowerCapDefault) >= 1 {
		segs = append(segs, tuneSegment{
			label: "CAP",
			value: fmt.Sprintf("%.0fW", gpu.PowerMax),
			dim:   fmt.Sprintf("of %.0fW", gpu.PowerCapDefault),
		})
	}

	switch {
	case gpu.Voltage > 0:
		s := tuneSegment{label: "VDD", value: fmt.Sprintf("%.0fmV", gpu.Voltage)}
		if gpu.OD.HasV {
			s.dim = fmtOffsetMV(gpu.OD.VoltOffset)
		}
		segs = append(segs, s)
	case gpu.OD.HasV:
		// No live sensor, but the card still reports what it is offset by.
		segs = append(segs, tuneSegment{label: "VDD", value: fmtOffsetMV(gpu.OD.VoltOffset)})
	}

	if gpu.OD.HasS {
		segs = append(segs, tuneSegment{label: "CLK±", value: fmtOffsetMHz(gpu.OD.SclkOffset)})
	}
	if gpu.OD.HasM {
		segs = append(segs, tuneSegment{label: "MEM≤", value: fmtMHz(gpu.OD.MclkMax)})
	}
	return segs
}

// renderTuneLine renders the TUNE row, degrading as width runs out: the " · "
// separators collapse to single spaces, then the faint secondary values are
// dropped, then segments are dropped from the right. Returns "" when there is
// nothing to show.
func renderTuneLine(gpu GpuData, cw, labelW int) string {
	segs := tuneSegments(gpu)
	if len(segs) == 0 {
		return ""
	}
	avail := cw - labelW
	if avail < minBarW {
		return ""
	}

	// plain renders the candidate in plain text so its width can be measured
	// without styling; render then rebuilds the winning candidate styled.
	plain := func(segs []tuneSegment, sep string, withDim bool) string {
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = s.label + " " + s.value
			if withDim && s.dim != "" {
				parts[i] += " " + s.dim
			}
		}
		return strings.Join(parts, sep)
	}
	render := func(segs []tuneSegment, sep string, withDim bool) string {
		var b strings.Builder
		b.WriteString(labelStyle.Render("TUNE "))
		for i, s := range segs {
			if i > 0 {
				b.WriteString(dimStyle.Render(sep))
			}
			b.WriteString(dimStyle.Render(s.label + " "))
			b.WriteString(boldStyle.Render(s.value))
			if withDim && s.dim != "" {
				b.WriteString(dimStyle.Render(" " + s.dim))
			}
		}
		return b.String()
	}

	for n := len(segs); n >= 1; n-- {
		for _, c := range []struct {
			sep     string
			withDim bool
		}{{" · ", true}, {" ", true}, {" · ", false}, {" ", false}} {
			if lipgloss.Width(plain(segs[:n], c.sep, c.withDim)) <= avail {
				return render(segs[:n], c.sep, c.withDim)
			}
		}
	}
	return ""
}

// ── Info view ─────────────────────────────────────────────────────────

func renderInfoLines(gpu GpuData, cw int, showBackendTag bool) []string {
	colW := cw/2 - 14
	if colW < 8 {
		colW = 8
	}

	title := cyanStyle.Render(fmt.Sprintf("GPU %d", gpu.CardID))
	if gpu.Backend != "" && showBackendTag {
		title += dimStyle.Render(fmt.Sprintf(" [%s]", gpu.Backend))
	}
	title += " · " + boldStyle.Render(gpu.Name) +
		"  " + dimStyle.Render("press ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700")).Bold(true).Render("i") +
		dimStyle.Render(" to return to metrics")

	kv := func(label, value string) string {
		style := boldStyle
		if value == "" {
			value = "N/A"
			style = noDataStyle
		}
		runes := []rune(value)
		if len(runes) > colW {
			value = string(runes[:colW-1]) + "…"
		}
		return labelStyle.Render(fmt.Sprintf("%-10s", label+":")) +
			" " + style.Render(fmt.Sprintf("%-*s", colW, value))
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

	// ECC / RAS errors (ROCm only)
	var eccLine string
	if gpu.Backend == "rocm" {
		corrStr := strconv.FormatInt(gpu.RasCorrectable, 10)
		uncorrStr := strconv.FormatInt(gpu.RasUncorrectable, 10)
		if gpu.RasUncorrectable > 0 {
			eccLine = kv("ECC Corr", corrStr) + "  " +
				labelStyle.Render(fmt.Sprintf("%-10s", "ECC Uncorr:")) +
				" " + warnStyle.Render(fmt.Sprintf("%-*s", colW, "⚠ "+uncorrStr))
		} else {
			eccLine = kvRow("ECC Corr", corrStr, "ECC Uncorr", uncorrStr)
		}
	} else {
		eccLine = kvRow("ECC Corr", "", "ECC Uncorr", "")
	}

	rows := []string{
		title,
		kvRow("Vendor", vendor, "GFX", gpu.GfxVersion),
		kvRow("VBIOS", gpu.Vbios, "PCIe", strings.TrimSpace(pcieVal)),
		kvRow("Memory", strings.TrimSpace(memVal), "Pwr Cap", fmtPowerCap(gpu)),
		kvRow("Driver", gpu.DriverVersion, "Perf", perfLevel),
		throttleLine,
		kvRow("Unique ID", gpu.UniqueID, "SKU", gpu.SKU),
		eccLine,
		kvRow("Cap Range", fmtRangeW(gpu.PowerCapMin, gpu.PowerCapMax),
			"Clk Max", fmtClkMax(gpu.SclkMax, gpu.MclkMax)),
	}
	if gpu.GttTotal > 0 && gpu.VramTotal < 4<<30 {
		gttVal := fmt.Sprintf("%s/%s", fmtGB(gpu.GttUsed), fmtGB(gpu.GttTotal))
		rows = append(rows, kv("GTT Mem", gttVal))
	}
	// OverDrive block — only on cards whose driver exposes pp_od_clk_voltage.
	if gpu.OD.Avail() {
		rows = append(rows,
			kvRow("V Offset", fmtODVolt(gpu.OD), "V Range", fmtODVoltRange(gpu.OD)),
			kvRow("Clk Off", fmtODSclk(gpu.OD), "Clk Range", fmtODSclkRange(gpu.OD)),
			kvRow("Mem Clk", fmtODMclk(gpu.OD), "Mem Range", fmtODMclkRange(gpu.OD)),
		)
	}
	return rows
}

// ── Info-view formatters ──────────────────────────────────────────────
//
// Each returns "" for "unavailable", which kv renders as a faint N/A.

// fmtPowerCap renders the cap in force, noting the board default when the two
// differ — that difference is what says the card has been capped.
func fmtPowerCap(gpu GpuData) string {
	if !(gpu.PowerMax > 0) || !(gpu.PowerCapDefault > 0) ||
		math.Abs(gpu.PowerMax-gpu.PowerCapDefault) < 1 {
		return fmtWattsOrNA(gpu.PowerMax)
	}
	return fmt.Sprintf("%.0fW of %.0fW", gpu.PowerMax, gpu.PowerCapDefault)
}

func fmtRangeW(lo, hi float64) string {
	if !(lo > 0) || !(hi > 0) {
		return ""
	}
	return fmt.Sprintf("%.0f-%.0fW", lo, hi)
}

// fmtClkMax renders the two DPM ceilings, which already include any
// OverDrive memory-clock limit in force.
func fmtClkMax(sclk, mclk int) string {
	switch {
	case sclk > 0 && mclk > 0:
		return fmt.Sprintf("%d/%dMHz", sclk, mclk)
	case sclk > 0:
		return fmtMHz(sclk)
	case mclk > 0:
		return fmtMHz(mclk)
	}
	return ""
}

func fmtODVolt(t odTable) string {
	if !t.HasV {
		return ""
	}
	return fmtOffsetMV(t.VoltOffset)
}

func fmtODVoltRange(t odTable) string {
	if !t.HasV {
		return ""
	}
	return fmt.Sprintf("%d..%dmV", t.VoltLo, t.VoltHi)
}

func fmtODSclk(t odTable) string {
	if !t.HasS {
		return ""
	}
	return fmtOffsetMHz(t.SclkOffset)
}

func fmtODSclkRange(t odTable) string {
	if !t.HasS {
		return ""
	}
	return fmt.Sprintf("%d..%dMHz", t.SclkLo, t.SclkHi)
}

func fmtODMclk(t odTable) string {
	if !t.HasM {
		return ""
	}
	return fmt.Sprintf("%d-%dMHz", t.MclkMin, t.MclkMax)
}

func fmtODMclkRange(t odTable) string {
	if !t.HasM {
		return ""
	}
	return fmt.Sprintf("%d-%dMHz", t.MclkLo, t.MclkHi)
}

func fmtWattsOrNA(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	return fmt.Sprintf("%.0fW", v)
}

func fmtBandwidth(mbps float64) string {
	if mbps >= 1000 {
		return fmt.Sprintf("%.2f GB/s", mbps/1000)
	}
	return fmt.Sprintf("%.1f MB/s", mbps)
}
