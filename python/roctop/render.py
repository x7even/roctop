"""
Rendering helpers: colored bar charts and braille sparklines.

Bar charts use Unicode block fill chars with per-value gradient colors.
Sparklines use braille patterns (U+2800-U+28FF) to encode two data points
per character cell, each character independently colored by its value.
"""
from __future__ import annotations

from collections.abc import Sequence

from rich.color import Color
from rich.style import Style
from rich.text import Text

# ---------------------------------------------------------------------------
# Unicode character sets
# ---------------------------------------------------------------------------

# 8 block fill levels for bars (left to right fill)
BAR_CHARS = " ▏▎▍▌▋▊▉█"

# Braille vertical bar chars for sparklines — 5 levels (0-4) per "column"
# Each braille char encodes two sequential values as symbol[v_left * 5 + v_right]
# Braille symbol set encoding two vertical bar levels per character cell
BRAILLE = (
    " ⢀⢠⢰⢸"
    "⡀⣀⣠⣰⣸"
    "⡄⣄⣤⣴⣼"
    "⡆⣆⣦⣶⣾"
    "⡇⣇⣧⣷⣿"
)

# Fallback single-column block sparkline chars (when braille unavailable)
SPARK_BLOCKS = "▁▂▃▄▅▆▇█"

# ---------------------------------------------------------------------------
# Color gradients  (list of (r, g, b) tuples, index by 0-100)
# ---------------------------------------------------------------------------

def _lerp_color(
    stops: list[tuple[int, float, tuple[int, int, int]]],
    value: float,
) -> tuple[int, int, int]:
    """Interpolate between color stops. stops = [(pos, (r,g,b)), ...]"""
    value = max(0.0, min(100.0, value))
    for i in range(len(stops) - 1):
        p0, c0 = stops[i]
        p1, c1 = stops[i + 1]
        if value <= p1:
            t = (value - p0) / (p1 - p0) if p1 != p0 else 0.0
            r = int(c0[0] + (c1[0] - c0[0]) * t)
            g = int(c0[1] + (c1[1] - c0[1]) * t)
            b = int(c0[2] + (c1[2] - c0[2]) * t)
            return r, g, b
    return stops[-1][1]


# Utilization gradient: deep blue → teal → green → amber → orange → red
_UTIL_STOPS: list[tuple[float, tuple[int, int, int]]] = [
    (0,   (30,  100, 200)),
    (30,  (0,   175, 135)),
    (60,  (95,  215,   0)),
    (75,  (255, 175,   0)),
    (88,  (255,  95,   0)),
    (100, (255,   0,   0)),
]

# Power gradient: dark green → green → yellow → orange → red (0-100% of max)
_POWER_STOPS: list[tuple[float, tuple[int, int, int]]] = [
    (0,   (0,  135,  95)),
    (40,  (0,  215,   0)),
    (65,  (215, 215,  0)),
    (85,  (255, 95,   0)),
    (100, (255,  0,   0)),
]

# Temperature gradient: cyan → green → amber → red (0-100°C mapped to 0-100)
_TEMP_STOPS: list[tuple[float, tuple[int, int, int]]] = [
    (0,   (0,   215, 255)),
    (40,  (0,   215, 135)),
    (60,  (95,  215,   0)),
    (75,  (255, 175,   0)),
    (88,  (255,  95,   0)),
    (100, (255,   0,   0)),
]

# Pre-build gradient lookup tables (101 entries, index = 0..100)
def _build_gradient(stops: list[tuple[float, tuple[int, int, int]]]) -> list[tuple[int, int, int]]:
    return [_lerp_color(stops, i) for i in range(101)]

UTIL_GRADIENT = _build_gradient(_UTIL_STOPS)
POWER_GRADIENT = _build_gradient(_POWER_STOPS)
TEMP_GRADIENT = _build_gradient(_TEMP_STOPS)


def _clamp_idx(v: float) -> int:
    return max(0, min(100, int(v)))


# ---------------------------------------------------------------------------
# Bar chart renderer
# ---------------------------------------------------------------------------

def render_bar(
    value: float,
    maximum: float,
    width: int,
    gradient: list[tuple[int, int, int]] = UTIL_GRADIENT,
    show_value: bool = False,
    unit: str = "%",
) -> Text:
    """
    Render a colored horizontal bar.

    Returns a Rich Text object ready to embed in a widget.
    """
    pct = max(0.0, min(100.0, value / maximum * 100)) if maximum > 0 else 0.0
    r, g, b = gradient[_clamp_idx(pct)]
    fill_color = Color.from_rgb(r, g, b)
    empty_color = Color.from_rgb(40, 40, 40)

    filled_cells = int(pct / 100 * width)
    remainder = (pct / 100 * width) - filled_cells
    # sub-character precision: pick the fractional block char
    frac_idx = int(remainder * (len(BAR_CHARS) - 1))
    frac_char = BAR_CHARS[frac_idx] if frac_idx > 0 else ""

    text = Text(no_wrap=True, overflow="crop")
    if filled_cells > 0:
        text.append("█" * filled_cells, style=Style(color=fill_color))
    if frac_char and filled_cells < width:
        text.append(frac_char, style=Style(color=fill_color))
        empty_cells = width - filled_cells - 1
    else:
        empty_cells = width - filled_cells
    if empty_cells > 0:
        text.append("░" * empty_cells, style=Style(color=empty_color))
    return text


# ---------------------------------------------------------------------------
# Braille sparkline renderer
# ---------------------------------------------------------------------------

def _normalize_to_level(value: float, vmin: float, vmax: float) -> int:
    """Map value to 0-4 braille fill level. Any non-zero value shows at least 1."""
    if vmax <= vmin:
        return 0
    t = (value - vmin) / (vmax - vmin)
    if t <= 0:
        return 0
    return max(1, min(4, int(t * 4.9)))


def render_sparkline(
    history: Sequence[float],
    width: int,
    vmin: float = 0.0,
    vmax: float = 100.0,
    gradient: list[tuple[int, int, int]] = UTIL_GRADIENT,
    gradient_scale: float = 100.0,
) -> Text:
    """
    Render a braille sparkline using dual-value encoding.

    Each character cell encodes two consecutive samples. Characters are
    individually colored by the average value of their two samples using
    the supplied gradient.

    Args:
        history:        sequence of values (oldest first)
        width:          character width to render
        vmin/vmax:      value range for vertical scaling
        gradient:       101-entry gradient for coloring
        gradient_scale: the "100%" value for gradient indexing
                        (e.g. 300.0 for power in watts → maps 0-300W to 0-100%)
    """
    # We need width*2 data points; pad left with zeros if not enough history
    needed = width * 2
    samples = list(history)
    if len(samples) < needed:
        samples = [vmin] * (needed - len(samples)) + samples
    else:
        samples = samples[-needed:]

    text = Text(no_wrap=True, overflow="crop")
    empty_color = Color.from_rgb(35, 35, 35)

    for i in range(width):
        v_left = samples[i * 2]
        v_right = samples[i * 2 + 1]

        lv = _normalize_to_level(v_left, vmin, vmax)
        rv = _normalize_to_level(v_right, vmin, vmax)
        char = BRAILLE[lv * 5 + rv]

        # Color by average of the two values
        avg = (v_left + v_right) / 2.0
        pct = max(0.0, min(100.0, avg / gradient_scale * 100)) if gradient_scale > 0 else 0.0
        if char == " " or (lv == 0 and rv == 0):
            text.append(char if char != " " else "⠀", style=Style(color=empty_color))
        else:
            r, g, b = gradient[_clamp_idx(pct)]
            text.append(char, style=Style(color=Color.from_rgb(r, g, b)))

    return text
