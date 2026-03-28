"""Process table widget showing GPU processes."""
from __future__ import annotations

from rich.text import Text
from textual.widget import Widget

from roctop.data import ProcessData


def _fmt_bytes(b: int) -> str:
    if b >= 1024**3:
        return f"{b / 1024**3:.1f} GB"
    if b >= 1024**2:
        return f"{b / 1024**2:.0f} MB"
    return f"{b // 1024} KB"


class ProcessTable(Widget):
    """Bottom panel listing active GPU processes."""

    DEFAULT_CSS = """
    ProcessTable {
        border: round $primary-darken-2;
        padding: 0 1;
        height: 12;
    }
    """

    def __init__(self, **kwargs) -> None:
        super().__init__(**kwargs)
        self._procs: list[ProcessData] = []

    def update_data(self, procs: list[ProcessData]) -> None:
        self._procs = procs
        self.refresh()

    def render(self) -> Text:
        t = Text(no_wrap=True, overflow="ellipsis")

        t.append("Processes", style="bold cyan")
        t.append("\n")
        t.append(f"{'PID':<8}", style="bold dim")
        t.append(f"{'Name':<20}", style="bold dim")
        t.append(f"{'GPUs':<12}", style="bold dim")
        t.append(f"{'VRAM':>10}", style="bold dim")
        t.append("\n")

        if not self._procs:
            t.append("  no GPU processes", style="dim italic")
        else:
            for proc in self._procs[:6]:
                gpus_str = ",".join(str(g) for g in sorted(proc.gpu_ids)) or "?"
                t.append(f"{proc.pid:<8}", style="green")
                t.append(f"{proc.name[:19]:<20}", style="")
                t.append(f"{gpus_str:<12}", style="cyan")
                t.append(f"{_fmt_bytes(proc.vram_used):>10}", style="yellow")
                t.append("\n")

        return t
