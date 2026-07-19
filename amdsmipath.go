package main

// Portable helpers for locating the amd-smi binary. The OS-specific probe
// order lives in amdsmipath_unix.go / amdsmipath_windows.go; the version
// selection below is untagged so it stays covered by Linux CI.

import (
	"path/filepath"
	"strconv"
	"strings"
)

// pickNewestAmdSmi picks, from ...\ROCm\<version>\bin\amd-smi.exe glob
// matches, the path whose <version> directory component is highest by
// numeric-segment comparison ("6.10" beats "6.2"). Returns "" when there are
// no candidates.
func pickNewestAmdSmi(paths []string) string {
	best, bestVer := "", ""
	for _, p := range paths {
		ver := filepath.Base(filepath.Dir(filepath.Dir(p)))
		if best == "" || compareRocmVersions(ver, bestVer) > 0 {
			best, bestVer = p, ver
		}
	}
	return best
}

// compareRocmVersions compares dotted version strings segment-by-segment,
// numerically where both segments parse as integers and lexically otherwise.
// Missing segments count as lower ("6.2" < "6.2.1").
func compareRocmVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var sa, sb string
		if i < len(as) {
			sa = as[i]
		}
		if i < len(bs) {
			sb = bs[i]
		}
		na, errA := strconv.Atoi(sa)
		nb, errB := strconv.Atoi(sb)
		if errA == nil && errB == nil {
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(sa, sb); c != 0 {
			return c
		}
	}
	return 0
}
