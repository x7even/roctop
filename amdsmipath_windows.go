//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// findAmdSmi locates amd-smi on Windows: PATH first (PATHEXT resolves
// .exe/.bat/.cmd), then the HIP SDK's %HIP_PATH%\bin, then the newest
// versioned install under %ProgramFiles%\AMD\ROCm. Returns "" when absent;
// PATH hits and both fallbacks are absolute, so exec never trips ErrDot.
func findAmdSmi() string {
	if path, err := exec.LookPath(amdSMI); err == nil {
		return path
	}
	if hip := os.Getenv("HIP_PATH"); hip != "" {
		p := filepath.Join(hip, "bin", "amd-smi.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		matches, _ := filepath.Glob(filepath.Join(pf, "AMD", "ROCm", "*", "bin", "amd-smi.exe"))
		return pickNewestAmdSmi(matches)
	}
	return ""
}
