# roctop

A terminal UI for real-time GPU monitoring on Linux — supports AMD (ROCm), NVIDIA, and integrated GPUs. Inspired by the fantastic work of [btop](https://github.com/aristocratos/btop).

roctop has two main views — the metrics view and the static info view (`i`):

![roctop metrics view](https://raw.githubusercontent.com/x7even/roctop/main/roctop-1.1.1-main.png)
*Real-time utilisation, memory, power, temperature, clocks, and more*

![roctop info view](https://raw.githubusercontent.com/x7even/roctop/main/roctop-1.1.1-info.png)
*Static Info GPU details: VBIOS, PCIe topology, memory vendor, driver version*

## Features

- Real-time 2-column GPU grid: utilisation, VRAM, GTT memory (APU/iGPU), power, temperature, clocks, fan
- Multi-row braille sparklines with status colouring (steel, amber past 75%, red past 90%) — variation within narrow ranges stays visible
- PCIe bandwidth monitoring — TX/RX rates with all-time peak tracking (AMD and NVIDIA)
- Throttle detection with reason decoding (THERMAL, POWER_LIMIT, etc.)
- Power limit, voltage and OverDrive readout (AMD) — the cap in force against the board default, the live core voltage, and the undervolt / core-clock / memory-clock offsets applied by tools like `gpucap`, with the range the firmware accepts. Read-only: roctop never writes to the driver
- ECC/RAS error detection — warnings shown in info view with correctable/uncorrectable counts

- Static info view (press `i`): VBIOS, PCIe topology, memory vendor, driver, unique ID
- Per-GPU focus view (press `0–9`): expand any GPU to full width
- Arrow key navigation — cycle between overview and individual GPU focus views
- Process table showing which processes are using VRAM and on which GPUs
- Scrollable GPU panel region — header and process table stay anchored when the terminal is small
- Scrollable event log (press `l`) — rocm-smi errors and backend warnings
- Adjustable refresh rate, pause, and force-refresh keybindings

- Multi-backend: monitors AMD discrete, NVIDIA discrete, and integrated GPUs simultaneously
- Auto-detection: probes for available GPU backends at startup — no configuration needed
- Single static binary — no runtime dependencies beyond system GPU tools

## Supported GPUs

| GPU Type | Backend | Requirement |
|----------|---------|-------------|
| AMD discrete | `rocm-smi` | [ROCm](https://rocm.docs.amd.com/) installed, `rocm-smi` on `$PATH` |
| NVIDIA discrete | `nvidia-smi` | NVIDIA drivers installed, `nvidia-smi` on `$PATH` |
| AMD integrated (iGPU / APU) | `sysfs` | `amdgpu` kernel driver loaded (native Linux) |
| Intel integrated (iGPU) | `sysfs` | `i915` or `xe` kernel driver loaded (native Linux) |

roctop auto-detects all available backends. On a mixed system (e.g., NVIDIA discrete + AMD iGPU), all GPUs appear together in the TUI. The header shows active backends: `[rocm]`, `[nvidia]`, `[nvidia+sysfs]`, etc.

Metrics that aren't available for a given GPU are shown in dark red to distinguish "no data" from a real zero.

Requires Linux and at least one of: `rocm-smi` on PATH, `nvidia-smi` on PATH, or a GPU with sysfs/hwmon support. No other dependencies.

## Installation

### One-line install / upgrade (recommended)

Auto-detects architecture (amd64 / arm64) and installs to `/usr/local/bin`. Re-running upgrades to the latest version:

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

## Usage

```bash
roctop
roctop --refresh 1
roctop --once           # one-shot plain-text snapshot (no TUI, pipe-friendly)
roctop --once --json    # machine-readable snapshot for scripts
```

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `--refresh N` | `2.0` | Refresh interval in seconds (minimum 0.5) |
| `--once` | — | Print one snapshot to stdout and exit (no TUI) |
| `--json` | — | With `--once`: emit the snapshot as JSON |
| `--version` | — | Print version and exit |

## Keybindings

| Key | Action |
|-----|--------|
| `q` / `ctrl+c` | Quit |
| `i` | Toggle info view (static GPU details) |
| `?` | Toggle help panel |
| `l` | Toggle event log |
| `r` | Force refresh |
| `+` / `=` | Increase refresh rate |
| `-` | Decrease refresh rate |
| `p` | Pause / resume |
| `0–9` | Focus GPU by index at full width (same key to toggle off) |
| `←` / `→` | Cycle between overview and individual GPU focus views |
| `↑` / `↓` / `PgUp` / `PgDn` | Scroll GPU panels |
| Mouse wheel | Scroll GPU panels |
| `Esc` | Return to main metrics screen from any mode |

## Data collection

roctop uses three backends depending on what's available:

- **ROCm** (`rocm-smi --json`): Full AMD discrete GPU metrics including VRAM, GTT memory, throttle status, PCIe link info, and static data (VBIOS, memory vendor, unique ID)
- **NVIDIA** (`nvidia-smi --query-gpu`): CSV-based metric collection for NVIDIA GPUs including utilization, VRAM, power, temperature, clocks, and per-process VRAM usage
- **sysfs/hwmon**: Reads directly from `/sys/class/drm/card*/device/` and hwmon interfaces for integrated GPUs — no vendor tools needed; also provides GTT memory data for APUs

At startup, roctop probes ROCm and NVIDIA backends first, records which PCI bus addresses they claim, then discovers any remaining GPUs via sysfs to avoid double-counting.

### Power limit, voltage and OverDrive (AMD)

These come from sysfs on every AMD card, whichever backend supplies the rest of
the metrics:

| Reading | Source |
|---------|--------|
| Cap in force, board default, cap range | `hwmon/power1_cap`, `power1_cap_default`, `power1_cap_min`, `power1_cap_max` |
| Core voltage | `hwmon/in0_input` |
| Core / memory clock ceiling | top level of `pp_dpm_sclk` / `pp_dpm_mclk` |
| Voltage, core-clock and memory-clock offsets + accepted ranges | `pp_od_clk_voltage` |

The `TUNE` row in the metrics view appears only when a card has something to
report, and the power cap is shown there only when it differs from the board
default — the `PWR` bar already shows the cap in force, so what it cannot tell
you is whether something moved it. The info view (`i`) adds the full ranges.

`pp_od_clk_voltage` exists only when the `amdgpu` OverDrive bit is enabled
(`options amdgpu ppfeaturemask=0xfff7ffff`). Without it the OverDrive rows are
simply absent; nothing else is affected. The file's format also varies by
generation — RDNA4 reports offsets, older ASICs report absolute clocks or V/F
curves — so roctop reports a setting only when the driver states it as an
offset it understands, rather than guessing. Note that the OverDrive memory
ceiling is what was *requested*: the DPM table may top out lower, and roctop
shows both.

Reading these is deliberately all roctop does — applying a cap or an undervolt
is the job of a separate tool run as root. roctop opens nothing for writing.

## Project structure

```
roctop/
├── main.go          # Entry point, flag parsing, backend auto-detection
├── data.go          # Backend interface, GPU data model, ROCm backend
├── nvidia.go        # NVIDIA backend (nvidia-smi CSV queries)
├── sysfs.go         # sysfs/hwmon backend (iGPU metrics from kernel)
├── amdsysfs.go      # AMD sysfs collector (metrics for cards any backend found)
├── od.go            # pp_od_clk_voltage parser (power/voltage/clock tunables)
├── render.go        # Bar charts, braille sparkline renderer, colour gradients
├── panel.go         # Per-GPU metric and info panel layouts
├── header.go        # Top status bar
├── help.go          # Help panel renderer
├── process.go       # GPU process table
├── model.go         # Bubble Tea model (Init / Update / View), viewport
├── applog.go        # In-memory event log (backend warnings, rocm-smi errors)
├── logpanel.go      # Event log panel renderer
├── go.mod
└── .goreleaser.yaml # Release config (linux amd64 + arm64 tarballs)
```

## License

[GPL v3](LICENSE) — free to use commercially; modifications must be shared under the same licence.
