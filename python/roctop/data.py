"""rocm-smi data collection and parsing."""
from __future__ import annotations

import json
import re
import subprocess
from collections import deque
from dataclasses import dataclass, field

MAX_HISTORY = 120  # samples kept per metric (2 min at 2s refresh)

ROCM_SMI = "rocm-smi"

ROCM_SMI_FLAGS = [
    "--showuse",
    "--showmeminfo", "vram",
    "--showmemuse",
    "-t",
    "--showpower",
    "--showmaxpower",
    "--showfan",
    "--showclocks",
    "--showvoltage",
    "--showproductname",
    "--showperflevel",
    "--showbus",
    "--showpids",
]

# Throttle status bit descriptions (AMD GPU throttle bitmask)
_THROTTLE_BITS: dict[int, str] = {
    0:  "POWER_LIMIT",
    1:  "THERMAL",
    2:  "CURRENT",
    3:  "VOLTAGE",
    4:  "GPU_CON",
    5:  "SOC",
    16: "PPT0",
    17: "PPT1",
    18: "PPT2",
    19: "PPT3",
    20: "FIT",
    21: "GFX_DUTY_CYCLE",
    22: "VR_TEMP",
}


def _throttle_reasons(status: int) -> list[str]:
    if status == 0:
        return []
    return [name for bit, name in _THROTTLE_BITS.items() if status & (1 << bit)]


@dataclass
class GpuData:
    card_id: int
    name: str = "AMD GPU"
    # Temperatures (°C)
    temp_edge: float = 0.0
    temp_junction: float = 0.0
    temp_memory: float = 0.0
    # Utilization (%)
    gpu_use: float = 0.0
    mem_activity: float = 0.0       # GPU Memory Read/Write Activity
    umc_activity: float = 0.0       # Universal Memory Controller activity
    # VRAM
    vram_total: int = 0             # bytes
    vram_used: int = 0              # bytes
    vram_percent: float = 0.0       # %
    # Power (W)
    power_avg: float = 0.0
    power_max: float = 300.0
    # Fan
    fan_rpm: int = 0
    fan_percent: float = 0.0
    # Clocks (MHz)
    sclk: int = 0
    mclk: int = 0
    # Electrical
    voltage: float = 0.0            # mV
    # Throttling
    throttle_status: int = 0        # bitmask; 0 = not throttled
    throttle_reasons: list[str] = field(default_factory=list)
    # PCIe
    pcie_bus: str = ""              # e.g. "0000:03:00.0"
    pcie_speed: str = ""            # e.g. "16.0GT/s"
    pcie_width: int = 0             # lanes
    pcie_root_port: str = ""        # e.g. "00:01.1" — root port from sysfs topology
    # Static info (fetched once)
    vendor: str = ""
    sku: str = ""
    gfx_version: str = ""
    vbios: str = ""
    mem_vendor: str = ""
    driver_version: str = ""
    perf_level: str = ""
    unique_id: str = ""


@dataclass
class ProcessData:
    pid: int
    name: str
    gpu_ids: list[int]
    vram_used: int  # bytes


@dataclass
class GpuHistory:
    card_id: int
    gpu_use: deque[float] = field(default_factory=lambda: deque(maxlen=MAX_HISTORY))
    power: deque[float] = field(default_factory=lambda: deque(maxlen=MAX_HISTORY))
    temp_junction: deque[float] = field(default_factory=lambda: deque(maxlen=MAX_HISTORY))


# ── Parsing helpers ──────────────────────────────────────────────────────────

def _f(val: str, default: float = 0.0) -> float:
    """Parse a string to float, stripping non-numeric suffixes."""
    try:
        return float(re.sub(r"[^\d.\-]", "", val))
    except (ValueError, TypeError):
        return default


def _i(val: str, default: int = 0) -> int:
    return int(_f(val, default))


def _mhz(val: str) -> int:
    """Extract MHz from strings like '(3305Mhz)' or '3305'."""
    m = re.search(r"(\d+)\s*[Mm]hz", val)
    return int(m.group(1)) if m else _i(val)


def _run_json(*extra_flags: str) -> dict:
    """Run rocm-smi --json with given flags and return parsed JSON."""
    try:
        result = subprocess.run(
            [ROCM_SMI, "--json"] + list(extra_flags),
            capture_output=True, text=True, timeout=5,
        )
        return json.loads(result.stdout)
    except (subprocess.TimeoutExpired, json.JSONDecodeError, FileNotFoundError, ValueError):
        return {}


# ── Main metrics parser ──────────────────────────────────────────────────────

def _parse_gpu(card_id: int, d: dict) -> GpuData:
    gpu = GpuData(card_id=card_id)

    # Product name
    series = d.get("Card Series", "")
    model  = d.get("Card Model", d.get("Card model", ""))
    gpu.name = (series or model or f"GPU {card_id}").strip()

    # Vendor / SKU
    gpu.vendor = d.get("Card Vendor", "").strip()
    gpu.sku    = d.get("Card SKU",    "").strip()
    gpu.gfx_version = d.get("GFX Version", "").strip()

    # Temperatures
    gpu.temp_edge     = _f(d.get("Temperature (Sensor edge) (C)",     "0"))
    gpu.temp_junction = _f(d.get("Temperature (Sensor junction) (C)", "0"))
    gpu.temp_memory   = _f(d.get("Temperature (Sensor memory) (C)",   "0"))

    # Utilization
    gpu.gpu_use      = _f(d.get("GPU use (%)", "0"))
    gpu.mem_activity = _f(d.get("GPU Memory Read/Write Activity (%)", "0"))
    gpu.umc_activity = _f(d.get("Memory Activity", "0"))

    # VRAM
    gpu.vram_total = _i(d.get("VRAM Total Memory (B)", "0"))
    gpu.vram_used  = _i(d.get("VRAM Total Used Memory (B)", "0"))
    if gpu.vram_total > 0:
        gpu.vram_percent = gpu.vram_used / gpu.vram_total * 100
    else:
        gpu.vram_percent = _f(d.get("GPU Memory Allocated (VRAM%)", "0"))

    # Power
    gpu.power_avg = _f(d.get("Average Graphics Package Power (W)", "0"))
    gpu.power_max = _f(d.get("Max Graphics Package Power (W)", "300"))
    if gpu.power_max == 0:
        gpu.power_max = 300.0

    # Fan
    gpu.fan_percent = _f(d.get("Fan speed (%)", "0"))
    gpu.fan_rpm     = _i(d.get("Fan RPM", "0"))

    # Clocks — keys have a trailing colon in rocm-smi JSON
    for key, val in d.items():
        kl = key.lower()
        if "sclk" in kl and "clock speed" in kl:
            gpu.sclk = _mhz(val)
        elif "mclk" in kl and "clock speed" in kl:
            gpu.mclk = _mhz(val)

    # Voltage
    gpu.voltage = _f(d.get("Voltage (mV)", "0"))

    # Performance level
    gpu.perf_level = d.get("Performance Level", "").strip()

    # PCIe bus address
    gpu.pcie_bus = d.get("PCI Bus", "").strip()

    return gpu


def _parse_processes(system: dict) -> list[ProcessData]:
    procs: dict[int, ProcessData] = {}
    for key, val in system.items():
        if not key.startswith("PID"):
            continue
        try:
            pid = int(key[3:])
        except ValueError:
            continue
        # format: "process_name, gpu_index, vram_bytes, sdma, cu_occupancy"
        parts = [p.strip() for p in str(val).split(",")]
        if len(parts) < 3:
            continue
        name = parts[0]
        try:
            gpu_id = int(parts[1])
        except ValueError:
            gpu_id = -1
        try:
            vram = int(parts[2])
        except ValueError:
            vram = 0

        if pid in procs:
            if gpu_id >= 0 and gpu_id not in procs[pid].gpu_ids:
                procs[pid].gpu_ids.append(gpu_id)
            procs[pid].vram_used += vram
        else:
            procs[pid] = ProcessData(
                pid=pid,
                name=name,
                gpu_ids=[gpu_id] if gpu_id >= 0 else [],
                vram_used=vram,
            )
    return sorted(procs.values(), key=lambda p: p.vram_used, reverse=True)


# ── Supplemental data collectors ─────────────────────────────────────────────

def _apply_metrics(gpus: list[GpuData]) -> None:
    """Fetch --showmetrics and overlay throttle + PCIe lane data."""
    data = _run_json("--showmetrics")
    by_id = {g.card_id: g for g in gpus}
    for key, val in data.items():
        if not key.lower().startswith("card") or not isinstance(val, dict):
            continue
        try:
            card_id = int(key[4:])
        except ValueError:
            continue
        gpu = by_id.get(card_id)
        if gpu is None:
            continue
        # Throttle status
        ts = val.get("throttle_status", val.get("Throttle status", 0))
        try:
            gpu.throttle_status = int(ts)
        except (ValueError, TypeError):
            gpu.throttle_status = 0
        gpu.throttle_reasons = _throttle_reasons(gpu.throttle_status)

        # PCIe lane info (from metrics)
        width = val.get("pcie_link_width", val.get("PCIe Link Width", ""))
        speed_raw = val.get("pcie_link_speed", "")
        gpu.pcie_width = _i(str(width)) if width else gpu.pcie_width
        if speed_raw:
            # speed in units of 0.1 GT/s — convert to GT/s string
            try:
                gts = int(speed_raw) / 10
                gpu.pcie_speed = f"{gts:.1f}GT/s" if gts else gpu.pcie_speed
            except (ValueError, TypeError):
                pass


def _pcie_root_port(pcie_bus: str) -> str:
    """
    Walk the sysfs device path for pcie_bus and return the root port address.

    Example path:
      /sys/devices/pci0000:00/0000:00:01.1/0000:01:00.0/0000:02:00.0/0000:03:00.0
    Returns "00:01.1" (the first bridge below the root complex).
    """
    if not pcie_bus:
        return ""
    import os
    sysfs = f"/sys/bus/pci/devices/{pcie_bus}"
    try:
        real = os.path.realpath(sysfs)
    except OSError:
        return ""
    # Extract all BDF segments from the resolved path
    parts = re.findall(r"([0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F])", real)
    # parts[0] is the root port (first PCI device below the root complex).
    # Sanity check: root port should differ from the GPU's own bus address.
    gpu_bdf = re.sub(r"^0000:", "", pcie_bus)
    if parts and re.sub(r"^0000:", "", parts[0]) != gpu_bdf:
        return re.sub(r"^0000:", "", parts[0])
    return ""


def collect_static_info(gpus: list[GpuData]) -> None:
    """
    Fetch one-time static GPU info (VBIOS, memory vendor, driver, unique ID,
    PCIe root port) and write it into the GpuData objects in-place.
    """
    by_id = {g.card_id: g for g in gpus}

    # VBIOS versions
    for key, val in _run_json("--showvbios").items():
        if key.lower().startswith("card") and isinstance(val, dict):
            try:
                card_id = int(key[4:])
            except ValueError:
                continue
            gpu = by_id.get(card_id)
            if gpu:
                gpu.vbios = val.get("VBIOS version", "").strip()

    # Memory vendor
    for key, val in _run_json("--showmemvendor").items():
        if key.lower().startswith("card") and isinstance(val, dict):
            try:
                card_id = int(key[4:])
            except ValueError:
                continue
            gpu = by_id.get(card_id)
            if gpu:
                gpu.mem_vendor = val.get("GPU memory vendor", "").strip()

    # Unique ID
    for key, val in _run_json("--showuniqueid").items():
        if key.lower().startswith("card") and isinstance(val, dict):
            try:
                card_id = int(key[4:])
            except ValueError:
                continue
            gpu = by_id.get(card_id)
            if gpu:
                gpu.unique_id = val.get("Unique ID", "").strip()

    # PCIe root port (sysfs topology walk — no subprocess needed)
    for gpu in gpus:
        if gpu.pcie_bus and not gpu.pcie_root_port:
            gpu.pcie_root_port = _pcie_root_port(gpu.pcie_bus)

    # Driver version (system-level, same for all GPUs)
    drv_data = _run_json("--showdriverversion")
    drv = ""
    for key, val in drv_data.items():
        if isinstance(val, dict):
            drv = val.get("Driver version", "").strip() or drv
        elif key.lower() == "driver version":
            drv = str(val).strip()
    if drv:
        for gpu in gpus:
            gpu.driver_version = drv


# ── Main collection entry point ───────────────────────────────────────────────

def collect_gpu_data() -> tuple[list[GpuData], list[ProcessData]]:
    """Run rocm-smi --json and return parsed GPU and process data."""
    data = _run_json(*ROCM_SMI_FLAGS)
    if not data:
        return [], []

    gpus: list[GpuData] = []
    processes: list[ProcessData] = []

    for key, val in data.items():
        if key.lower().startswith("card") and isinstance(val, dict):
            try:
                card_id = int(key[4:])
            except ValueError:
                continue
            gpus.append(_parse_gpu(card_id, val))
        elif key.lower() == "system" and isinstance(val, dict):
            processes = _parse_processes(val)

    gpus.sort(key=lambda g: g.card_id)

    # Overlay throttle + PCIe info from --showmetrics
    if gpus:
        _apply_metrics(gpus)

    return gpus, processes
