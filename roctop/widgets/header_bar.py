"""Top header widget."""
from __future__ import annotations

from rich.text import Text
from textual.widget import Widget


class HeaderBar(Widget):
    """Top bar showing app name, GPU count, refresh rate and key hints."""

    DEFAULT_CSS = """
    HeaderBar {
        background: $primary-darken-3;
        color: $text;
        height: 1;
        padding: 0 1;
        text-style: bold;
    }
    """

    def __init__(self, gpu_count: int = 0, refresh_interval: float = 2.0, **kwargs) -> None:
        super().__init__(**kwargs)
        self._gpu_count = gpu_count
        self._refresh_interval = refresh_interval
        self._paused = False
        self._info_mode = False

    def render_header(
        self,
        gpu_count: int,
        refresh_interval: float,
        info_mode: bool = False,
    ) -> None:
        self._gpu_count = gpu_count
        self._refresh_interval = refresh_interval
        self._paused = False
        self._info_mode = info_mode
        self.refresh()

    def show_paused(self) -> None:
        self._paused = True
        self.refresh()

    def render(self) -> Text:
        if self._paused:
            t = Text(no_wrap=True, overflow="ellipsis")
            t.append(" ⏸  PAUSED", style="bold yellow")
            t.append(" — press ", style="dim")
            t.append("p", style="bold yellow")
            t.append(" to resume", style="dim")
            return t

        t = Text(no_wrap=True, overflow="ellipsis")
        t.append("roctop", style="bold cyan")
        t.append(f"  {self._gpu_count} GPU{'s' if self._gpu_count != 1 else ''}", style="green")
        if not self._info_mode:
            t.append(f"  refresh {self._refresh_interval:.1f}s", style="dim")
        else:
            t.append("  INFO MODE", style="bold magenta")
        t.append("  q", style="bold yellow")
        t.append(":quit  ", style="dim")
        t.append("+", style="bold yellow")
        t.append("/", style="bold yellow")
        t.append("-", style="bold yellow")
        t.append(":speed  ", style="dim")
        t.append("r", style="bold yellow")
        t.append(":refresh  ", style="dim")
        t.append("p", style="bold yellow")
        t.append(":pause  ", style="dim")
        t.append("i", style="bold yellow")
        t.append(":info", style="dim")
        return t
