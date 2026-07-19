//go:build !windows

package main

// newProcStats returns the OS-specific per-process GPU stats collector used
// by the rocm backend: DRM fdinfo on Linux and friends.
func newProcStats() procStatsCollector {
	return newFdinfoCollector()
}
