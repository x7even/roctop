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

	if tool := selectAmdTool(); tool != nil {
		b := newRocmBackend(*tool)
		if len(b.cards) == 0 {
			logf("warning: %s found but returned no GPUs; skipping rocm backend", tool.name)
		} else {
			logf("rocm backend using %s", tool.name)
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

	for _, b := range fallbackBackends(claimedPCI) {
		backends = append(backends, b)
		gpus, procs := b.CollectData()
		allGpus = append(allGpus, gpus...)
		allProcs = append(allProcs, procs...)
	}

	allGpus, allProcs = sortAndMergeGpuData(allGpus, allProcs)
	return backends, allGpus, allProcs
}

// version is injected at build time via ldflags: -X main.version=x.y.z
var version = "dev"

// printNoBackends writes the standard "no GPUs" message to stderr.
func printNoBackends() {
	fmt.Fprintln(os.Stderr, "error: no supported GPUs found.")
	fmt.Fprintln(os.Stderr, noBackendsHint)
}

// runSnapshot handles --once: one detection pass (which includes exactly one
// collection), printed as plain text or JSON. Never starts bubbletea, so it
// works without a tty. Returns the process exit code.
func runSnapshot(asJSON bool) int {
	backends, gpus, procs := detectBackends()
	if len(backends) == 0 {
		printNoBackends()
		return 1
	}
	var names []string
	for _, b := range backends {
		names = append(names, b.Name())
	}
	if asJSON {
		out, err := buildSnapshotJSON(gpus, procs, names)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(renderSnapshotText(gpus, procs, names))
	}
	return 0
}

func main() {
	refreshSecs := flag.Float64("refresh", 2.0, "Refresh interval in seconds (default: 2.0)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	once := flag.Bool("once", false, "Collect once, print a plain-text snapshot to stdout, and exit")
	jsonOut := flag.Bool("json", false, "With --once, emit the snapshot as JSON")
	flag.Parse()

	if *showVersion {
		fmt.Println("roctop", version)
		os.Exit(0)
	}

	if *jsonOut && !*once {
		fmt.Fprintln(os.Stderr, "error: --json requires --once")
		os.Exit(1)
	}
	if *once {
		os.Exit(runSnapshot(*jsonOut))
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
			printNoBackends()
		} else {
			fmt.Fprintln(os.Stderr, "error:", fm.fatalErr)
		}
		os.Exit(1)
	}
}
