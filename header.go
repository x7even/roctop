package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	headerBg    = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#a0c8e8"))
	headerCyan  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00d7ff")).Bold(true)
	headerGreen = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00d700"))
	headerDim   = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#a0c8e8")).Faint(true)
	headerKey   = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ffd700")).Bold(true)
	headerPause = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ffd700")).Bold(true)
	headerInfo  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ff00ff")).Bold(true)
	headerHelp  = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#00ff87")).Bold(true)
	headerStale = lipgloss.NewStyle().Background(lipgloss.Color("#0d1a2d")).Foreground(lipgloss.Color("#ff8700")).Bold(true)
)

func renderHeader(gpuCount int, refreshSecs float64, paused, infoMode, helpMode, dataStale bool, width int) string {
	var sb strings.Builder

	if paused {
		sb.WriteString(headerPause.Render(" ⏸  PAUSED"))
		sb.WriteString(headerDim.Render(" — press "))
		sb.WriteString(headerKey.Render("p"))
		sb.WriteString(headerDim.Render(" to resume"))
	} else {
		sb.WriteString(headerCyan.Render("roctop"))
		sb.WriteString(headerDim.Render(" " + version + " [" + backendNames() + "]"))
		sb.WriteString(headerGreen.Render(fmt.Sprintf("  %d GPU", gpuCount)))
		if gpuCount != 1 {
			sb.WriteString(headerGreen.Render("s"))
		}
		switch {
		case helpMode:
			sb.WriteString(headerHelp.Render("  HELP"))
		case infoMode:
			sb.WriteString(headerInfo.Render("  INFO MODE"))
		default:
			sb.WriteString(headerDim.Render(fmt.Sprintf("  refresh %.1fs", refreshSecs)))
		}
		if dataStale {
			sb.WriteString(headerStale.Render("  ⚠ STALE DATA"))
		}
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
		sb.WriteString(headerDim.Render(":help"))
	}

	line := sb.String()
	return lipgloss.NewStyle().
		Background(lipgloss.Color("#0d1a2d")).
		Width(width).
		Render(line)
}
