//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// Wrong struct layout produces plausible garbage, not errors — pin the
// amd64 ABI here (same pattern as the D3DKMT sizeof test in B1).
func TestPdhStructLayout(t *testing.T) {
	if got := unsafe.Sizeof(pdhFmtCountervalue{}); got != 16 {
		t.Errorf("sizeof PDH_FMT_COUNTERVALUE = %d, want 16", got)
	}
	if got := unsafe.Offsetof(pdhFmtCountervalue{}.DoubleValue); got != 8 {
		t.Errorf("offsetof DoubleValue = %d, want 8", got)
	}
	if got := unsafe.Sizeof(pdhFmtCountervalueItemW{}); got != 24 {
		t.Errorf("sizeof PDH_FMT_COUNTERVALUE_ITEM_W = %d, want 24", got)
	}
	if got := unsafe.Offsetof(pdhFmtCountervalueItemW{}.FmtValue); got != 8 {
		t.Errorf("offsetof FmtValue = %d, want 8", got)
	}
}
