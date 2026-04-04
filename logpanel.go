package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	logTimestampStyle = lipgloss.NewStyle().Faint(true)
	logMsgStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff8700"))
	logEmptyStyle     = lipgloss.NewStyle().Faint(true)
	logTitleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7ff")).Bold(true)
)

func renderLogPanel(width int) string {
	entries := getLogEntries()

	var lines []string
	lines = append(lines, logTitleStyle.Render("event log")+" "+logTimestampStyle.Render("press l to close"))
	lines = append(lines, "")

	if len(entries) == 0 {
		lines = append(lines, logEmptyStyle.Render("  No events logged."))
	} else {
		for _, e := range entries {
			ts := logTimestampStyle.Render(fmt.Sprintf("[%s]", e.ts.Format("15:04:05")))
			msg := logMsgStyle.Render(e.msg)
			lines = append(lines, "  "+ts+"  "+msg)
		}
	}

	return strings.Join(lines, "\n")
}
