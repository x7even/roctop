//go:build windows

package main

// newProcStats returns the OS-specific per-process GPU stats collector used
// by the rocm backend: on Windows, the PDH per-process overlay. The
// collector degrades internally (returns nil rows) when GPU counters are
// unavailable or no adapter maps to a target GPU, and the call sites then
// fall through to the tool's own process listing.
func newProcStats() procStatsCollector {
	return pdhProcStats{}
}

// pdhProcStats attributes PDH per-process GPU counters to the rocm
// backend's GPUs by joining adapter LUIDs to PCI addresses via D3DKMT.
type pdhProcStats struct{}

func (pdhProcStats) collect(pdevToGpu map[string]int, nameFn func(int) string) []ProcessData {
	c := sharedPdh()
	if c == nil {
		return nil
	}
	adapters := enumWinAdapters()
	luidToGpu := make(map[string]int)
	for _, a := range adapters {
		if a.bdf == "" {
			continue
		}
		if gpu, ok := pdevToGpu[normalizePCI(a.bdf)]; ok {
			luidToGpu[a.luid] = gpu
		}
	}
	// An adapter without a bus address can only be attributed in the
	// unambiguous single-adapter/single-GPU case; anything else would be
	// guessing.
	if len(luidToGpu) == 0 && len(adapters) == 1 && adapters[0].bdf == "" && len(pdevToGpu) == 1 {
		for _, gpu := range pdevToGpu {
			luidToGpu[adapters[0].luid] = gpu
		}
	}
	if len(luidToGpu) == 0 {
		return nil
	}
	s, err := c.sample()
	if err != nil {
		return nil
	}
	return pdhProcesses(s, luidToGpu, nameFn)
}
