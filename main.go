package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// version is injected at build time via ldflags: -X main.version=x.y.z
var version = "dev"

func main() {
	refreshSecs := flag.Float64("refresh", 2.0, "Refresh interval in seconds (default: 2.0)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("roctop", version)
		os.Exit(0)
	}

	if _, err := exec.LookPath("rocm-smi"); err != nil {
		fmt.Fprintln(os.Stderr, "error: rocm-smi not found on PATH.")
		fmt.Fprintln(os.Stderr, "roctop requires ROCm to be installed. See https://rocm.docs.amd.com/")
		os.Exit(1)
	}

	interval := time.Duration(*refreshSecs * float64(time.Second))
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}

	p := tea.NewProgram(
		newModel(interval),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
