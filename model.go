package main

import (
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
	gpus      []GpuData
	procs     []ProcessData
	histories map[string]*GpuHistory
	interval  time.Duration
	paused    bool
	infoMode  bool
	width     int
	height    int
	ready     bool
	vp        viewport.Model
	vpReady   bool
}

func newModel(interval time.Duration) model {
	return model{
		histories: make(map[string]*GpuHistory),
		interval:  interval,
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
}

// ── Tea commands ──────────────────────────────────────────────────────

func fetchDataCmd() tea.Msg {
	gpus, procs := collectGpuData()
	return dataMsg{gpus: gpus, procs: procs}
}

func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── GPU content renderer ──────────────────────────────────────────────

func (m model) getHist(key string) *GpuHistory {
	if h := m.histories[key]; h != nil {
		return h
	}
	return &GpuHistory{}
}

func (m model) renderGpuContent() string {
	if len(m.gpus) == 0 {
		return "\n  " + dimStyle.Render("Waiting for GPU data...")
	}
	halfWidth := m.width / 2
	var rows []string
	for i := 0; i < len(m.gpus); i += 2 {
		if i+1 < len(m.gpus) {
			left := renderGpuPanel(m.gpus[i], m.getHist(m.gpus[i].HistKey()), halfWidth, m.infoMode)
			right := renderGpuPanel(m.gpus[i+1], m.getHist(m.gpus[i+1].HistKey()), halfWidth, m.infoMode)
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
		} else {
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
			m.vp.SetContent(m.renderGpuContent())
			m.vpReady = true
		} else {
			yOff := m.vp.YOffset
			m.vp.Width = m.width
			m.vp.Height = vpH
			m.vp.SetYOffset(yOff)
		}

	case dataMsg:
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
			h.GpuUse.Push(gpu.GpuUse)
			h.Power.Push(gpu.PowerAvg)
			h.TempJnc.Push(gpu.TempJunc)
			if prev, ok := byKey[key]; ok {
				carryStaticFields(&prev, gpu)
			}
		}
		m.gpus = msg.gpus
		m.procs = msg.procs
		if m.vpReady {
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
			m.infoMode = !m.infoMode
			if m.vpReady {
				yOff := m.vp.YOffset
				m.vp.SetContent(m.renderGpuContent())
				m.vp.SetYOffset(yOff)
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
	header := renderHeader(len(m.gpus), intervalSecs, m.paused, m.infoMode, m.width)
	proc := renderProcessTable(m.procs, m.width)

	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), proc)
}
