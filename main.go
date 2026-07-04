package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// errNoBackends is stored on the model as fatalErr when detection finds no
// usable GPUs; main prints the full message to stderr after the program exits.
var errNoBackends = errors.New("no supported GPUs found")

// detectBackends probes for available GPU backends. A backend is registered
// only when its probe finds at least one GPU, so a tool that is installed
// but has no devices does not cost dead execs on every refresh. The probe's
// first collection is returned alongside the backends so the initial paint
// does not have to wait for another fetch.
func detectBackends() ([]GpuBackend, []GpuData, []ProcessData) {
	var backends []GpuBackend
	var allGpus []GpuData
	var allProcs []ProcessData
	claimedPCI := make(map[string]bool)

	if _, err := exec.LookPath("rocm-smi"); err == nil {
		b := newRocmBackend()
		if len(b.cards) == 0 {
			logf("warning: rocm-smi found but returned no GPUs; skipping rocm backend")
		} else {
			for _, c := range b.cards {
				if c.identity.PcieBus != "" {
					claimedPCI[normalizePCI(c.identity.PcieBus)] = true
				}
			}
			backends = append(backends, b)
			gpus, procs := b.CollectData()
			allGpus = append(allGpus, gpus...)
			allProcs = append(allProcs, procs...)
		}
	}

	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		b := &nvidiaBackend{}
		gpus, procs := b.CollectData()
		if len(gpus) == 0 {
			logf("warning: nvidia-smi found but returned no GPUs; skipping nvidia backend")
		} else {
			for _, g := range gpus {
				if g.PcieBus != "" {
					claimedPCI[normalizePCI(g.PcieBus)] = true
				}
			}
			backends = append(backends, b)
			allGpus = append(allGpus, gpus...)
			allProcs = append(allProcs, procs...)
		}
	}

	if sysfs := newSysfsBackend(claimedPCI); sysfs != nil {
		backends = append(backends, sysfs)
		gpus, procs := sysfs.CollectData()
		allGpus = append(allGpus, gpus...)
		allProcs = append(allProcs, procs...)
	}

	allGpus, allProcs = sortAndMergeGpuData(allGpus, allProcs)
	return backends, allGpus, allProcs
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

	interval := time.Duration(*refreshSecs * float64(time.Second))
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}

	// Backend detection runs asynchronously inside the bubbletea lifecycle
	// (see detectBackendsCmd) so the UI appears immediately.
	m := newModel(interval, nil)
	m.detecting = true

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if fm, ok := finalModel.(model); ok && fm.fatalErr != nil {
		if errors.Is(fm.fatalErr, errNoBackends) {
			fmt.Fprintln(os.Stderr, "error: no supported GPUs found.")
			fmt.Fprintln(os.Stderr, "roctop requires ROCm (rocm-smi), NVIDIA (nvidia-smi), or a compatible GPU in sysfs.")
		} else {
			fmt.Fprintln(os.Stderr, "error:", fm.fatalErr)
		}
		os.Exit(1)
	}
}
