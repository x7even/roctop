# roctop

A terminal UI for real-time AMD GPU monitoring via ROCm/rocm-smi, inspired by the fantastic work of [btop](https://github.com/aristocratos/btop).

![roctop screenshot](https://raw.githubusercontent.com/placeholder/roctop/main/screenshot.png)

## Features

- Real-time per-GPU panels: utilization, VRAM, power, temperature, clocks, fan
- Braille sparklines with gradient coloring (dual-value encoding per character cell)
- Throttle detection with reason decoding (THERMAL, POWER_LIMIT, etc.)
- Static info view (press `i`): VBIOS, PCIe topology, memory vendor, driver, unique ID
- Process table showing which processes are using VRAM and on which GPUs
- Adjustable refresh rate, pause, and force-refresh keybindings

## Requirements

- Linux with AMD GPU(s)
- [ROCm](https://rocm.docs.amd.com/) installed — specifically `rocm-smi` must be on `$PATH`
- Python 3.12+

## Installation

### From source (recommended for development)

```bash
git clone https://github.com/your-username/roctop.git
cd roctop
pip install -e .
```

### Direct pip install

```bash
pip install .
```

A virtual environment is recommended:

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e .
```

## Running

```bash
roctop
```

Or without installing:

```bash
python -m roctop
```

### Options

```
roctop [--refresh N]
```

| Option | Default | Description |
|--------|---------|-------------|
| `--refresh N` | `2.0` | Refresh interval in seconds |

## Keybindings

| Key | Action |
|-----|--------|
| `q` | Quit |
| `i` | Toggle info view (static GPU details) |
| `r` | Force refresh |
| `+` / `=` | Increase refresh rate |
| `-` | Decrease refresh rate |
| `p` | Pause / resume |

## Panel layout

**Metrics view** (default):
```
┌ GPU 0 · AMD Radeon RX 7900 XTX ──────────────────────────────────────┐
│ USE  ████████████████████████████████████████  100.0%                 │
│      ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿  ← utilisation sparkline  │
│ VRAM ████████████████████████████████░░░░░░░   92.3%  29.4/32.0GB    │
│ PWR  ████████████████████████████░░░░░░░░░░░   171W/300W             │
│      ⣿⣶⣴⣿⣿⣷⣦⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿  ← power sparkline        │
│ TEMP ███████████████████░░░░░░░░░  77°C · FAN 41% 2082rpm · CLK 3.30GHz │
└───────────────────────────────────────────────────────────────────────┘
```

**Info view** (press `i`):
```
┌ GPU 0 · AMD Radeon RX 7900 XTX  press i to return to metrics ────────┐
│ Vendor:    AMD                   GFX:       gfx1201                   │
│ VBIOS:     113-APM107573-100     PCIe:      0000:03:00.0 x16 16.0GT/s │
│ Memory:    Samsung  32.0GB       Max Power: 300W                      │
│ Driver:    6.16.6                Perf:      auto                      │
│ Throttle:  none                  Voltage:   1148mV                    │
│ Unique ID: 0x64ac21a676f77a5b    SKU:       D7170100                  │
└───────────────────────────────────────────────────────────────────────┘
```

## Development

The project uses [Textual](https://github.com/Textualize/textual) for the TUI framework.

### Project structure

```
roctop/
├── pyproject.toml          # Project metadata and dependencies
└── roctop/
    ├── __main__.py         # Entry point (python -m roctop)
    ├── app.py              # Textual App — layout, keybindings, timer
    ├── data.py             # rocm-smi data collection and parsing
    ├── render.py           # Bar charts and braille sparkline renderer
    ├── roctop.tcss         # Textual CSS stylesheet
    └── widgets/
        ├── gpu_panel.py    # Per-GPU panel (metrics + info views)
        ├── header_bar.py   # Top header bar
        └── process_table.py # GPU process list
```

### Dependencies

| Package | Purpose |
|---------|---------|
| `textual>=0.50.0` | TUI framework (widgets, layout, input handling) |
| `rich` | Rich text rendering (pulled in by Textual) |

### Data collection

All GPU data is collected by shelling out to `rocm-smi --json` with appropriate flags. No ROCm Python bindings are used — this keeps the dependency surface small and avoids version compatibility issues.

Key collection calls:
- `rocm-smi --json --showuse --showmeminfo vram --showmemuse -t --showpower ...` — main metrics (every refresh)
- `rocm-smi --json --showmetrics` — throttle status and PCIe link info (every refresh)
- `rocm-smi --json --showvbios --showmemvendor --showuniqueid --showdriverversion` — static info (once at startup)

### Running without installing

```bash
cd /path/to/roctop
python -m roctop
python -m roctop --refresh 1
```

## License

MIT
