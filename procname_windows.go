//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func procName(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "unknown"
	}
	defer windows.CloseHandle(h) //nolint:errcheck
	// Long-path installs (node_modules/venv trees, WindowsApps packages)
	// exceed MAX_PATH; grow the buffer up to the 32K object-path ceiling.
	for size := uint32(windows.MAX_PATH); size <= 32768; size *= 2 {
		buf := make([]uint16, size)
		n := size
		err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n)
		if err == windows.ERROR_INSUFFICIENT_BUFFER {
			continue
		}
		if err != nil {
			return "unknown"
		}
		name := filepath.Base(windows.UTF16ToString(buf[:n]))
		if strings.EqualFold(filepath.Ext(name), ".exe") {
			name = name[:len(name)-len(".exe")]
		}
		return name
	}
	return "unknown"
}
