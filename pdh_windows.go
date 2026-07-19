//go:build windows

package main

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PDH status/format constants (pdhmsg.h / pdh.h).
const (
	pdhCstatusNoInstance = 0x800007D1 // PDH_CSTATUS_NO_INSTANCE
	pdhMoreData          = 0x800007D2 // PDH_MORE_DATA
	pdhNoData            = 0x800007D5 // PDH_NO_DATA
	pdhCstatusValidData  = 0x00000000 // PDH_CSTATUS_VALID_DATA
	pdhCstatusNewData    = 0x00000001 // PDH_CSTATUS_NEW_DATA
	pdhFmtDouble         = 0x00000200 // PDH_FMT_DOUBLE
)

var (
	modpdh                           = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArrayW = modpdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = modpdh.NewProc("PdhCloseQuery")
)

// pdhFmtCountervalue is PDH_FMT_COUNTERVALUE for amd64: DWORD CStatus at
// offset 0, 4 bytes padding, then an 8-byte union at offset 8 (we always
// request PDH_FMT_DOUBLE, so the union is read as the double member).
// sizeof = 16.
type pdhFmtCountervalue struct {
	CStatus     uint32
	_           uint32  // alignment padding before the 8-byte union
	DoubleValue float64 // union { LONG; double; LONGLONG; LPCSTR; LPCWSTR }
}

// pdhFmtCountervalueItemW is PDH_FMT_COUNTERVALUE_ITEM_W for amd64:
// LPWSTR szName at offset 0, PDH_FMT_COUNTERVALUE at offset 8. sizeof = 24.
// szName points INTO the caller-supplied buffer, so the name must be copied
// (UTF16PtrToString copies) before the buffer is reused or released.
type pdhFmtCountervalueItemW struct {
	SzName   *uint16
	FmtValue pdhFmtCountervalue
}

// pdhCollector owns a long-lived PDH query over the GPU countersets.
// One PdhCollectQueryData per tick; PDH computes rates as the diff
// against the previous collect, so the query must never be reopened
// between ticks.
type pdhCollector struct {
	mu       sync.Mutex
	query    uintptr
	engine   uintptr // \GPU Engine(*)\Utilization Percentage — mandatory
	procMem  uintptr // \GPU Process Memory(*)\Dedicated Usage — 0 when unavailable
	adapMem  uintptr // \GPU Adapter Memory(*)\Dedicated Usage — 0 when unavailable
	primedAt time.Time
	cached   pdhSample // B3 type (pdhparse.go)
	cachedAt time.Time
}

var (
	pdhOnce   sync.Once
	pdhShared *pdhCollector
)

// sharedPdh returns the process-wide collector, or nil when GPU counters
// are unavailable (no WDDM GPUs, corrupt counterset registry, pdh.dll
// missing on stripped-down SKUs). Both consumers — the wddm backend and
// the rocm per-process overlay — must treat nil as "degrade silently".
func sharedPdh() *pdhCollector {
	pdhOnce.Do(func() { pdhShared, _ = newPdhCollector() })
	return pdhShared
}

func pdhAddCounter(query uintptr, path string) (uintptr, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h uintptr
	r, _, _ := procPdhAddEnglishCounterW.Call(query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return 0, fmt.Errorf("PdhAddEnglishCounterW(%s): %#x", path, r)
	}
	return h, nil
}

// newPdhCollector opens the query, adds the counters and primes the first
// collect (rates need two collects; primedAt lets sample() enforce a sane
// minimum diff window). The engine counter is mandatory: without it there
// is nothing to monitor, so failure returns nil and callers degrade.
func newPdhCollector() (*pdhCollector, error) {
	if err := modpdh.Load(); err != nil {
		return nil, err
	}
	var q uintptr
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return nil, fmt.Errorf("PdhOpenQueryW: %#x", r)
	}
	c := &pdhCollector{query: q}
	eng, err := pdhAddCounter(q, `\GPU Engine(*)\Utilization Percentage`)
	if err != nil {
		procPdhCloseQuery.Call(q) //nolint:errcheck
		return nil, err
	}
	c.engine = eng
	// Memory counters are best-effort: older drivers lack them and the
	// engine counter alone still yields busy percentages.
	c.procMem, _ = pdhAddCounter(q, `\GPU Process Memory(*)\Dedicated Usage`)
	c.adapMem, _ = pdhAddCounter(q, `\GPU Adapter Memory(*)\Dedicated Usage`)
	if r, _, _ := procPdhCollectQueryData.Call(q); r != 0 {
		procPdhCloseQuery.Call(q) //nolint:errcheck
		return nil, fmt.Errorf("PdhCollectQueryData (prime): %#x", r)
	}
	c.primedAt = time.Now()
	return c, nil
}

// readCounterArray fetches all instances of one counter via the standard
// PDH_MORE_DATA two-call protocol. Instances whose CStatus is neither
// VALID_DATA nor NEW_DATA are skipped (instance churn: a process that
// exited between collects reports stale/invalid slots). Returns nil for a
// zero handle (counter never added).
func (c *pdhCollector) readCounterArray(counter uintptr) ([]pdhInstance, error) {
	if counter == 0 {
		return nil, nil
	}
	var bufSize, itemCount uint32
	r, _, _ := procPdhGetFormattedCounterArrayW.Call(counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&itemCount)), 0)
	switch r {
	case pdhMoreData:
		// Instances exist; fall through to the fill loop below.
	case 0, pdhCstatusNoInstance, pdhNoData:
		// A wildcard counter matching zero instances (idle GPU with no
		// process touching it) is an empty sample, not an error.
		return nil, nil
	default:
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW (size): %#x", r)
	}
	// Instances can appear between the size call and the fill call; retry
	// a few times with the grown size rather than looping forever.
	var buf []byte
	for range 4 {
		if bufSize == 0 {
			return nil, nil
		}
		buf = make([]byte, bufSize)
		r, _, _ = procPdhGetFormattedCounterArrayW.Call(counter, pdhFmtDouble,
			uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&itemCount)),
			uintptr(unsafe.Pointer(&buf[0])))
		if r != pdhMoreData {
			break
		}
	}
	if r != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW: %#x", r)
	}
	if itemCount == 0 {
		return nil, nil
	}
	items := unsafe.Slice((*pdhFmtCountervalueItemW)(unsafe.Pointer(&buf[0])), itemCount)
	out := make([]pdhInstance, 0, itemCount)
	for i := range items {
		it := &items[i]
		if it.FmtValue.CStatus != pdhCstatusValidData && it.FmtValue.CStatus != pdhCstatusNewData {
			continue
		}
		// UTF16PtrToString copies out of buf; the pointer itself only
		// stays valid while buf is alive.
		out = append(out, pdhInstance{Name: windows.UTF16PtrToString(it.SzName), Val: it.FmtValue.DoubleValue})
	}
	return out, nil
}

// sample collects one snapshot. The 250ms cache exists because two
// consumers (wddm backend + rocm overlay) sample every tick: back-to-back
// PdhCollectQueryData calls would give PDH a ~0-width diff window and
// garbage rates. The 500ms floor after priming makes rate counters valid
// for --once; in the TUI the first tick already arrives later, so the
// sleep is free.
func (c *pdhCollector) sample() (pdhSample, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.cachedAt.IsZero() && time.Since(c.cachedAt) < 250*time.Millisecond {
		return c.cached, nil
	}
	if wait := 500*time.Millisecond - time.Since(c.primedAt); wait > 0 {
		time.Sleep(wait)
	}
	if r, _, _ := procPdhCollectQueryData.Call(c.query); r != 0 {
		return pdhSample{}, fmt.Errorf("PdhCollectQueryData: %#x", r)
	}
	engine, err := c.readCounterArray(c.engine)
	if err != nil {
		return pdhSample{}, err
	}
	procMem, _ := c.readCounterArray(c.procMem) // best-effort, like the add
	adapMem, _ := c.readCounterArray(c.adapMem)

	s := pdhSample{Engine: engine, ProcMem: procMem, AdapterMem: adapMem}
	c.cached, c.cachedAt = s, time.Now()
	return s, nil
}
