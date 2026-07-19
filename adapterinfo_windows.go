//go:build windows

package main

// WDDM adapter identity via flat D3DKMT exports from gdi32.dll.
//
// PDH "GPU Engine"/"GPU Process Memory" instance names embed adapter LUIDs
// (pid_<P>_luid_0x<HI>_0x<LO>_...), while the CLI backends identify GPUs by
// PCI BDF. D3DKMT is the cgo-free bridge between the two: it yields every
// adapter's LUID together with its bus address, marketing name and dedicated
// VRAM size. No COM, no DXGI — just NewLazySystemDLL("gdi32.dll") and
// pointer-sized calls, which means a layout mistake in the structs below
// fails silently with plausible garbage. Each struct cites its DDK header
// and adapterinfo_windows_test.go pins every size.
//
// Everything degrades: a missing export (D3DKMTEnumAdapters2 needs Win8+)
// or enumeration failure returns nil, a failed per-adapter query leaves the
// corresponding field zero — never a panic. All D3DKMT entry points return
// NTSTATUS where success is exactly STATUS_SUCCESS (0), so any non-zero
// status is treated as failure.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winAdapter is one WDDM adapter as seen by both PDH (luid) and the CLI
// backends (bdf).
type winAdapter struct {
	luid      string // lowercased "0x%08x_0x%08x" HighPart_LowPart, as PDH embeds it
	bdf       string // normalized DDDD:BB:DD.F; "" when ADAPTERADDRESS failed
	name      string // adapter marketing string, e.g. "AMD Radeon RX 9070 XT"
	vramTotal int64  // dedicated video memory in bytes; 0 when GETSEGMENTSIZE failed
}

// ntLUID mirrors LUID (winnt.h): {DWORD LowPart; LONG HighPart}. 8 bytes.
type ntLUID struct {
	LowPart  uint32
	HighPart int32
}

// d3dkmtAdapterInfo mirrors D3DKMT_ADAPTERINFO (d3dkmthk.h):
// {D3DKMT_HANDLE hAdapter; LUID AdapterLuid; ULONG NumOfSources;
// BOOL bPrecisePresentRegionsPreferred}. All fields 4-byte aligned;
// 20 bytes, no padding, on every architecture.
type d3dkmtAdapterInfo struct {
	HAdapter                        uint32
	AdapterLuid                     ntLUID
	NumOfSources                    uint32
	BPrecisePresentRegionsPreferred uint32
}

// d3dkmtEnumAdapters2 mirrors D3DKMT_ENUMADAPTERS2 (d3dkmthk.h):
// {ULONG NumAdapters; D3DKMT_ADAPTERINFO *pAdapters}. Go inserts the same
// pre-pointer padding MSVC does (16 bytes on amd64, 8 on 386).
type d3dkmtEnumAdapters2 struct {
	NumAdapters uint32
	PAdapters   *d3dkmtAdapterInfo
}

// d3dkmtQueryAdapterInfo mirrors D3DKMT_QUERYADAPTERINFO (d3dkmthk.h):
// {D3DKMT_HANDLE hAdapter; KMTQUERYADAPTERINFOTYPE Type;
// VOID *pPrivateDriverData; UINT PrivateDriverDataSize}. 24 bytes on amd64
// (trailing pad after the UINT).
type d3dkmtQueryAdapterInfo struct {
	HAdapter              uint32
	Type                  uint32
	PPrivateDriverData    unsafe.Pointer
	PrivateDriverDataSize uint32
}

// d3dkmtAdapterAddress mirrors D3DKMT_ADAPTERADDRESS (d3dkmthk.h):
// {UINT BusNumber; UINT DeviceNumber; UINT FunctionNumber}. 12 bytes.
// WDDM reports no PCI domain; domain 0 is assumed, like Task Manager.
type d3dkmtAdapterAddress struct {
	BusNumber      uint32
	DeviceNumber   uint32
	FunctionNumber uint32
}

// maxPath is MAX_PATH (minwindef.h).
const maxPath = 260

// d3dkmtAdapterRegistryInfo mirrors D3DKMT_ADAPTERREGISTRYINFO
// (d3dkmthk.h): four WCHAR[MAX_PATH] arrays. 2080 bytes.
type d3dkmtAdapterRegistryInfo struct {
	AdapterString [maxPath]uint16
	BiosString    [maxPath]uint16
	DacType       [maxPath]uint16
	ChipType      [maxPath]uint16
}

// d3dkmtSegmentSizeInfo mirrors D3DKMT_SEGMENTSIZEINFO (d3dkmthk.h):
// three D3DKMT_ALIGN64 ULONGLONG fields. 24 bytes.
type d3dkmtSegmentSizeInfo struct {
	DedicatedVideoMemorySize  uint64
	DedicatedSystemMemorySize uint64
	SharedSystemMemorySize    uint64
}

// d3dkmtCloseAdapter mirrors D3DKMT_CLOSEADAPTER (d3dkmthk.h):
// {D3DKMT_HANDLE hAdapter}. 4 bytes.
type d3dkmtCloseAdapter struct {
	HAdapter uint32
}

// KMTQUERYADAPTERINFOTYPE values (d3dkmthk.h).
const (
	kmtqaiGetSegmentSize      = 3 // KMTQAITYPE_GETSEGMENTSIZE
	kmtqaiAdapterAddress      = 6 // KMTQAITYPE_ADAPTERADDRESS
	kmtqaiAdapterRegistryInfo = 8 // KMTQAITYPE_ADAPTERREGISTRYINFO
)

var (
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procEnumAdapters2    = gdi32.NewProc("D3DKMTEnumAdapters2")
	procQueryAdapterInfo = gdi32.NewProc("D3DKMTQueryAdapterInfo")
	procCloseAdapter     = gdi32.NewProc("D3DKMTCloseAdapter")
)

// d3dkmtQuery wraps D3DKMTQueryAdapterInfo for one info type. Returns
// false on any non-zero NTSTATUS, leaving *out untouched by convention.
func d3dkmtQuery(hAdapter, typ uint32, out unsafe.Pointer, size uintptr) bool {
	q := d3dkmtQueryAdapterInfo{
		HAdapter:              hAdapter,
		Type:                  typ,
		PPrivateDriverData:    out,
		PrivateDriverDataSize: uint32(size),
	}
	st, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&q)))
	return uint32(st) == 0
}

// enumWinAdapters lists WDDM adapters with their LUID, PCI address, name
// and dedicated VRAM. Returns nil when D3DKMT is unavailable (pre-Win8
// gdi32 lacks D3DKMTEnumAdapters2) or enumeration fails; individual query
// failures degrade per adapter instead. Software adapters (WARP, remoting
// stubs) are filtered out.
func enumWinAdapters() []winAdapter {
	if procEnumAdapters2.Find() != nil ||
		procQueryAdapterInfo.Find() != nil ||
		procCloseAdapter.Find() != nil {
		return nil
	}

	// Two-call protocol: nil pAdapters asks only for the adapter count.
	var enum d3dkmtEnumAdapters2
	if st, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&enum))); uint32(st) != 0 {
		return nil
	}
	if enum.NumAdapters == 0 {
		return nil
	}
	infos := make([]d3dkmtAdapterInfo, enum.NumAdapters)
	enum.PAdapters = &infos[0]
	if st, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&enum))); uint32(st) != 0 {
		return nil
	}
	infos = infos[:enum.NumAdapters]

	var out []winAdapter
	for i := range infos {
		info := &infos[i]
		a := winAdapter{
			// PDH embeds LUIDs as lowercase zero-padded hex; %08x matches.
			luid: fmt.Sprintf("0x%08x_0x%08x",
				uint32(info.AdapterLuid.HighPart), info.AdapterLuid.LowPart),
		}
		var addr d3dkmtAdapterAddress
		if d3dkmtQuery(info.HAdapter, kmtqaiAdapterAddress,
			unsafe.Pointer(&addr), unsafe.Sizeof(addr)) {
			a.bdf = normalizePCI(fmtBDF(0, int64(addr.BusNumber),
				int64(addr.DeviceNumber), int64(addr.FunctionNumber)))
		}
		var reg d3dkmtAdapterRegistryInfo
		if d3dkmtQuery(info.HAdapter, kmtqaiAdapterRegistryInfo,
			unsafe.Pointer(&reg), unsafe.Sizeof(reg)) {
			a.name = windows.UTF16ToString(reg.AdapterString[:])
		}
		var seg d3dkmtSegmentSizeInfo
		if d3dkmtQuery(info.HAdapter, kmtqaiGetSegmentSize,
			unsafe.Pointer(&seg), unsafe.Sizeof(seg)) {
			a.vramTotal = int64(seg.DedicatedVideoMemorySize)
		}

		// EnumAdapters2 hands back open handles; release every one, even
		// for adapters the filters below drop.
		closeArg := d3dkmtCloseAdapter{HAdapter: info.HAdapter}
		_, _, _ = procCloseAdapter.Call(uintptr(unsafe.Pointer(&closeArg)))

		// Software adapters carry no telemetry worth monitoring: skip the
		// WARP rasterizer by name, and anything with neither dedicated
		// VRAM nor a bus address (indirect-display / remoting stubs).
		if a.name == "Microsoft Basic Render Driver" {
			continue
		}
		if a.vramTotal == 0 && a.bdf == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}
