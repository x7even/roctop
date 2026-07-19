//go:build windows

package main

// D3DKMT struct layouts must match the DDK headers exactly — a padding
// mistake produces plausible garbage instead of an error, so the Windows
// CI runner pins every size here. Pointer-bearing structs vary with the
// word size; the expected values encode both 64- and 32-bit layouts.

import (
	"testing"
	"unsafe"
)

func TestD3DKMTStructSizes(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0)) // 8 on amd64/arm64, 4 on 386/arm
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"ntLUID", unsafe.Sizeof(ntLUID{}), 8},
		{"d3dkmtAdapterInfo", unsafe.Sizeof(d3dkmtAdapterInfo{}), 20},
		{"d3dkmtEnumAdapters2", unsafe.Sizeof(d3dkmtEnumAdapters2{}), 2 * ptr},
		{"d3dkmtQueryAdapterInfo", unsafe.Sizeof(d3dkmtQueryAdapterInfo{}), 8 + 2*ptr},
		{"d3dkmtAdapterAddress", unsafe.Sizeof(d3dkmtAdapterAddress{}), 12},
		{"d3dkmtAdapterRegistryInfo", unsafe.Sizeof(d3dkmtAdapterRegistryInfo{}), 2080},
		{"d3dkmtSegmentSizeInfo", unsafe.Sizeof(d3dkmtSegmentSizeInfo{}), 24},
		{"d3dkmtCloseAdapter", unsafe.Sizeof(d3dkmtCloseAdapter{}), 4},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("unsafe.Sizeof(%s) = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
	// LUID sits directly after the 4-byte handle — a stray 64-bit field
	// anywhere upstream would shift it and corrupt every LUID string.
	if off := unsafe.Offsetof(d3dkmtAdapterInfo{}.AdapterLuid); off != 4 {
		t.Errorf("Offsetof(d3dkmtAdapterInfo.AdapterLuid) = %d, want 4", off)
	}
}
