package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func detectBackend() GpuBackend {
	if _, err := exec.LookPath("rocm-smi"); err == nil {
		return &rocmBackend{}
	}
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		return &nvidiaBackend{}
	}
	return nil
}

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

	activeBackend = detectBackend()
	if activeBackend == nil {
		fmt.Fprintln(os.Stderr, "error: no supported GPU tools found on PATH.")
		fmt.Fprintln(os.Stderr, "roctop requires either ROCm (rocm-smi) or NVIDIA (nvidia-smi) drivers.")
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
