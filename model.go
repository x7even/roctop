package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	minRefresh  = 500 * time.Millisecond
	maxRefresh  = 30 * time.Second
	refreshStep = 500 * time.Millisecond
)

type tickMsg time.Time

type dataMsg struct {
	gpus  []GpuData
	procs []ProcessData
}

type model struct {
	gpus      []GpuData
	procs     []ProcessData
	histories map[int]*GpuHistory
	interval  time.Duration
	paused    bool
	infoMode  bool
	width     int
	height    int
	ready     bool
}

func newModel(interval time.Duration) model {
	return model{
		histories: make(map[int]*GpuHistory),
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

	case dataMsg:
		byID := make(map[int]GpuData)
		for _, g := range m.gpus {
			byID[g.CardID] = g
		}

		for i := range msg.gpus {
			gpu := &msg.gpus[i]
			if _, exists := m.histories[gpu.CardID]; !exists {
				m.histories[gpu.CardID] = &GpuHistory{}
			}
			h := m.histories[gpu.CardID]
			h.GpuUse.Push(gpu.GpuUse)
			h.Power.Push(gpu.PowerAvg)
			h.TempJnc.Push(gpu.TempJunc)

			if prev, ok := byID[gpu.CardID]; ok {
				carryStaticFields(&prev, gpu)
			}
		}
		m.gpus = msg.gpus
		m.procs = msg.procs

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
		}
	}

	return m, nil
}

func (m model) View() string {
	if !m.ready || m.width == 0 {
		return "Loading..."
	}

	width := m.width

	// Calculate layout heights
	headerH := 1
	procH := 5 + 2  // 5 content rows + 2 border
	gpuPanelH := 13 // 11 content rows + 2 border
	availH := m.height - headerH - procH
	maxPanels := availH / gpuPanelH
	if maxPanels < 1 {
		maxPanels = 1
	}

	var sections []string

	// Header
	intervalSecs := m.interval.Seconds()
	sections = append(sections, renderHeader(len(m.gpus), intervalSecs, m.paused, m.infoMode, width))

	// GPU panels — 2-column grid
	getHist := func(id int) *GpuHistory {
		if h := m.histories[id]; h != nil {
			return h
		}
		return &GpuHistory{}
	}
	halfWidth := width / 2
	for i := 0; i < len(m.gpus); i += 2 {
		if i+1 < len(m.gpus) {
			left := renderGpuPanel(m.gpus[i], getHist(m.gpus[i].CardID), halfWidth, m.infoMode)
			right := renderGpuPanel(m.gpus[i+1], getHist(m.gpus[i+1].CardID), halfWidth, m.infoMode)
			sections = append(sections, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
		} else {
			sections = append(sections, renderGpuPanel(m.gpus[i], getHist(m.gpus[i].CardID), width, m.infoMode))
		}
	}

	// Process table
	sections = append(sections, renderProcessTable(m.procs, width))

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}
