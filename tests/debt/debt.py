#!/usr/bin/env python3
"""Hold the debt register against the tree (Plane 445).

  debt.py ROOT                 fail on a problem, warn on what comes up soon
  debt.py ROOT --today DATE    the same, as on DATE (for tests)

tests/debt/register.json lists every patch set and every open Workaround
item, each with a tier, its Plane item, why it stays and the way out (exit).

  tier 1  security path: a review date at most 30 days ahead, a guard test
          (a tests/nix check wired in flake.nix) and, on patches, a guard
  tier 2  upgrade debt: a guard that stops the build when the source moves
  tier 3  hygiene: problems only warn

A guard is the file that pins or asserts the patched source: a version
assert, a pinned rev or a source hash. Where none exists yet, guard and test
name the Plane item that adds it ("Plane 471").

Every tracked *.patch belongs to exactly one patches entry. Members are
regular files inside the tree: no links, no FIFOs, no links in a parent.

Tier 1 review dates in tests/copies/known.json follow the same date rule.
A review date can move later only with a new why and a higher "renewed"
count; in a git checkout this is checked against HEAD. Three renewals or
more need Jade's OK in the why ("Jade YYYY-MM-DD"). How to: docs/debt.md.
"""
import argparse
import datetime
import json
import os
import re
import stat
import subprocess
import sys
from pathlib import Path

REGISTER_PATH = "tests/debt/register.json"
KNOWN_PATH = "tests/copies/known.json"
KINDS = ("patches", "workaround")
FIELDS = {"name", "kind", "members", "tier", "item", "why", "exit", "guard", "test", "review", "renewed"}
NEEDED = {"name", "kind", "members", "tier", "item", "why", "exit"}
PLANE = re.compile(r"Plane [1-9][0-9]*")
ITEM = re.compile(r"[1-9][0-9]*")
GUARD = re.compile(r'\.version == "|rev = "[0-9a-f]{40}"|sha256 = "|hash = "sha256-')
JADE = re.compile(r"Jade [0-9]{4}-[0-9]{2}-[0-9]{2}")
AHEAD = 30
SOON = 7
CAP = 1 << 20


class Failed(Exception):
    pass


def read_inside(root, path):
    """Text of a regular file at root/path, or None.

    The path must be relative, stay inside root and pass no link on the way;
    the file is opened without following a link and without blocking on a
    FIFO, and only a regular file of at most CAP bytes is read."""
    if not isinstance(path, str) or not path or path.startswith("/") or ".." in path.split("/"):
        return None
    full = root / path
    if os.path.realpath(full) != os.path.join(os.path.realpath(root), path):
        return None
    try:
        fd = os.open(full, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        return None
    with os.fdopen(fd, "rb") as handle:
        info = os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > CAP:
            return None
        return handle.read(CAP).decode("utf-8", "replace")


def patch_files(root):
    """Every *.patch the check covers: tracked ones in a git checkout, all in a source tree."""
    if (root / ".git").exists():
        done = subprocess.run(["git", "-C", str(root), "ls-files", "-z", "*.patch"], capture_output=True)
        if done.returncode != 0:
            raise Failed(f"git ls-files: {done.stderr.decode()[:300]}")
        return sorted(p for p in done.stdout.decode().split("\0") if p)
    return sorted(str(p.relative_to(root)) for p in root.rglob("*.patch"))


def at_head(root, path):
    """The committed text of path, or None outside git or when it is new."""
    if not (root / ".git").exists():
        return None
    done = subprocess.run(["git", "-C", str(root), "show", f"HEAD:{path}"], capture_output=True)
    return done.stdout.decode() if done.returncode == 0 else None


class Report:
    def __init__(self):
        self.failures, self.warnings = [], []

    def add(self, soft, line):
        (self.warnings if soft else self.failures).append(line)


def check_entry(root, entry, flake, report, owners):
    """Field, member, guard and test checks of one register entry."""
    name = entry.get("name") if isinstance(entry, dict) else None
    label = f"{REGISTER_PATH} {name if name else repr(entry)}"
    if not isinstance(entry, dict) or set(entry) - FIELDS or not NEEDED <= set(entry):
        report.add(False, f"{label}: want {', '.join(sorted(NEEDED))}; guard, test, review, renewed as needed")
        return
    tier, kind = entry["tier"], entry["kind"]
    if tier not in (1, 2, 3) or kind not in KINDS:
        report.add(False, f"{label}: tier 1, 2 or 3 and kind patches or workaround")
        return
    soft = tier == 3
    if not ITEM.fullmatch(str(entry["item"])) or not str(entry["why"]).strip() or not str(entry["exit"]).strip():
        report.add(soft, f"{label}: a Plane item number, a why and an exit")
    members = entry["members"]
    if not isinstance(members, list) or not members or len(set(members)) != len(members):
        report.add(soft, f"{label}: members is a list of different paths")
        members = []
    for member in members:
        if read_inside(root, member) is None:
            report.add(soft, f"{label}: member {member} is not a regular file in the tree")
        if kind == "patches":
            if not str(member).endswith(".patch"):
                report.add(soft, f"{label}: member {member} is not a .patch file")
            owners.setdefault(member, []).append(name)
    if kind == "patches":
        guard = entry.get("guard")
        if guard is None:
            report.add(soft, f"{label}: a patches entry needs a guard")
        elif not PLANE.fullmatch(str(guard)):
            text = read_inside(root, guard)
            if text is None:
                report.add(soft, f"{label}: guard {guard} is not a regular file in the tree")
            elif not GUARD.search(text):
                report.add(soft, f"{label}: guard {guard} has no version assert, pinned rev or source hash")
    elif "guard" in entry:
        report.add(soft, f"{label}: only patches entries have a guard")
    if tier == 1:
        test = entry.get("test")
        if test is None or "review" not in entry:
            report.add(False, f"{label}: tier 1 needs a test and a review date")
        elif not PLANE.fullmatch(str(test)):
            if not str(test).startswith("tests/nix/") or read_inside(root, test) is None:
                report.add(False, f"{label}: test {test} is not a check file under tests/nix")
            elif "./" + test not in flake:
                report.add(False, f"{label}: test {test} is not wired in flake.nix")
    elif "test" in entry or "review" in entry:
        report.add(soft, f"{label}: test and review only on tier 1")


def check_review(label, entry, today, report):
    """The tier 1 date rule and the Jade OK on the third renewal."""
    try:
        review = datetime.date.fromisoformat(entry["review"])
    except (TypeError, ValueError):
        report.add(False, f"{label}: review {entry.get('review')!r} is not a date")
        return
    renewed = entry.get("renewed", 0)
    if not isinstance(renewed, int) or renewed < 0:
        report.add(False, f"{label}: renewed is a count")
    elif renewed >= 3 and not JADE.search(str(entry.get("why", ""))):
        report.add(False, f"{label}: renewed {renewed} times; the why needs Jade's OK (Jade YYYY-MM-DD)")
    if review < today:
        report.add(False, f"{label}: review was due {review}; review it, then renew it or remove the debt")
    elif review > today + datetime.timedelta(days=AHEAD):
        report.add(False, f"{label}: review {review} is more than {AHEAD} days ahead")
    elif review <= today + datetime.timedelta(days=SOON):
        report.add(True, f"{label}: review due {review}")


def check_renewal(label, old, new, report):
    """A review date that moved later needs a new why and a higher renewed count."""
    if not old or "review" not in old or "review" not in new:
        return
    try:
        moved = datetime.date.fromisoformat(new["review"]) > datetime.date.fromisoformat(old["review"])
    except (TypeError, ValueError):
        return
    if not moved:
        return
    if new.get("why") == old.get("why"):
        report.add(False, f"{label}: review moved to {new['review']} with the same why; say what the review found")
    if new.get("renewed", 0) <= old.get("renewed", 0):
        report.add(False, f"{label}: review moved to {new['review']}; raise renewed")


def entries(text, key):
    data = json.loads(text)
    if not isinstance(data, dict) or not isinstance(data.get(key), list):
        raise Failed(f"want an object with a {key} list")
    return data[key]


def copy_key(entry):
    return (entry.get("kind"), tuple(sorted(entry.get("members", []))))


def run(root, today):
    report = Report()
    register_text = read_inside(root, REGISTER_PATH)
    known_text = read_inside(root, KNOWN_PATH)
    flake = read_inside(root, "flake.nix")
    if register_text is None or known_text is None or flake is None:
        raise Failed(f"{REGISTER_PATH}, {KNOWN_PATH} and flake.nix must be regular files in the tree")
    register = entries(register_text, "debt")
    owners, names = {}, set()
    for entry in register:
        check_entry(root, entry, flake, report, owners)
        if isinstance(entry, dict):
            if entry.get("name") in names:
                report.add(False, f"{REGISTER_PATH} {entry.get('name')}: listed twice")
            names.add(entry.get("name"))
    for patch in patch_files(root):
        if patch not in owners:
            report.add(False, f"{patch}: not in a patches entry of {REGISTER_PATH}")
    for patch, names_of in sorted(owners.items()):
        if len(names_of) > 1:
            report.add(False, f"{patch}: in more than one patches entry ({', '.join(names_of)})")
    copies = [e for e in entries(known_text, "copies") if isinstance(e, dict)]
    dated = [(f"{REGISTER_PATH} {e['name']}", e) for e in register
             if isinstance(e, dict) and e.get("tier") == 1 and "review" in e and "name" in e]
    dated += [(f"{KNOWN_PATH} {', '.join(copy_key(e)[1])}", e) for e in copies if "review" in e]
    for label, entry in dated:
        check_review(label, entry, today, report)
    old_register = at_head(root, REGISTER_PATH)
    if old_register:
        before = {e.get("name"): e for e in entries(old_register, "debt") if isinstance(e, dict)}
        for entry in register:
            if isinstance(entry, dict):
                check_renewal(f"{REGISTER_PATH} {entry.get('name')}", before.get(entry.get("name")), entry, report)
    old_known = at_head(root, KNOWN_PATH)
    if old_known:
        before = {copy_key(e): e for e in entries(old_known, "copies") if isinstance(e, dict)}
        for entry in copies:
            check_renewal(f"{KNOWN_PATH} {', '.join(copy_key(entry)[1])}", before.get(copy_key(entry)), entry, report)
    return report, len(register), len(owners)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("root", type=Path, help="the gjallarOS checkout or source tree")
    parser.add_argument("--today", type=datetime.date.fromisoformat, default=datetime.date.today())
    args = parser.parse_args(argv)
    try:
        report, count, patches = run(args.root, args.today)
    except (Failed, ValueError, KeyError, TypeError, OSError) as failed:
        print(f"debt: {failed}", file=sys.stderr)
        return 2
    print(f"debt: {count} entries, {patches} patches, as of {args.today}")
    for line in report.failures:
        print(line)
    for line in report.warnings:
        print(f"warning: {line}")
    return 1 if report.failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
