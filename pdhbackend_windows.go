//go:build windows

package main

// wddm backend: Task-Manager-style GPU telemetry for Windows GPUs that no
// CLI backend (amd-smi/nvidia-smi) claimed. Identity (LUID, PCI address,
// name, VRAM size) comes from D3DKMT (adapterinfo_windows.go); per-tick
// utilization, VRAM usage and per-process stats come from the shared PDH
// collector (pdh_windows.go) parsed by pdhparse.go. PDH exposes only
// engine busy and memory counters, so temperature/power/clock/fan stay at
// their NaN sentinels and the panels render "n/a".

import (
	"math"
	"sync"
)

// pdhBackend is the WDDM catch-all backend.
type pdhBackend struct {
	claimedPCI map[string]bool // filter reapplied on adapter re-enumeration

	mu         sync.Mutex // CollectData can run in overlapping goroutines
	adapters   []winAdapter
	luidToCard map[string]int  // lowercased LUID → CardID ordinal
	knownLuids map[string]bool // every LUID already considered; guards re-enum
}

// newPdhBackend returns the WDDM backend, or nil when GPU performance
// counters are unavailable or every adapter is already claimed by a CLI
// backend (mirrors the newSysfsBackend nil-filter idiom).
func newPdhBackend(claimedPCI map[string]bool) *pdhBackend {
	if sharedPdh() == nil {
		return nil
	}
	b := &pdhBackend{claimedPCI: claimedPCI}
	b.rebuild(enumWinAdapters())
	if len(b.adapters) == 0 {
		return nil
	}
	return b
}

// rebuild filters the enumerated adapters against claimedPCI and assigns
// CardID ordinals. Adapters without a bus address cannot be matched against
// the claimed set, so they are registered only when no CLI backend found
// GPUs (empty claimed set) — otherwise they could duplicate a claimed GPU.
func (b *pdhBackend) rebuild(all []winAdapter) {
	b.adapters = nil
	b.luidToCard = make(map[string]int)
	b.knownLuids = make(map[string]bool)
	for _, a := range all {
		b.knownLuids[a.luid] = true
		if a.bdf == "" {
			if len(b.claimedPCI) > 0 {
				continue
			}
		} else if b.claimedPCI[normalizePCI(a.bdf)] {
			continue
		}
		b.luidToCard[a.luid] = len(b.adapters)
		b.adapters = append(b.adapters, a)
	}
}

func (b *pdhBackend) Name() string { return "wddm" }

// sampleLuids collects every adapter LUID mentioned by the sample's
// instance names.
func sampleLuids(s pdhSample) map[string]bool {
	luids := make(map[string]bool)
	for _, inst := range s.Engine {
		if _, luid, _, ok := parseEngineInstance(inst.Name); ok {
			luids[luid] = true
		}
	}
	for _, inst := range s.ProcMem {
		if _, luid, ok := parseMemInstance(inst.Name); ok {
			luids[luid] = true
		}
	}
	for _, inst := range s.AdapterMem {
		if luid, ok := parseAdapterMemInstance(inst.Name); ok {
			luids[luid] = true
		}
	}
	return luids
}

func (b *pdhBackend) CollectData() ([]GpuData, []ProcessData) {
	c := sharedPdh()
	if c == nil {
		return nil, nil
	}
	s, err := c.sample()
	if err != nil {
		// Degrade to an identity-only tick: the zero sample yields NaN
		// utilization/VRAM below and an empty process list.
		logf("wddm: %v", err)
		s = pdhSample{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// The adapter set can change underneath us: a driver restart (TDR)
	// hands out fresh LUIDs, and eGPUs hotplug. When the sample mentions a
	// LUID never seen before, re-enumerate once and rebuild with the same
	// claimed filter, then proceed with the current sample — no retry
	// loop, no second sample. Sample LUIDs that remain unmapped after the
	// rebuild (claimed or software adapters) are marked known so they
	// cannot re-trigger enumeration every tick — but only when the
	// enumeration itself returned adapters; a transient D3DKMT failure
	// (nil) keeps the old set and leaves the LUIDs unknown so the next
	// tick retries.
	luids := sampleLuids(s)
	unknown := false
	for luid := range luids {
		if !b.knownLuids[luid] {
			unknown = true
			break
		}
	}
	if unknown {
		if all := enumWinAdapters(); all != nil {
			b.rebuild(all)
			for luid := range luids {
				b.knownLuids[luid] = true
			}
		}
	}

	gpus := make([]GpuData, 0, len(b.adapters))
	for i, a := range b.adapters {
		g := newGpuData(i, "wddm")
		g.Name = a.name
		g.PcieBus = a.bdf
		g.VramTotal = a.vramTotal
		g.GpuUse = pdhAdapterUse(s, a.luid)
		if v := pdhAdapterVram(s, a.luid); !math.IsNaN(v) {
			g.VramUsed = int64(v)
			if g.VramTotal > 0 {
				g.VramPercent = float64(g.VramUsed) / float64(g.VramTotal) * 100
			}
		}
		gpus = append(gpus, g)
	}
	return gpus, pdhProcesses(s, b.luidToCard, procName)
}
