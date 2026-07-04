package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	procBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#1a3a5a")).
			Background(lipgloss.Color("#0d0d18")).
			PaddingLeft(1).
			PaddingRight(1)

	procHeader = lipgloss.NewStyle().Faint(true).Bold(true)
	procPID    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00af00"))
	procGPU    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff"))
	procVRAM   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd700"))
	procTitle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff")).Bold(true)
)

func fmtBytes(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	}
	if b >= 1<<20 {
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	}
	return fmt.Sprintf("%d KB", b/1024)
}

func renderProcessTable(procs []ProcessData, width int) string {
	var lines []string
	lines = append(lines, procTitle.Render("Processes"))
	lines = append(lines,
		procHeader.Render(fmt.Sprintf("%-8s %-20s %-12s %10s", "PID", "Name", "GPUs", "VRAM")),
	)

	const maxShown = 6

	if len(procs) == 0 {
		lines = append(lines, dimStyle.Render("  no GPU processes"))
	} else {
		shown := procs
		if len(shown) > maxShown {
			// Reserve one row for the "+ N more" line so the panel
			// never exceeds its fixed 8-row content height.
			shown = shown[:maxShown-1]
		}
		for _, p := range shown {
			sort.Ints(p.GpuIDs)
			gpuStr := "?"
			if len(p.GpuIDs) > 0 {
				parts := make([]string, len(p.GpuIDs))
				for i, g := range p.GpuIDs {
					parts[i] = strconv.Itoa(g)
				}
				gpuStr = strings.Join(parts, ",")
			}
			name := p.Name
			if len([]rune(name)) > 19 {
				name = string([]rune(name)[:18]) + "…"
			}
			line := procPID.Render(fmt.Sprintf("%-8d", p.PID)) +
				fmt.Sprintf("%-20s", name) +
				procGPU.Render(fmt.Sprintf("%-12s", gpuStr)) +
				procVRAM.Render(fmt.Sprintf("%10s", fmtBytes(p.VramUsed)))
			lines = append(lines, line)
		}
		if extra := len(procs) - len(shown); extra > 0 {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  + %d more", extra)))
		}
	}

	// Pad to fixed height (8 content rows + 2 border = 10 lines total)
	// so the process panel is always anchored at a consistent position.
	for len(lines) < 8 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return procBorder.Width(width - 2).Render(content)
}
