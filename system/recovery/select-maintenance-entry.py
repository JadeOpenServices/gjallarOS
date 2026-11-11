"""Select the recovery-maintenance boot entry of one system generation.

  select-maintenance-entry.py GENERATION < bootctl-list.json

A rerun after a failed maintenance boot leaves the previous generation's
maintenance entry in place, so matching every "gjallar-recovery-maintenance"
entry found two and refused to arm (e2e-full, 2026-09-29). Only the entry
built with the generation the installer just activated is armed.
"""
import json
import sys

generation = sys.argv[1]
if not generation.isdigit():
    raise SystemExit(f"invalid system generation {generation!r}")

# systemd-boot: nixos-generation-4-specialisation-gjallar-recovery-maintenance.conf
# lanzaboote:   nixos-generation-4-specialisation-gjallar-recovery-maintenance-<hash>.efi
prefix = f"nixos-generation-{generation}-specialisation-gjallar-recovery-maintenance"
matches = [
    entry["id"]
    for entry in json.load(sys.stdin)
    if isinstance(entry.get("id"), str)
    and entry["id"].startswith(prefix)
    and entry["id"][len(prefix):][:1] in ("-", ".")
]

if len(matches) != 1:
    raise SystemExit(
        "expected exactly one gjallarOS recovery-maintenance boot entry "
        f"for generation {generation}, found {len(matches)}"
    )

print(matches[0])
