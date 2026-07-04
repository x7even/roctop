package main

import (
	"math"
	"strconv"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	minRefresh  = 500 * time.Millisecond
	maxRefresh  = 30 * time.Second
	refreshStep = 500 * time.Millisecond
)

// Fixed heights for anchored regions.
const (
	uiHeaderH = 1
	uiProcH   = 10 // 8 content rows + 2 border rows
)

type tickMsg time.Time

type dataMsg struct {
	gpus  []GpuData
	procs []ProcessData
}

// backendsMsg delivers the result of asynchronous GPU backend detection,
// including the probe's first collection so it can seed the initial paint.
type backendsMsg struct {
	backends []GpuBackend
	gpus     []GpuData
	procs    []ProcessData
}

type model struct {
	backends      []GpuBackend
	detecting     bool  // backend detection still in flight; View shows a splash
	fatalErr      error // fatal startup error; main prints it after Run returns
	gpus          []GpuData
	procs         []ProcessData
	histories     map[string]*GpuHistory
	interval      time.Duration
	paused        bool
	infoMode      bool
	helpMode      bool
	logMode       bool
	dataStale     bool // true when the last fetch returned no data
	gpuVpOffset   int  // saved GPU scroll position while help/log is open
	focusIdx      int  // index into m.gpus of focused GPU; -1 = no focus
	width         int
	height        int
	ready         bool
	vp            viewport.Model
	vpReady       bool
	staticFetched bool

	// PCIe bandwidth rate computation.
	pciePrev     map[string][2]int64 // HistKey → [txBytes, rxBytes] from previous tick
	pcieBwLogged map[string]bool     // HistKey → true once "unsupported" has been logged
	lastDataTime time.Time
}

func newModel(interval time.Duration, backends []GpuBackend) model {
	return model{
		backends:     backends,
		histories:    make(map[string]*GpuHistory),
		interval:     interval,
		focusIdx:     -1,
		pciePrev:     make(map[string][2]int64),
		pcieBwLogged: make(map[string]bool),
	}
}

// ── Static fields to carry forward across refreshes ───────────────────

func carryStaticFields(from, to *GpuData) {
	if to.Vbios == "" && from.Vbios != "" {
		to.Vbios = from.Vbios
	}
	if to.MemVendor == "" && from.MemVendor != "" {
		to.MemVendor = from.MemVendor
	}
	if to.DriverVersion == "" && from.DriverVersion != "" {
		to.DriverVersion = from.DriverVersion
	}
	if to.UniqueID == "" && from.UniqueID != "" {
		to.UniqueID = from.UniqueID
	}
	if to.Vendor == "" && from.Vendor != "" {
		to.Vendor = from.Vendor
	}
	if to.SKU == "" && from.SKU != "" {
		to.SKU = from.SKU
	}
	if to.GfxVersion == "" && from.GfxVersion != "" {
		to.GfxVersion = from.GfxVersion
	}
	if to.PcieRootPort == "" && from.PcieRootPort != "" {
		to.PcieRootPort = from.PcieRootPort
	}
	if to.RasCorrectable == 0 && from.RasCorrectable != 0 {
		to.RasCorrectable = from.RasCorrectable
	}
	if to.RasUncorrectable == 0 && from.RasUncorrectable != 0 {
		to.RasUncorrectable = from.RasUncorrectable
	}
}

// ── Tea commands ──────────────────────────────────────────────────────

type staticInfoMsg []GpuData

// detectBackendsCmd runs GPU backend detection off the UI thread and
// delivers the result as a backendsMsg.
func detectBackendsCmd() tea.Cmd {
	return func() tea.Msg {
		backends, gpus, procs := detectBackends()
		return backendsMsg{backends: backends, gpus: gpus, procs: procs}
	}
}

func fetchDataCmd(backends []GpuBackend) tea.Cmd {
	return func() tea.Msg {
		gpus, procs := collectGpuData(backends)
		return dataMsg{gpus: gpus, procs: procs}
	}
}

func fetchStaticInfoCmd(gpus []GpuData) tea.Cmd {
	snapshot := make([]GpuData, len(gpus))
	copy(snapshot, gpus)
	return func() tea.Msg {
		collectStaticInfo(snapshot)
		return staticInfoMsg(snapshot)
	}
}

func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── Viewport content management ──────────────────────────────────────

// setViewportContent sets the viewport to help or GPU content depending
// on the current mode, preserving the scroll offset for GPU content.
func (m *model) setViewportContent() {
	switch {
	case m.helpMode:
		m.vp.SetContent(renderHelp(m.width))
	case m.logMode:
		m.vp.SetContent(renderLogPanel(m.width))
	default:
		m.vp.SetContent(m.renderGpuContent())
	}
}

// ── GPU content renderer ──────────────────────────────────────────────

func (m model) getHist(key string) *GpuHistory {
	if h := m.histories[key]; h != nil {
		return h
	}
	return &GpuHistory{}
}

// minColWidth is the minimum panel width (chars) required to use two columns.
// Below this threshold the layout switches to a single full-width column.
const minColWidth = 60

func (m model) renderGpuContent() string {
	if len(m.gpus) == 0 {
		return "\n  " + dimStyle.Render("Waiting for GPU data...")
	}

	showBackendTag := len(m.backends) > 1

	// Focus mode: single GPU at full width.
	if m.focusIdx >= 0 && m.focusIdx < len(m.gpus) {
		gpu := m.gpus[m.focusIdx]
		return renderGpuPanel(gpu, m.getHist(gpu.HistKey()), m.width, m.infoMode, showBackendTag)
	}

	twoCol := len(m.gpus) > 1 && m.width/2 >= minColWidth

	var rows []string
	if twoCol {
		halfWidth := m.width / 2
		for i := 0; i < len(m.gpus); i += 2 {
			if i+1 < len(m.gpus) {
				left := renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), halfWidth, m.infoMode, showBackendTag)
				right := renderGpuPanel(m.gpus[i+1], m.getHist(m.gpus[i+1].HistKey()), halfWidth, m.infoMode, showBackendTag)
				rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
			} else {
				// Odd GPU at end gets full width.
				rows = append(rows, renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), m.width, m.infoMode, showBackendTag))
			}
		}
	} else {
		for i := range m.gpus {
			rows = append(rows, renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), m.width, m.infoMode, showBackendTag))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m model) vpHeight() int {
	h := m.height - uiHeaderH - uiProcH
	if h < 1 {
		return 1
	}
	return h
}

// ── Model lifecycle ───────────────────────────────────────────────────

func (m model) Init() tea.Cmd {
	if m.detecting {
		// Detect GPUs first; the initial fetch and tick chain start when
		// the backendsMsg arrives (see Update).
		return detectBackendsCmd()
	}
	return tea.Batch(
		fetchDataCmd(m.backends),
		tickCmd(m.interval),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		vpH := m.vpHeight()
		if !m.vpReady {
			m.vp = viewport.New(m.width, vpH)
			m.vp.MouseWheelEnabled = true
			m.setViewportContent()
			m.vpReady = true
		} else {
			yOff := m.vp.YOffset
			m.vp.Width = m.width
			m.vp.Height = vpH
			m.setViewportContent()
			m.vp.SetYOffset(yOff)
		}

	case backendsMsg:
		m.detecting = false
		if len(msg.backends) == 0 {
			m.fatalErr = errNoBackends
			return m, tea.Quit
		}
		m.backends = msg.backends
		if len(msg.gpus) > 0 {
			// Reuse the detection probe's collection for the first paint
			// instead of waiting a full interval for the next fetch.
			next, cmd := m.Update(dataMsg{gpus: msg.gpus, procs: msg.procs})
			return next, tea.Batch(cmd, tickCmd(m.interval))
		}
		return m, tea.Batch(fetchDataCmd(m.backends), tickCmd(m.interval))

	case dataMsg:
		if len(msg.gpus) == 0 {
			// Fetch failed or returned nothing — keep existing data and
			// flag it as stale so the header can warn the user.
			m.dataStale = true
		} else {
			m.dataStale = false
			now := time.Now()
			elapsed := 0.0
			if !m.lastDataTime.IsZero() {
				elapsed = now.Sub(m.lastDataTime).Seconds()
			}
			m.lastDataTime = now

			byKey := make(map[string]GpuData)
			for _, g := range m.gpus {
				byKey[g.HistKey()] = g
			}
			for i := range msg.gpus {
				gpu := &msg.gpus[i]
				key := gpu.HistKey()
				if _, exists := m.histories[key]; !exists {
					m.histories[key] = &GpuHistory{}
				}
				h := m.histories[key]
				if !math.IsNaN(gpu.GpuUse) {
					h.GpuUse.Push(gpu.GpuUse)
				}
				if !math.IsNaN(gpu.PowerAvg) {
					h.Power.Push(gpu.PowerAvg)
					if gpu.PowerAvg > h.PowerPeak {
						h.PowerPeak = gpu.PowerAvg
					}
				}
				if !math.IsNaN(gpu.TempJunc) {
					h.TempJnc.Push(gpu.TempJunc)
				}
				if prev, ok := byKey[key]; ok {
					carryStaticFields(&prev, gpu)
				}

				// PCIe bandwidth rate computation — three-source priority.
				//
				// Priority 1: rocm-smi cumulative counters (needs delta).
				if gpu.PcieTxBytes >= 0 && elapsed > 0 {
					if prev, ok := m.pciePrev[key]; ok {
						txDelta := gpu.PcieTxBytes - prev[0]
						rxDelta := gpu.PcieRxBytes - prev[1]
						if txDelta >= 0 && rxDelta >= 0 { // skip on counter reset/wrap
							gpu.PcieTxMBps = float64(txDelta) / elapsed / 1_000_000
							gpu.PcieRxMBps = float64(rxDelta) / elapsed / 1_000_000
						}
					}
					m.pciePrev[key] = [2]int64{gpu.PcieTxBytes, gpu.PcieRxBytes}
				}
				// Priority 2: pcie_bw sysfs (kernel resets on each read; already a
				// per-interval delta — divide by elapsed for MB/s).
				if math.IsNaN(gpu.PcieTxMBps) && gpu.PcieBwTxDelta >= 0 && elapsed > 0 {
					gpu.PcieTxMBps = float64(gpu.PcieBwTxDelta) / elapsed / 1_000_000
					gpu.PcieRxMBps = float64(gpu.PcieBwRxDelta) / elapsed / 1_000_000
				}
				// Priority 3: gpu_metrics combined rate — already set as PcieTxMBps
				// by the sysfs backend; PcieRxMBps stays NaN → panel shows "BW".

				// If no source provided data after the first tick, log once.
				if math.IsNaN(gpu.PcieTxMBps) && elapsed > 0 && !m.pcieBwLogged[key] {
					m.pcieBwLogged[key] = true
					logf("PCIe TX/RX unsupported by %s", gpu.Name)
				}

				// Push computed rates (or sysfs combined value) to history
				// and track all-time peaks.
				if !math.IsNaN(gpu.PcieTxMBps) {
					h.PcieTx.Push(gpu.PcieTxMBps)
					if gpu.PcieTxMBps > h.PcieTxPeak {
						h.PcieTxPeak = gpu.PcieTxMBps
					}
				}
				if !math.IsNaN(gpu.PcieRxMBps) {
					h.PcieRx.Push(gpu.PcieRxMBps)
					if gpu.PcieRxMBps > h.PcieRxPeak {
						h.PcieRxPeak = gpu.PcieRxMBps
					}
				}
			}
			m.gpus = msg.gpus
			m.procs = msg.procs
		}
		if m.vpReady && !m.helpMode && !m.logMode {
			yOff := m.vp.YOffset
			m.vp.SetContent(m.renderGpuContent())
			m.vp.SetYOffset(yOff)
		}
		if !m.staticFetched && len(m.gpus) > 0 {
			m.staticFetched = true
			return m, fetchStaticInfoCmd(m.gpus)
		}

	case staticInfoMsg:
		byKey := make(map[string]GpuData)
		for _, g := range []GpuData(msg) {
			byKey[g.HistKey()] = g
		}
		for i := range m.gpus {
			if sg, ok := byKey[m.gpus[i].HistKey()]; ok {
				carryStaticFields(&sg, &m.gpus[i])
			}
		}
		if m.vpReady && !m.helpMode && !m.logMode {
			yOff := m.vp.YOffset
			m.vp.SetContent(m.renderGpuContent())
			m.vp.SetYOffset(yOff)
		}

	case tickMsg:
		if m.detecting {
			// No tick chain runs during detection; ignore any stray tick.
			return m, nil
		}
		if m.paused {
			return m, tickCmd(m.interval)
		}
		return m, tea.Batch(fetchDataCmd(m.backends), tickCmd(m.interval))

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			if m.detecting {
				return m, nil
			}
			return m, fetchDataCmd(m.backends)
		case "+", "=":
			m.interval -= refreshStep
			if m.interval < minRefresh {
				m.interval = minRefresh
			}
		case "-":
			m.interval += refreshStep
			if m.interval > maxRefresh {
				m.interval = maxRefresh
			}
		case "p":
			m.paused = !m.paused
		case "i":
			if m.helpMode || m.logMode {
				m.helpMode = false
				m.logMode = false
				if m.vpReady {
					m.setViewportContent()
					m.vp.SetYOffset(m.gpuVpOffset)
				}
			} else {
				m.infoMode = !m.infoMode
				if m.vpReady {
					yOff := m.vp.YOffset
					m.vp.SetContent(m.renderGpuContent())
					m.vp.SetYOffset(yOff)
				}
			}
		case "?":
			if m.helpMode {
				m.helpMode = false
				if m.vpReady {
					m.setViewportContent()
					m.vp.SetYOffset(m.gpuVpOffset)
				}
			} else {
				m.gpuVpOffset = m.vp.YOffset
				m.helpMode = true
				m.logMode = false
				if m.vpReady {
					m.vp.SetContent(renderHelp(m.width))
					m.vp.GotoTop()
				}
			}
		case "l":
			if m.logMode {
				m.logMode = false
				if m.vpReady {
					m.setViewportContent()
					m.vp.SetYOffset(m.gpuVpOffset)
				}
			} else {
				m.gpuVpOffset = m.vp.YOffset
				m.logMode = true
				m.helpMode = false
				if m.vpReady {
					m.vp.SetContent(renderLogPanel(m.width))
					m.vp.GotoBottom()
				}
			}
		case "esc":
			// Always return to the normal multi-GPU metrics screen.
			m.focusIdx = -1
			m.helpMode = false
			m.logMode = false
			m.infoMode = false
			if m.vpReady {
				m.setViewportContent()
				m.vp.SetYOffset(m.gpuVpOffset)
			}
		case "left":
			if len(m.gpus) > 0 {
				switch m.focusIdx {
				case -1:
					// Overview → focus last GPU
					m.gpuVpOffset = m.vp.YOffset
					m.focusIdx = len(m.gpus) - 1
					m.helpMode = false
					m.logMode = false
				case 0:
					// First GPU → back to overview
					m.focusIdx = -1
				default:
					m.focusIdx--
				}
				if m.vpReady {
					m.setViewportContent()
					if m.focusIdx == -1 {
						m.vp.SetYOffset(m.gpuVpOffset)
					} else {
						m.vp.GotoTop()
					}
				}
			}
		case "right":
			if len(m.gpus) > 0 {
				if m.focusIdx == -1 {
					// Overview → focus first GPU
					m.gpuVpOffset = m.vp.YOffset
					m.focusIdx = 0
					m.helpMode = false
					m.logMode = false
				} else if m.focusIdx == len(m.gpus)-1 {
					// Last GPU → back to overview
					m.focusIdx = -1
				} else {
					m.focusIdx++
				}
				if m.vpReady {
					m.setViewportContent()
					if m.focusIdx == -1 {
						m.vp.SetYOffset(m.gpuVpOffset)
					} else {
						m.vp.GotoTop()
					}
				}
			}
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx, _ := strconv.Atoi(msg.String())
			if idx < len(m.gpus) {
				if m.focusIdx == idx {
					m.focusIdx = -1 // same key toggles off
				} else {
					m.focusIdx = idx
					m.helpMode = false
					m.logMode = false
				}
				if m.vpReady {
					m.setViewportContent()
					m.vp.GotoTop()
				}
			}
		default:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) View() string {
	if m.detecting {
		return "Detecting GPUs..."
	}
	if !m.ready || !m.vpReady || m.width == 0 {
		return "Loading..."
	}

	intervalSecs := m.interval.Seconds()
	header := renderHeader(len(m.gpus), backendNames(m.backends), intervalSecs, m.paused, m.infoMode, m.helpMode, m.logMode, m.dataStale, m.focusIdx, m.width)
	proc := renderProcessTable(m.procs, m.width)

	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), proc)
}
