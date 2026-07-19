package main

// Tests for the portable amd-smi path helpers. Paths are built with
// filepath.Join so the cases pass on both slash conventions.

import (
	"path/filepath"
	"testing"
)

func rocmSmiPath(version string) string {
	return filepath.Join("C:\\", "Program Files", "AMD", "ROCm", version, "bin", "amd-smi.exe")
}

func TestPickNewestAmdSmi(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"empty slice", nil, ""},
		{"single path", []string{rocmSmiPath("6.2")}, rocmSmiPath("6.2")},
		{
			"numeric beats lexicographic",
			[]string{rocmSmiPath("6.2.4"), rocmSmiPath("6.10.0")},
			rocmSmiPath("6.10.0"),
		},
		{
			"missing segment counts as lower",
			[]string{rocmSmiPath("6.2"), rocmSmiPath("6.2.1")},
			rocmSmiPath("6.2.1"),
		},
		{
			"non-numeric tie-break is deterministic",
			[]string{rocmSmiPath("6.2"), rocmSmiPath("6.2-beta")},
			rocmSmiPath("6.2-beta"),
		},
		{
			"non-numeric tie-break order-independent",
			[]string{rocmSmiPath("6.2-beta"), rocmSmiPath("6.2")},
			rocmSmiPath("6.2-beta"),
		},
	}
	for _, c := range cases {
		if got := pickNewestAmdSmi(c.paths); got != c.want {
			t.Errorf("%s: pickNewestAmdSmi(%v) = %q, want %q", c.name, c.paths, got, c.want)
		}
	}
}
