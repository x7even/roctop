# roctop

A terminal UI for real-time AMD GPU monitoring via ROCm/rocm-smi, inspired by the fantastic work of [btop](https://github.com/aristocratos/btop).

![roctop metrics view](https://raw.githubusercontent.com/x7even/roctop/main/roctop-metrics.png)

![roctop info view](https://raw.githubusercontent.com/x7even/roctop/main/roctop-info.png)

## Features

- Real-time 2-column GPU grid: utilisation, VRAM, power, temperature, clocks, fan
- Multi-row braille sparklines with gradient colouring — variation within narrow ranges stays visible
- Throttle detection with reason decoding (THERMAL, POWER_LIMIT, etc.)
- Static info view (press `i`): VBIOS, PCIe topology, memory vendor, driver, unique ID
- Process table showing which processes are using VRAM and on which GPUs
- Scrollable GPU panel region — header and process table stay anchored when the terminal is small
- Adjustable refresh rate, pause, and force-refresh keybindings
- Single static binary — no runtime dependencies beyond `rocm-smi`

## Requirements

- Linux with AMD GPU(s)
- [ROCm](https://rocm.docs.amd.com/) installed — `rocm-smi` must be on `$PATH`

## Installation

### One-line install (recommended)

Auto-detects architecture (amd64 / arm64) and installs to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/x7even/roctop/main/install.sh | bash
```

To install to a different directory:

```bash
INSTALL_DIR=~/.local/bin curl -fsSL https://raw.githubusercontent.com/x7even/roctop/main/install.sh | bash
```

### Manual binary download

Grab the tarball for your architecture from the [releases page](https://github.com/x7even/roctop/releases) and extract it:

```bash
# amd64
curl -fsSL https://github.com/x7even/roctop/releases/latest/download/roctop_<version>_linux_amd64.tar.gz | tar xz
sudo mv roctop /usr/local/bin/

# arm64
curl -fsSL https://github.com/x7even/roctop/releases/latest/download/roctop_<version>_linux_arm64.tar.gz | tar xz
sudo mv roctop /usr/local/bin/
```

### Build from source

Requires Go 1.24+:

```bash
git clone https://github.com/x7even/roctop.git
cd roctop
go build -o roctop .
```

## Running

```bash
roctop
roctop --refresh 1
```

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `--refresh N` | `2.0` | Refresh interval in seconds (minimum 0.5) |
| `--version` | — | Print version and exit |

## Keybindings

| Key | Action |
|-----|--------|
| `q` / `ctrl+c` | Quit |
| `i` | Toggle info view (static GPU details) |
| `r` | Force refresh |
| `+` / `=` | Increase refresh rate |
| `-` | Decrease refresh rate |
| `p` | Pause / resume |
| `↑` / `↓` / `PgUp` / `PgDn` | Scroll GPU panels |
| Mouse wheel | Scroll GPU panels |

## Project structure

```
roctop/
├── main.go          # Entry point, flag parsing, rocm-smi path check
├── data.go          # rocm-smi JSON collection and parsing
├── render.go        # Bar charts, braille sparkline renderer, colour gradients
├── panel.go         # Per-GPU metric and info panel layouts
├── header.go        # Top status bar
├── process.go       # GPU process table
├── model.go         # Bubble Tea model (Init / Update / View), viewport
├── go.mod
└── .goreleaser.yaml # Release config (linux amd64 + arm64 tarballs)
```

## Data collection

All GPU data comes from `rocm-smi --json`. No Python bindings or C libraries are used.

Key calls:
- `rocm-smi --json --showuse --showmeminfo vram --showmemuse -t --showpower ...` — main metrics (every refresh)
- `rocm-smi --json --showmetrics` — throttle status and PCIe link info (every refresh)
- `rocm-smi --json --showvbios --showmemvendor --showuniqueid --showdriverversion` — static info (once at startup)

## License

[GPL v3](LICENSE) — free to use commercially; modifications must be shared under the same licence.
