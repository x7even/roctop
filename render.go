package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Unicode character sets ────────────────────────────────────────────

const barChars = " ▏▎▍▌▋▊▉█"

// braille encodes two vertical bar levels (0-4) per cell: braille[left*5+right]
const braille = " ⢀⢠⢰⢸⡀⣀⣠⣰⣸⡄⣄⣤⣴⣼⡆⣆⣦⣶⣾⡇⣇⣧⣷⣿"

// ── Color gradients ───────────────────────────────────────────────────

type colorStop struct {
	pos float64
	r, g, b uint8
}

type gradient [101][3]uint8

func buildGradient(stops []colorStop) gradient {
	var g gradient
	for i := 0; i <= 100; i++ {
		v := float64(i)
		for j := 0; j < len(stops)-1; j++ {
			s0, s1 := stops[j], stops[j+1]
			if v <= s1.pos {
				t := (v - s0.pos) / (s1.pos - s0.pos)
				g[i][0] = uint8(float64(s0.r) + float64(int(s1.r)-int(s0.r))*t)
				g[i][1] = uint8(float64(s0.g) + float64(int(s1.g)-int(s0.g))*t)
				g[i][2] = uint8(float64(s0.b) + float64(int(s1.b)-int(s0.b))*t)
				break
			}
		}
	}
	// ensure last stop is set
	last := stops[len(stops)-1]
	g[100] = [3]uint8{last.r, last.g, last.b}
	return g
}

var utilGradient = buildGradient([]colorStop{
	{0, 30, 100, 200},
	{30, 0, 175, 135},
	{60, 95, 215, 0},
	{75, 255, 175, 0},
	{88, 255, 95, 0},
	{100, 255, 0, 0},
})

var powerGradient = buildGradient([]colorStop{
	{0, 0, 135, 95},
	{40, 0, 215, 0},
	{65, 215, 215, 0},
	{85, 255, 95, 0},
	{100, 255, 0, 0},
})

var tempGradient = buildGradient([]colorStop{
	{0, 0, 215, 255},
	{40, 0, 215, 135},
	{60, 95, 215, 0},
	{75, 255, 175, 0},
	{88, 255, 95, 0},
	{100, 255, 0, 0},
})

func clampIdx(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return int(v)
}

func rgbStyle(r, g, b uint8) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b)))
}

// ── Bar chart renderer ────────────────────────────────────────────────

func renderBar(value, maximum float64, width int, grad gradient) string {
	if width <= 0 {
		return ""
	}
	pct := 0.0
	if maximum > 0 {
		pct = value / maximum * 100
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
	}

	c := grad[clampIdx(pct)]
	fillStyle := rgbStyle(c[0], c[1], c[2])
	emptyStyle := rgbStyle(40, 40, 40)

	barRunes := []rune(barChars)
	filled := int(pct / 100 * float64(width))
	remainder := (pct / 100 * float64(width)) - float64(filled)
	fracIdx := int(remainder * float64(len(barRunes)-1))

	var sb strings.Builder
	if filled > 0 {
		sb.WriteString(fillStyle.Render(strings.Repeat("█", filled)))
	}
	if fracIdx > 0 && filled < width {
		sb.WriteString(fillStyle.Render(string(barRunes[fracIdx])))
		empty := width - filled - 1
		if empty > 0 {
			sb.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))
		}
	} else {
		empty := width - filled
		if empty > 0 {
			sb.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))
		}
	}
	return sb.String()
}

// ── Braille sparkline renderer ────────────────────────────────────────

func normalizeLevel(value, vmin, vmax float64) int {
	if vmax <= vmin {
		return 0
	}
	t := (value - vmin) / (vmax - vmin)
	if t <= 0 {
		return 0
	}
	level := int(t * 4.9)
	if level < 1 {
		level = 1
	}
	if level > 4 {
		level = 4
	}
	return level
}

func renderSparkline(history []float64, width int, vmin, vmax float64, grad gradient, gradScale float64) string {
	if width <= 0 {
		return ""
	}

	needed := width * 2
	samples := make([]float64, needed)
	// left-pad with vmin
	if len(history) >= needed {
		copy(samples, history[len(history)-needed:])
	} else {
		copy(samples[needed-len(history):], history)
	}

	brailleRunes := []rune(braille)
	emptyStyle := rgbStyle(35, 35, 35)

	var sb strings.Builder
	for i := 0; i < width; i++ {
		vl := samples[i*2]
		vr := samples[i*2+1]

		ll := normalizeLevel(vl, vmin, vmax)
		rl := normalizeLevel(vr, vmin, vmax)
		ch := brailleRunes[ll*5+rl]

		if ll == 0 && rl == 0 {
			sb.WriteString(emptyStyle.Render("⠀"))
		} else {
			avg := (vl + vr) / 2
			pct := 0.0
			if gradScale > 0 {
				pct = avg / gradScale * 100
			}
			c := grad[clampIdx(pct)]
			sb.WriteString(rgbStyle(c[0], c[1], c[2]).Render(string(ch)))
		}
	}
	return sb.String()
}

// ── Formatting helpers ────────────────────────────────────────────────

func fmtGB(b int64) string {
	return fmt.Sprintf("%.1fGB", float64(b)/float64(1024*1024*1024))
}

func fmtMHz(mhz int) string {
	if mhz >= 1000 {
		return fmt.Sprintf("%.2fGHz", float64(mhz)/1000)
	}
	return fmt.Sprintf("%dMHz", mhz)
}
