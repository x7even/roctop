"""Main Textual application."""
from __future__ import annotations

from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import ScrollableContainer, Vertical

from roctop.data import GpuHistory, collect_gpu_data, collect_static_info
from roctop.widgets.gpu_panel import GpuPanel
from roctop.widgets.header_bar import HeaderBar
from roctop.widgets.process_table import ProcessTable

MIN_REFRESH = 0.5
MAX_REFRESH = 30.0
REFRESH_STEP = 0.5


class RoctopApp(App):
    CSS_PATH = "roctop.tcss"

    BINDINGS = [
        Binding("q", "quit", "Quit", priority=True),
        Binding("r", "force_refresh", "Refresh", priority=True),
        Binding("+", "faster", "Faster", priority=True),
        Binding("=", "faster", "Faster", show=False, priority=True),
        Binding("-", "slower", "Slower", priority=True),
        Binding("p", "pause", "Pause", priority=True),
        Binding("i", "toggle_info", "Info", priority=True),
    ]

    def __init__(self, refresh_interval: float = 2.0, **kwargs) -> None:
        super().__init__(**kwargs)
        self._refresh_interval = refresh_interval
        self._paused = False
        self._info_mode = False
        self._histories: dict[int, GpuHistory] = {}
        self._timer = None
        self._initialized = False

    def compose(self) -> ComposeResult:
        with Vertical(id="main-layout"):
            yield HeaderBar(id="header")
            with ScrollableContainer(id="gpu-container"):
                with Vertical(id="gpu-row"):
                    pass  # GPU panels mounted dynamically in on_mount
            yield ProcessTable(id="process-table")

    async def on_mount(self) -> None:
        # Phase 1: fetch initial data and mount GPU panels
        gpus, procs = collect_gpu_data()
        gpu_row = self.query_one("#gpu-row", Vertical)

        for gpu in gpus:
            self._histories[gpu.card_id] = GpuHistory(card_id=gpu.card_id)
            panel = GpuPanel(
                card_id=gpu.card_id,
                history=self._histories[gpu.card_id],
                id=f"gpu-panel-{gpu.card_id}",
            )
            await gpu_row.mount(panel)

        self.query_one("#header", HeaderBar).render_header(len(gpus), self._refresh_interval)

        # Phase 2: fetch one-time static info (VBIOS, mem vendor, driver, unique ID)
        collect_static_info(gpus)

        # Phase 3: push initial data into panels now that they're composed
        self._push_update(gpus, procs)
        self._initialized = True

        # Start the periodic refresh timer
        self._timer = self.set_interval(self._refresh_interval, self._do_refresh)

    def _push_update(self, gpus, procs) -> None:
        """Push data into history buffers and update all panels."""
        for gpu in gpus:
            if gpu.card_id not in self._histories:
                continue
            h = self._histories[gpu.card_id]
            h.gpu_use.append(gpu.gpu_use)
            h.power.append(gpu.power_avg)
            h.temp_junction.append(gpu.temp_junction)
            try:
                panel = self.query_one(f"#gpu-panel-{gpu.card_id}", GpuPanel)
                panel.update_data(gpu)
            except Exception:
                pass

        try:
            self.query_one("#process-table", ProcessTable).update_data(procs)
        except Exception:
            pass

    def _do_refresh(self) -> None:
        if self._paused or not self._initialized:
            return
        gpus, procs = collect_gpu_data()
        self._push_update(gpus, procs)

    def _restart_timer(self) -> None:
        if self._timer is not None:
            self._timer.stop()
        self._timer = self.set_interval(self._refresh_interval, self._do_refresh)
        self.query_one("#header", HeaderBar).render_header(
            len(self._histories), self._refresh_interval
        )

    def action_force_refresh(self) -> None:
        self._do_refresh()

    def action_faster(self) -> None:
        self._refresh_interval = max(MIN_REFRESH, self._refresh_interval - REFRESH_STEP)
        self._restart_timer()

    def action_slower(self) -> None:
        self._refresh_interval = min(MAX_REFRESH, self._refresh_interval + REFRESH_STEP)
        self._restart_timer()

    def action_pause(self) -> None:
        self._paused = not self._paused
        header = self.query_one("#header", HeaderBar)
        if self._paused:
            header.show_paused()
        else:
            header.render_header(len(self._histories), self._refresh_interval)
            self._do_refresh()

    def action_toggle_info(self) -> None:
        """Toggle all GPU panels between metrics view and static info view."""
        self._info_mode = not self._info_mode
        for card_id in self._histories:
            try:
                panel = self.query_one(f"#gpu-panel-{card_id}", GpuPanel)
                panel.set_info_mode(self._info_mode)
            except Exception:
                pass
        self.query_one("#header", HeaderBar).render_header(
            len(self._histories), self._refresh_interval, info_mode=self._info_mode
        )
