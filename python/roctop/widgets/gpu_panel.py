"""Per-GPU panel widget with bars, braille sparklines, and info view."""
from __future__ import annotations

from rich.color import Color
from rich.style import Style
from rich.text import Text
from textual.app import ComposeResult
from textual.widget import Widget
from textual.widgets import Static

from roctop.data import GpuData, GpuHistory
from roctop.render import (
    POWER_GRADIENT,
    TEMP_GRADIENT,
    UTIL_GRADIENT,
    render_bar,
    render_sparkline,
)

_SPARK_INDENT = 5     # leading spaces before sparkline rows
_WARN_COLOR   = Color.from_rgb(255, 80, 0)   # orange for throttle warning


def _fmt_gb(b: int) -> str:
    return f"{b / 1024**3:.1f}GB"


def _fmt_mhz(mhz: int) -> str:
    if mhz >= 1000:
        return f"{mhz / 1000:.2f}GHz"
    return f"{mhz}MHz"


def _kv(t: Text, label: str, value: str, label_style: str = "dim", value_style: str = "") -> None:
    """Append a label: value pair to a Rich Text object."""
    t.append(f"{label}: ", style=label_style)
    t.append(value or "N/A", style=value_style or "bold")


class GpuPanel(Widget):
    """
    A full-width bordered panel for one GPU.

    Metrics mode (default) — 7 content rows + 2 border = height 9:
      GPU N · Name  [⚠ THROTTLED: REASON]
      USE  ████████████████████████████████████████████  100.0%
           ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿  ← usage sparkline
      VRAM █████████████████████████████████████░░░░░░  92.3%  29.4/32.0GB
      PWR  ████████████████████████████████████░░░░░░░  171W/300W
           ⣿⣶⣴⣿⣿⣷⣦⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿  ← power sparkline
      TEMP ███████████████████████░░░░░░░░░░░░░  77°C · FAN 41% 2082rpm · CLK 3.30GHz

    Info mode (press i) — same height:
      GPU N · Name
      Vendor:   AMD                  GFX:     gfx1201
      VBIOS:    113-APM107573-100    PCIe:    0000:03:00.0 x16 16.0GT/s
      Memory:   Samsung  32.0GB      Max Pwr: 300W
      Driver:   6.16.6              Perf:    auto
      Throttle: none                Voltage: 1148mV
      Unique:   0x64ac21a676f77a5b
    """

    DEFAULT_CSS = """
    GpuPanel {
        border: round $primary-darken-2;
        padding: 0 1;
        height: 9;
        width: 100%;
    }
    GpuPanel:focus {
        border: round $accent;
    }
    GpuPanel Static {
        height: 1;
    }
    """

    def __init__(self, card_id: int, history: GpuHistory, **kwargs) -> None:
        super().__init__(**kwargs)
        self.card_id = card_id
        self.history = history
        self._gpu: GpuData | None = None
        self._info_mode = False

    def compose(self) -> ComposeResult:
        n = self.card_id
        yield Static("", id=f"g{n}-title")
        yield Static("", id=f"g{n}-row1")
        yield Static("", id=f"g{n}-row2")
        yield Static("", id=f"g{n}-row3")
        yield Static("", id=f"g{n}-row4")
        yield Static("", id=f"g{n}-row5")
        yield Static("", id=f"g{n}-row6")

    # ── Public API ────────────────────────────────────────────────────────────

    # Static fields that are fetched once and should survive live refreshes
    _STATIC_FIELDS = (
        "vbios", "mem_vendor", "driver_version", "unique_id",
        "vendor", "sku", "gfx_version", "pcie_root_port",
    )

    def update_data(self, gpu: GpuData) -> None:
        # Carry forward static fields from previous data when new data is blank
        if self._gpu is not None:
            for field in self._STATIC_FIELDS:
                if not getattr(gpu, field) and getattr(self._gpu, field):
                    setattr(gpu, field, getattr(self._gpu, field))
        self._gpu = gpu
        if not self._info_mode:
            self._render_metrics()

    def set_info_mode(self, enabled: bool) -> None:
        self._info_mode = enabled
        if self._gpu is None:
            return
        if enabled:
            self._render_info()
        else:
            self._render_metrics()

    # ── Internal ──────────────────────────────────────────────────────────────

    def _s(self, idx: int) -> Static:
        return self.query_one(f"#g{self.card_id}-row{idx}", Static)

    def _render_metrics(self) -> None:
        if self._gpu is None:
            return
        gpu = self._gpu
        n = self.card_id
        cw = max(20, self.content_size.width)
        LABEL = 5
        pct_w = 7

        # Pre-compute variable-length suffixes for exact bar sizing
        gb_info = f" {_fmt_gb(gpu.vram_used)}/{_fmt_gb(gpu.vram_total)}"
        pwr_sfx = f" {gpu.power_avg:.0f}W/{gpu.power_max:.0f}W"
        tmp_sfx = (
            f" {gpu.temp_junction:.0f}°C"
            f" · FAN {gpu.fan_percent:.0f}% {gpu.fan_rpm}rpm"
            f" · CLK {_fmt_mhz(gpu.sclk)}"
            f" · MEM {_fmt_mhz(gpu.mclk)}"
        )
        spark_w = max(8, cw - _SPARK_INDENT)

        def bw(suffix_len: int) -> int:
            return max(8, cw - LABEL - suffix_len)

        # ── Title + optional throttle warning ─────────────────────────────────
        title = self.query_one(f"#g{n}-title", Static)
        t = Text.from_markup(f"[bold cyan]GPU {n}[/] · [bold]{gpu.name}[/]")
        if gpu.throttle_status:
            reasons = ", ".join(gpu.throttle_reasons) if gpu.throttle_reasons else "UNKNOWN"
            t.append(f"  ⚠ THROTTLED: {reasons}", style=Style(color=_WARN_COLOR, bold=True))
        title.update(t)

        # ── GPU Utilization bar ───────────────────────────────────────────────
        t = Text()
        t.append("USE  ", style="bold dim")
        t.append_text(render_bar(gpu.gpu_use, 100, bw(pct_w), UTIL_GRADIENT))
        t.append(f" {gpu.gpu_use:5.1f}%", style="bold")
        self._s(1).update(t)

        # ── Usage sparkline ───────────────────────────────────────────────────
        t = Text()
        t.append(" " * _SPARK_INDENT)
        t.append_text(render_sparkline(
            self.history.gpu_use, spark_w,
            vmin=0, vmax=100, gradient=UTIL_GRADIENT, gradient_scale=100,
        ))
        self._s(2).update(t)

        # ── VRAM bar + used/total ─────────────────────────────────────────────
        t = Text()
        t.append("VRAM ", style="bold dim")
        t.append_text(render_bar(gpu.vram_percent, 100, bw(pct_w + len(gb_info)), UTIL_GRADIENT))
        t.append(f" {gpu.vram_percent:5.1f}%", style="bold")
        t.append(gb_info, style="dim")
        self._s(3).update(t)

        # ── Power bar ─────────────────────────────────────────────────────────
        t = Text()
        t.append("PWR  ", style="bold dim")
        t.append_text(render_bar(gpu.power_avg, gpu.power_max, bw(len(pwr_sfx)), POWER_GRADIENT))
        t.append(pwr_sfx, style="bold")
        self._s(4).update(t)

        # ── Power sparkline ───────────────────────────────────────────────────
        t = Text()
        t.append(" " * _SPARK_INDENT)
        t.append_text(render_sparkline(
            self.history.power, spark_w,
            vmin=0, vmax=gpu.power_max, gradient=POWER_GRADIENT,
            gradient_scale=gpu.power_max,
        ))
        self._s(5).update(t)

        # ── Temperature bar + fan + clocks ────────────────────────────────────
        t = Text()
        t.append("TEMP ", style="bold dim")
        t.append_text(render_bar(gpu.temp_junction, 110, bw(len(tmp_sfx)), TEMP_GRADIENT))
        t.append(f" {gpu.temp_junction:.0f}°C", style="bold")
        t.append(" · FAN ", style="dim")
        t.append(f"{gpu.fan_percent:.0f}%", style="bold")
        t.append(f" {gpu.fan_rpm}rpm", style="dim")
        t.append(" · CLK ", style="dim")
        t.append(f"{_fmt_mhz(gpu.sclk)}", style="bold")
        t.append(" · MEM ", style="dim")
        t.append(f"{_fmt_mhz(gpu.mclk)}", style="bold")
        self._s(6).update(t)

    def _render_info(self) -> None:
        if self._gpu is None:
            return
        gpu = self._gpu
        n = self.card_id
        cw = max(20, self.content_size.width)
        # Each info row shows two columns; col_w is value column width
        col_w = max(8, cw // 2 - 14)

        # ── Title ─────────────────────────────────────────────────────────────
        title = self.query_one(f"#g{n}-title", Static)
        title.update(Text.from_markup(
            f"[bold cyan]GPU {n}[/] · [bold]{gpu.name}[/]"
            f"  [dim]press [bold yellow]i[/] to return to metrics[/]"
        ))

        def kv_row(pairs: list[tuple[str, str]]) -> Text:
            """Two label: value pairs side by side."""
            t = Text(no_wrap=True, overflow="ellipsis")
            for i, (label, value) in enumerate(pairs):
                if i > 0:
                    t.append("  ")
                t.append(f"{label + ':': <10}", style="dim")
                t.append(f" {(value or 'N/A'): <{col_w}}", style="bold")
            return t

        # ── Row 1: Vendor + GFX version ───────────────────────────────────────
        self._s(1).update(kv_row([
            ("Vendor", gpu.vendor or "AMD"),
            ("GFX",    gpu.gfx_version),
        ]))

        # ── Row 2: VBIOS + PCIe bus (with root port, width, speed) ──────────
        pcie_val = gpu.pcie_bus
        if gpu.pcie_width:
            pcie_val += f" x{gpu.pcie_width}"
        if gpu.pcie_speed:
            pcie_val += f" {gpu.pcie_speed}"
        if gpu.pcie_root_port:
            pcie_val += f"  root {gpu.pcie_root_port}"
        self._s(2).update(kv_row([
            ("VBIOS", gpu.vbios),
            ("PCIe",  pcie_val.strip()),
        ]))

        # ── Row 3: Memory vendor + total, max power ───────────────────────────
        mem_val = f"{gpu.mem_vendor} {_fmt_gb(gpu.vram_total)}" if gpu.mem_vendor else _fmt_gb(gpu.vram_total)
        self._s(3).update(kv_row([
            ("Memory",   mem_val.strip()),
            ("Max Power", f"{gpu.power_max:.0f}W"),
        ]))

        # ── Row 4: Driver version + performance level ─────────────────────────
        self._s(4).update(kv_row([
            ("Driver", gpu.driver_version),
            ("Perf",   gpu.perf_level or "auto"),
        ]))

        # ── Row 5: Throttle status + voltage ─────────────────────────────────
        if gpu.throttle_status:
            reasons = ", ".join(gpu.throttle_reasons) if gpu.throttle_reasons else "ACTIVE"
            t = Text(no_wrap=True, overflow="ellipsis")
            t.append(f"{'Throttle:': <10}", style="dim")
            t.append(f" ⚠ {reasons}", style=Style(color=_WARN_COLOR, bold=True))
            t.append(f"  {'Voltage:': <10}", style="dim")
            t.append(f" {gpu.voltage:.0f}mV", style="bold")
            self._s(5).update(t)
        else:
            self._s(5).update(kv_row([
                ("Throttle", "none"),
                ("Voltage",  f"{gpu.voltage:.0f}mV"),
            ]))

        # ── Row 6: Unique ID + SKU ────────────────────────────────────────────
        self._s(6).update(kv_row([
            ("Unique ID", gpu.unique_id),
            ("SKU",       gpu.sku),
        ]))

    def on_resize(self) -> None:
        if self._info_mode:
            self._render_info()
        else:
            self._render_metrics()


