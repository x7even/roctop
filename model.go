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

type model struct {
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
}

func newModel(interval time.Duration) model {
	return model{
		histories: make(map[string]*GpuHistory),
		interval:  interval,
		focusIdx:  -1,
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

func fetchDataCmd() tea.Msg {
	gpus, procs := collectGpuData()
	return dataMsg{gpus: gpus, procs: procs}
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

	// Focus mode: single GPU at full width.
	if m.focusIdx >= 0 && m.focusIdx < len(m.gpus) {
		gpu := m.gpus[m.focusIdx]
		return renderGpuPanel(gpu, m.getHist(gpu.HistKey()), m.width, m.infoMode)
	}

	twoCol := len(m.gpus) > 1 && m.width/2 >= minColWidth

	var rows []string
	if twoCol {
		halfWidth := m.width / 2
		for i := 0; i < len(m.gpus); i += 2 {
			if i+1 < len(m.gpus) {
				left := renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), halfWidth, m.infoMode)
				right := renderGpuPanel(m.gpus[i+1], m.getHist(m.gpus[i+1].HistKey()), halfWidth, m.infoMode)
				rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
			} else {
				// Odd GPU at end gets full width.
				rows = append(rows, renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), m.width, m.infoMode))
			}
		}
	} else {
		for i := range m.gpus {
			rows = append(rows, renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), m.width, m.infoMode))
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
	return tea.Batch(
		fetchDataCmd,
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

	case dataMsg:
		if len(msg.gpus) == 0 {
			// Fetch failed or returned nothing — keep existing data and
			// flag it as stale so the header can warn the user.
			m.dataStale = true
		} else {
			m.dataStale = false
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
				}
				if !math.IsNaN(gpu.TempJunc) {
					h.TempJnc.Push(gpu.TempJunc)
				}
				if prev, ok := byKey[key]; ok {
					carryStaticFields(&prev, gpu)
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
		if m.paused {
			return m, tickCmd(m.interval)
		}
		return m, tea.Batch(fetchDataCmd, tickCmd(m.interval))

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, fetchDataCmd
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
			if m.focusIdx >= 0 {
				m.focusIdx = -1
				if m.vpReady {
					m.setViewportContent()
				}
			}
		case "left":
			if m.focusIdx >= 0 && len(m.gpus) > 0 {
				m.focusIdx = (m.focusIdx - 1 + len(m.gpus)) % len(m.gpus)
				if m.vpReady {
					m.setViewportContent()
					m.vp.GotoTop()
				}
			}
		case "right":
			if m.focusIdx >= 0 && len(m.gpus) > 0 {
				m.focusIdx = (m.focusIdx + 1) % len(m.gpus)
				if m.vpReady {
					m.setViewportContent()
					m.vp.GotoTop()
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
	if !m.ready || !m.vpReady || m.width == 0 {
		return "Loading..."
	}

	intervalSecs := m.interval.Seconds()
	header := renderHeader(len(m.gpus), intervalSecs, m.paused, m.infoMode, m.helpMode, m.logMode, m.dataStale, m.focusIdx, m.width)
	proc := renderProcessTable(m.procs, m.width)

	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), proc)
}
