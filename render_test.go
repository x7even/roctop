package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ANSI truecolor foreground fragments for the status palette.
const (
	ansiSteel = "38;2;95;135;175"
	ansiAmber = "38;2;215;166;95"
	ansiRed   = "38;2;215;95;95"
	ansiTrack = "38;2;50;50;60"
)

// forceTrueColor makes lipgloss emit truecolor escape codes so tests can
// assert on colors; restored after the test.
func forceTrueColor(t *testing.T) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

func TestRenderBarFillAndTrackGlyphs(t *testing.T) {
	out := renderBar(50, 100, 10)
	if got := strings.Count(out, barFill); got != 5 {
		t.Errorf("want 5 %q fill cells, got %d in %q", barFill, got, out)
	}
	if got := strings.Count(out, trackRune); got != 5 {
		t.Errorf("want 5 %q track cells, got %d in %q", trackRune, got, out)
	}
	for _, old := range []string{"█", "░"} {
		if strings.Contains(out, old) {
			t.Errorf("output must not contain old glyph %q: %q", old, out)
		}
	}
}

func TestRenderBarEmptyIsAllTrack(t *testing.T) {
	forceTrueColor(t)
	out := renderBar(0, 100, 8)
	if got := strings.Count(out, trackRune); got != 8 {
		t.Errorf("want 8 track cells, got %d in %q", got, out)
	}
	if strings.Contains(out, barFill) {
		t.Errorf("empty bar must not contain fill cells: %q", out)
	}
	if !strings.Contains(out, ansiTrack) {
		t.Errorf("track must use rgb(50,50,60), got %q", out)
	}
}

func TestRenderBarThresholdCellColors(t *testing.T) {
	forceTrueColor(t)

	// A full 20-cell bar: cell i covers i*5%, so amber starts at cell 15
	// (75%) and red at cell 18 (90%).
	out := renderBar(100, 100, 20)
	iSteel := strings.Index(out, ansiSteel)
	iAmber := strings.Index(out, ansiAmber)
	iRed := strings.Index(out, ansiRed)
	if iSteel < 0 || iAmber < 0 || iRed < 0 {
		t.Fatalf("full bar must contain steel, amber and red segments: %q", out)
	}
	if iSteel >= iAmber || iAmber >= iRed {
		t.Errorf("segments must appear in order steel < amber < red, got %d/%d/%d in %q",
			iSteel, iAmber, iRed, out)
	}
	if got := strings.Count(out, barFill); got != 20 {
		t.Errorf("want 20 fill cells, got %d in %q", got, out)
	}

	// A 70% bar never reaches the hot zone: steel only, no amber/red.
	calm := renderBar(70, 100, 20)
	if !strings.Contains(calm, ansiSteel) {
		t.Errorf("calm bar must use steel base: %q", calm)
	}
	if strings.Contains(calm, ansiAmber) || strings.Contains(calm, ansiRed) {
		t.Errorf("calm bar must not contain status accents: %q", calm)
	}

	// An 80% bar reaches amber but not red.
	warm := renderBar(80, 100, 20)
	if !strings.Contains(warm, ansiAmber) {
		t.Errorf("80%% bar must contain amber cells: %q", warm)
	}
	if strings.Contains(warm, ansiRed) {
		t.Errorf("80%% bar must not contain red cells: %q", warm)
	}
}

func TestRenderBarWholeCellRounding(t *testing.T) {
	// The slim fill has no fractional glyphs: 55% of 10 cells = 5.5 rounds
	// up to 6 fills; 54% = 5.4 rounds down to 5.
	out := renderBar(55, 100, 10)
	if got := strings.Count(out, barFill); got != 6 {
		t.Errorf("55%%: want 6 fill cells, got %d in %q", got, out)
	}
	if got := strings.Count(out, trackRune); got != 4 {
		t.Errorf("55%%: want 4 track cells, got %d in %q", got, out)
	}
	out = renderBar(54, 100, 10)
	if got := strings.Count(out, barFill); got != 5 {
		t.Errorf("54%%: want 5 fill cells, got %d in %q", got, out)
	}
	// No leftover braille tip glyphs from the textured variant.
	for _, tip := range []string{"⡀", "⡄", "⡆", "⡇"} {
		if strings.Contains(out, tip) {
			t.Errorf("bar must not contain braille tip %q: %q", tip, out)
		}
	}
}

func TestStatusTierThresholds(t *testing.T) {
	cases := []struct {
		pct  float64
		want int
	}{
		{0, 0}, {74.9, 0}, {75, 1}, {89.9, 1}, {90, 2}, {100, 2},
	}
	for _, c := range cases {
		if got := statusTier(c.pct); got != c.want {
			t.Errorf("statusTier(%v) = %d, want %d", c.pct, got, c.want)
		}
	}
}

func TestSparklineThresholdColors(t *testing.T) {
	forceTrueColor(t)

	render := func(v float64) string {
		hist := make([]float64, 40)
		for i := range hist {
			hist[i] = v
		}
		rows := renderMultilineSparkline(hist, 20, 3, 0, 100, 100)
		return strings.Join(rows, "\n")
	}

	low := render(50)
	if !strings.Contains(low, ansiSteel) {
		t.Errorf("50%% sparkline must use steel base: %q", low)
	}
	if strings.Contains(low, ansiAmber) || strings.Contains(low, ansiRed) {
		t.Errorf("50%% sparkline must not contain status accents: %q", low)
	}

	warm := render(80)
	if !strings.Contains(warm, ansiAmber) {
		t.Errorf("80%% sparkline must be amber: %q", warm)
	}
	if strings.Contains(warm, ansiRed) {
		t.Errorf("80%% sparkline must not be red: %q", warm)
	}

	hot := render(95)
	if !strings.Contains(hot, ansiRed) {
		t.Errorf("95%% sparkline must be red: %q", hot)
	}
}
