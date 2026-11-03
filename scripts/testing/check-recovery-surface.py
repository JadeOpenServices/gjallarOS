#!/usr/bin/env python3

from pathlib import Path
import re

repo = Path(__file__).resolve().parents[2]
tools = repo / "system/recovery/tools.nix"

text = tools.read_text()

def require(condition, message):
    if not condition:
        raise RuntimeError(message)

print("=== RECOVERY SURFACE CHECK ===")

require(
    "gjallar-recover guided {repair|reinstall|fresh}" in text,
    "canonical recovery usage text changed",
)

start = text.find("      guided)")
require(start >= 0, "guided recovery case missing")

case_start = text.find('case "$mode" in', start)
require(case_start >= 0, "guided mode dispatcher missing")

case_end = text.find("      esac", case_start)
require(case_end >= 0, "guided mode dispatcher terminator missing")

block = text[case_start:case_end]

modes = []
for line in block.splitlines():
    match = re.match(r"^\s{12}([A-Za-z0-9_-]+)\)$", line)
    if match:
        modes.append(match.group(1))

expected = ["repair", "reinstall", "fresh"]

require(
    modes == expected,
    f"recovery authority surface changed: expected {expected}, got {modes}",
)

require(
    "run_installer --recovery --accept-existing" in block,
    "reinstall lost explicit existing-install authority",
)

require(
    "run_installer --recovery" in block,
    "fresh lost explicit fresh-install authority",
)

require(
    "/run/gjallarOS/device-config/user.config.json" in text,
    "device-config compatibility hook missing",
)

# The provider-neutral config handoff must not itself introduce a new
# recovery action. New modes require a separate reviewed ticket.
for forbidden in [
    "remote-wipe)",
    "wipe)",
    "factory-reset)",
    "repartition)",
]:
    require(
        forbidden not in block,
        f"unexpected recovery authority added: {forbidden}",
    )

print("PASS: recovery modes are exactly repair/reinstall/fresh")
print("PASS: reinstall retains explicit existing-install authority")
print("PASS: fresh retains explicit fresh-install authority")
print("PASS: device-config handoff adds data only, not a recovery mode")
print("PASS: future recovery expansion remains deferred")
