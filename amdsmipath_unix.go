//go:build !windows

package main

import "os/exec"

// findAmdSmi locates the amd-smi binary via PATH, returning "" when absent
// (preserves the pre-Windows-port LookPath behavior exactly).
func findAmdSmi() string {
	path, err := exec.LookPath(amdSMI)
	if err != nil {
		return ""
	}
	return path
}
