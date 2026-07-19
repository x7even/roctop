//go:build !windows

package main

// fallbackBackends returns the OS-specific catch-all backends for GPUs not
// claimed by a CLI-tool backend. On Linux and friends that is the sysfs
// backend.
func fallbackBackends(claimedPCI map[string]bool) []GpuBackend {
	var backends []GpuBackend
	if sysfs := newSysfsBackend(claimedPCI); sysfs != nil {
		backends = append(backends, sysfs)
	}
	return backends
}

// noBackendsHint is the second line of the "no GPUs" message.
const noBackendsHint = "roctop requires ROCm (rocm-smi), NVIDIA (nvidia-smi), or a compatible GPU in sysfs."
