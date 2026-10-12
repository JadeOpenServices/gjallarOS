"""Check `oddc security --json` reports against what gjallarctl reads.

Usage: oddc-security-flags.py GJALLARCTL_SOURCE_DIR REPORT_DIR
"""

import json
import pathlib
import re
import sys

source, report_dir = map(pathlib.Path, sys.argv[1:3])

go = "\n".join(
    p.read_text() for p in sorted(source.glob("*.go")) if not p.name.endswith("_test.go")
)

flags = set(re.findall(r'\.Flags\["([A-Za-z0-9]+)"\]', go))
struct = re.search(r"type securityFinding struct \{(.*?)\n\}", go, re.S)
fields = set(re.findall(r'json:"([A-Za-z0-9]+)', struct.group(1))) if struct else set()

errors = []
if not flags:
    errors.append("found no Flags[\"...\"] lookup in gjallarctl; the pattern in this check is stale")
if not fields:
    errors.append("found no securityFinding struct in gjallarctl; the pattern in this check is stale")

seen = set()
for path in sorted(report_dir.glob("*.json")):
    report = json.loads(path.read_text())
    if not isinstance(report.get("findings"), list) or not isinstance(report.get("flags"), dict):
        errors.append(f"{path.name}: want {{\"findings\": [...], \"flags\": {{...}}}}, got keys {sorted(report)}")
        continue
    for name, value in report["flags"].items():
        if not isinstance(value, bool):
            errors.append(f"{path.name}: flag {name} is {type(value).__name__}, gjallarctl reads a bool")
    seen |= report["flags"].keys()
    for finding in report["findings"]:
        missing = sorted(f for f in fields if not isinstance(finding.get(f), str))
        if missing:
            errors.append(f"{path.name}: finding {finding.get('id', '?')} lacks string {', '.join(missing)}")
    print(f"{path.name}: {len(report['findings'])} findings, flags {sorted(report['flags'])}")

for name in sorted(flags - seen):
    errors.append(
        f"gjallarctl reads flag {name}, but no tested model has it at this ODDC revision "
        "(renamed or dropped? ask ODDC before moving the pin)"
    )

print(f"gjallarctl reads flags {sorted(flags)} and finding fields {sorted(fields)}")
for e in errors:
    print("FAIL:", e, file=sys.stderr)
sys.exit(1 if errors else 0)
