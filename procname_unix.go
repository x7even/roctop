//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
)

func procName(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}
