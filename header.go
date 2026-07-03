package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	headerCyan  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00d7ff")).Bold(true)
	headerGreen = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00d700"))
	headerDim   = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#a0c8e8")).Faint(true)
	headerKey   = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ffd700")).Bold(true)
	headerPause = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ffd700")).Bold(true)
	headerInfo  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ff00ff")).Bold(true)
	headerHelp  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00ff87")).Bold(true)
	headerStale = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ff8700")).Bold(true)
	headerLog   = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ff8700")).Bold(true)
)

func renderHeader(gpuCount int, backendStr string, refreshSecs float64, paused, infoMode, helpMode, logMode, dataStale bool, focusIdx int, width int) string {
	var sb strings.Builder

	if paused {
		sb.WriteString(headerPause.Render(" ⏸  PAUSED"))
		sb.WriteString(headerDim.Render(" — press "))
		sb.WriteString(headerKey.Render("p"))
		sb.WriteString(headerDim.Render(" to resume"))
	} else {
		sb.WriteString(headerCyan.Render("roctop"))
		sb.WriteString(headerDim.Render(" " + version + " [" + backendStr + "]"))
		sb.WriteString(headerGreen.Render(fmt.Sprintf("  %d GPU", gpuCount)))
		if gpuCount != 1 {
			sb.WriteString(headerGreen.Render("s"))
		}
		switch {
		case helpMode:
			sb.WriteString(headerHelp.Render("  HELP"))
		case logMode:
			sb.WriteString(headerLog.Render("  LOG"))
		case focusIdx >= 0:
			sb.WriteString(headerInfo.Render(fmt.Sprintf("  FOCUS GPU %d", focusIdx)))
		case infoMode:
			sb.WriteString(headerInfo.Render("  INFO MODE"))
		default:
			sb.WriteString(headerDim.Render(fmt.Sprintf("  refresh %.1fs", refreshSecs)))
		}
		if dataStale {
			sb.WriteString(headerStale.Render("  ⚠ STALE DATA"))
		}
		logCount := len(getLogEntries())
		sb.WriteString(headerKey.Render("  q"))
		sb.WriteString(headerDim.Render(":quit  "))
		sb.WriteString(headerKey.Render("+"))
		sb.WriteString(headerKey.Render("/"))
		sb.WriteString(headerKey.Render("-"))
		sb.WriteString(headerDim.Render(":speed  "))
		sb.WriteString(headerKey.Render("r"))
		sb.WriteString(headerDim.Render(":refresh  "))
		sb.WriteString(headerKey.Render("p"))
		sb.WriteString(headerDim.Render(":pause  "))
		sb.WriteString(headerKey.Render("i"))
		sb.WriteString(headerDim.Render(":info  "))
		sb.WriteString(headerKey.Render("?"))
		sb.WriteString(headerDim.Render(":help  "))
		sb.WriteString(headerKey.Render("l"))
		if logCount > 0 {
			sb.WriteString(headerLog.Render(fmt.Sprintf(":log(%d)", logCount)))
		} else {
			sb.WriteString(headerDim.Render(":log"))
		}
		// Arrow-key cycle hint — only shown when it fits.
		arrowHint := headerKey.Render("  ←→") + headerDim.Render(":cycle")
		if lipgloss.Width(sb.String())+lipgloss.Width(arrowHint) <= width {
			sb.WriteString(arrowHint)
		}
	}

	line := sb.String()
	return lipgloss.NewStyle().
		Background(lipgloss.Color("#0d1a2d")).
		Width(width).
		Render(line)
}
