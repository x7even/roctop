package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Unicode character sets ────────────────────────────────────────────

// braille encodes two vertical bar levels (0-4) per cell: braille[left*5+right]
const braille = " ⢀⢠⢰⢸⡀⣀⣠⣰⣸⡄⣄⣤⣴⣼⡆⣆⣦⣶⣾⡇⣇⣧⣷⣿"

// barFill is a full braille cell: it gives the bar fill a woven texture
// that matches the braille sparklines.
const barFill = "⣿"

// barTipChars maps the fractional tail of a bar to a rising left-column
// braille glyph. Index 0 is intentionally blank: a fraction too small to
// earn the first partial glyph falls through to the dotted track, matching
// the previous partial-cell behavior.
const barTipChars = " ⡀⡄⡆⡇"

// trackRune is the empty portion of a bar: a quiet dotted track.
const trackRune = "·"

// ── Status palette ────────────────────────────────────────────────────
//
// Muted steel fill with htop-like positional status accents. A filled bar
// cell is colored by its POSITION in the bar, not by the metric value: the
// bar stays calm until the fill actually reaches the hot zone, and then
// only the hot cells change color.

var (
	steelStyle = rgbStyle(95, 135, 175) // #5f87af base fill
	amberStyle = rgbStyle(215, 166, 95) // #d7a65f warning accent
	redStyle   = rgbStyle(215, 95, 95)  // #d75f5f critical accent
	trackStyle = rgbStyle(50, 50, 60)   // dotted empty track
)

// tierStyles is indexed by statusTier.
var tierStyles = [3]lipgloss.Style{steelStyle, amberStyle, redStyle}

// statusTier maps a percentage to a color tier: 0 steel, 1 amber (>= 75),
// 2 red (>= 90).
func statusTier(pct float64) int {
	switch {
	case pct >= 90:
		return 2
	case pct >= 75:
		return 1
	default:
		return 0
	}
}

// barCellTier returns the color tier for the filled cell at position i of a
// bar that is width cells wide.
func barCellTier(i, width int) int {
	return statusTier(float64(i) * 100 / float64(width))
}

func rgbStyle(r, g, b uint8) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b)))
}

// ── Bar chart renderer ────────────────────────────────────────────────

func renderBar(value, maximum float64, width int) string {
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

	tipRunes := []rune(barTipChars)
	cells := pct / 100 * float64(width)
	filled := int(cells)
	remainder := cells - float64(filled)
	fracIdx := int(remainder * float64(len(tipRunes)-1))

	var sb strings.Builder
	// Paint filled cells in runs of a single tier to keep escape codes down.
	for start := 0; start < filled; {
		tier := barCellTier(start, width)
		end := start + 1
		for end < filled && barCellTier(end, width) == tier {
			end++
		}
		sb.WriteString(tierStyles[tier].Render(strings.Repeat(barFill, end-start)))
		start = end
	}
	if fracIdx > 0 && filled < width {
		sb.WriteString(tierStyles[barCellTier(filled, width)].Render(string(tipRunes[fracIdx])))
		if empty := width - filled - 1; empty > 0 {
			sb.WriteString(trackStyle.Render(strings.Repeat(trackRune, empty)))
		}
	} else if empty := width - filled; empty > 0 {
		sb.WriteString(trackStyle.Render(strings.Repeat(trackRune, empty)))
	}
	return sb.String()
}

// ── Braille sparkline renderer ────────────────────────────────────────

func normalizeToLevels(value, vmin, vmax float64, total int) int {
	if vmax <= vmin || value <= vmin {
		return 0
	}
	if value >= vmax {
		return total
	}
	t := (value - vmin) / (vmax - vmin)
	level := int(t * float64(total))
	if level < 1 {
		level = 1
	}
	if level > total {
		level = total
	}
	return level
}

// renderMultilineSparkline draws history as braille columns. Each column is
// colored by its value relative to colorScale: >= 90% red, >= 75% amber,
// otherwise the steel base — the same status thresholds as the bars.
func renderMultilineSparkline(history []float64, width, rows int, vmin, vmax float64, colorScale float64) []string {
	result := make([]string, rows)
	if width <= 0 {
		return result
	}

	needed := width * 2
	samples := make([]float64, needed)
	if len(history) >= needed {
		copy(samples, history[len(history)-needed:])
	} else {
		copy(samples[needed-len(history):], history)
	}

	totalLevels := rows * 4
	brailleRunes := []rune(braille)
	emptyStyle := rgbStyle(35, 35, 35)

	rowBuilders := make([]strings.Builder, rows)

	for i := 0; i < width; i++ {
		vl := samples[i*2]
		vr := samples[i*2+1]

		fillL := normalizeToLevels(vl, vmin, vmax, totalLevels)
		fillR := normalizeToLevels(vr, vmin, vmax, totalLevels)
		isEmpty := fillL == 0 && fillR == 0

		avg := (vl + vr) / 2
		pct := 0.0
		if colorScale > 0 {
			pct = avg / colorScale * 100
		}
		colStyle := tierStyles[statusTier(pct)]

		for r := 0; r < rows; r++ {
			levelsBelow := (rows - 1 - r) * 4
			ll := fillL - levelsBelow
			rl := fillR - levelsBelow
			if ll < 0 {
				ll = 0
			}
			if ll > 4 {
				ll = 4
			}
			if rl < 0 {
				rl = 0
			}
			if rl > 4 {
				rl = 4
			}

			if isEmpty || (ll == 0 && rl == 0) {
				rowBuilders[r].WriteString(emptyStyle.Render(string(brailleRunes[0])))
			} else {
				rowBuilders[r].WriteString(colStyle.Render(string(brailleRunes[ll*5+rl])))
			}
		}
	}

	for i := range result {
		result[i] = rowBuilders[i].String()
	}
	return result
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
