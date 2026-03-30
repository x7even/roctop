package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func detectBackends() []GpuBackend {
	var backends []GpuBackend
	claimedPCI := make(map[string]bool)

	if _, err := exec.LookPath("rocm-smi"); err == nil {
		b := &rocmBackend{}
		gpus, _ := b.CollectData()
		for _, g := range gpus {
			if g.PcieBus != "" {
				claimedPCI[normalizePCI(g.PcieBus)] = true
			}
		}
		backends = append(backends, b)
	}

	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		b := &nvidiaBackend{}
		gpus, _ := b.CollectData()
		for _, g := range gpus {
			if g.PcieBus != "" {
				claimedPCI[normalizePCI(g.PcieBus)] = true
			}
		}
		backends = append(backends, b)
	}

	if sysfs := newSysfsBackend(claimedPCI); sysfs != nil {
		backends = append(backends, sysfs)
	}

	return backends
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

	activeBackends = detectBackends()
	if len(activeBackends) == 0 {
		fmt.Fprintln(os.Stderr, "error: no supported GPUs found.")
		fmt.Fprintln(os.Stderr, "roctop requires ROCm (rocm-smi), NVIDIA (nvidia-smi), or a compatible GPU in sysfs.")
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
