package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	helpTitleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff")).Bold(true)
	helpSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700")).Bold(true)
	helpKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700")).Bold(true)
	helpLabelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff"))
	helpDimStyle     = lipgloss.NewStyle().Faint(true)
	helpTextStyle    = lipgloss.NewStyle()
)

func renderHelp(width int) string {
	col := func(label, desc string) string {
		return helpLabelStyle.Render(fmt.Sprintf("%-10s", label)) +
			helpDimStyle.Render(" ") +
			helpTextStyle.Render(desc)
	}

	key := func(k, desc string) string {
		return helpKeyStyle.Render(fmt.Sprintf("%-10s", k)) +
			helpDimStyle.Render(" ") +
			helpTextStyle.Render(desc)
	}

	lines := []string{
		helpTitleStyle.Render("roctop help") + helpDimStyle.Render("  press ? to close"),
		"",
		helpSectionStyle.Render("── Metrics view ─────────────────────────────────────────────"),
		col("USE", "GPU compute utilisation (%)"),
		col("VRAM", "Video RAM — used / total capacity"),
		col("MACT", "Memory bus read/write activity (%)"),
		col("PWR", "Average power draw vs TDP cap (W)"),
		col("TEMP", "Junction temperature — hotspot on the die (°C)"),
		col("  e XX°", "Edge temperature — sensor at the die edge (lower than junction)"),
		col("  m XX°", "Memory temperature — GDDR/HBM sensor (when available)"),
		col("FAN", "Fan speed as % of max RPM, and raw RPM"),
		col("CLK", "Current GPU core clock speed"),
		col("MEM", "Current memory bus clock speed"),
		col("⚠ THROTTLED", "GPU is power- or thermally-throttling; reason(s) shown"),
		col("⚠ ECC", "Uncorrectable ECC/RAS hardware errors detected — see info view (i) for counts"),
		col("⚠ STALE DATA", "Last data fetch failed — values shown are from the previous cycle"),
		"",
		helpSectionStyle.Render("── Sparklines ────────────────────────────────────────────────"),
		helpTextStyle.Render("Braille-dot graphs show the last ~" + fmt.Sprintf("%d", maxHistory/2) + "s of history for USE,"),
		helpTextStyle.Render("PWR, and TEMP. Colour follows the same gradient as the bar above."),
		helpTextStyle.Render("Each character encodes two data points; three stacked rows give"),
		helpTextStyle.Render("12 vertical levels of resolution."),
		"",
		helpSectionStyle.Render("── Info view  (press i) ──────────────────────────────────────"),
		col("Vendor", "GPU manufacturer"),
		col("GFX", "Shader/compute architecture (e.g. gfx1100)"),
		col("VBIOS", "Video BIOS version string"),
		col("PCIe", "Bus address · link width (x16) · speed (GT/s) · root port"),
		col("Memory", "VRAM vendor and total capacity"),
		col("Max Power", "TDP cap in watts"),
		col("Driver", "Kernel driver / ROCm driver version"),
		col("Perf", "Performance level (auto / high / low / manual)"),
		col("Throttle", "Active throttle reasons, or \"none\""),
		col("Voltage", "Current GPU core voltage (mV)"),
		col("Unique ID", "Hardware unique identifier"),
		col("SKU", "GPU SKU / board code"),
		col("ECC Corr", "Cumulative correctable ECC/RAS errors since last driver load"),
		col("ECC Uncorr", "Cumulative uncorrectable ECC/RAS errors — hardware attention required if non-zero"),
		"",
		helpSectionStyle.Render("── Keybindings ───────────────────────────────────────────────"),
		key("q / Ctrl+C", "Quit"),
		key("i", "Toggle info / metrics view"),
		key("?", "Toggle this help panel"),
		key("l", "Toggle event log (shows rocm-smi errors and warnings)"),
		key("p", "Pause / resume live updates"),
		key("r", "Force an immediate refresh"),
		key("+ / =", "Increase refresh rate (faster, min 0.5 s)"),
		key("-", "Decrease refresh rate (slower, max 30 s)"),
		key("↑ ↓ / scroll", "Scroll GPU panels"),
	}

	return strings.Join(lines, "\n")
}
