//go:build windows

package main

// fallbackBackends returns the OS-specific catch-all backends for GPUs not
// claimed by a CLI-tool backend. On Windows that is the PDH/WDDM backend.
func fallbackBackends(claimedPCI map[string]bool) []GpuBackend {
	var backends []GpuBackend
	if b := newPdhBackend(claimedPCI); b != nil {
		backends = append(backends, b)
	}
	return backends
}

// noBackendsHint is the second line of the "no GPUs" message.
const noBackendsHint = "roctop requires ROCm (amd-smi), NVIDIA (nvidia-smi), or a WDDM GPU with performance counters."
