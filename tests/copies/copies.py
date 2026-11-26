#!/usr/bin/env python3
"""Find copied code and hold it against the known list (Plane 445).

  copies.py ROOT            fail on a copy that is not listed and on a
                            listed one that is gone
  copies.py ROOT --list     every copy found, with lines
  copies.py ROOT --new      list entries to fill in for unlisted copies
  copies.py ROOT --sort     write the list back in its order

Two kinds:

  whole  a body of 4+ code lines that is the same in another body
  run    8+ code lines in a row that are the same in otherwise different bodies

A body is a function in the Go, Python, JavaScript and Lua that lizard
reads, and a whole file in Nix, shell, QML and Luau, which lizard cannot
read. Lines are compared stripped, with whitespace collapsed; blank lines,
comments, lone brackets and the signature do not count. A line counts only
in the innermost function around it. Tests are left out.

tests/copies/known.json lists each kept copy by its members, with a tier
(1 on a security path), the Plane item and why it stays; tier 1 also has a
review date. The rules come from JODS scripts/lint/copies, so both
projects list copies the same way. How to: docs/copies.md.
"""
import argparse
import csv
import datetime
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from collections import defaultdict
from pathlib import Path

HERE = Path(__file__).resolve().parent
KNOWN = HERE / "known.json"
KNOWN_PATH = "tests/copies/known.json"
WHOLE = 4
RUN = 8
# lizard picks the language by the file name's extension.
FUNCTIONS = (".go", ".py", ".js", ".lua")
FILES = (".nix", ".sh", ".qml", ".luau")
TEST = re.compile(r"(^|/)(tests?|testdata)/|_test\.go$|(^|/)test_[^/]*\.py$")
COMMENT = re.compile(r"^(//|#|/\*|\*|--)")
TRIVIAL = {
    "}", "{", ")", "(", "]", "[", "});", ");", "},", "};", "];", "),", "}))", "} else {",
    "return nil", "return err", "in", "let", "''", "'';", "fi", "done", "esac", ";;",
    "else", "then", "do", "end",
}
ITEM = re.compile(r"[1-9][0-9]*")
KINDS = ("whole", "run")
FIELDS = {"kind", "members", "tier", "item", "why", "review"}


class Failed(Exception):
    pass


def normalize(line):
    """The line as compared, or None when it does not count."""
    line = re.sub(r"\s+", " ", line.strip())
    if not line or COMMENT.match(line) or line in TRIVIAL:
        return None
    return line


def code_files(root):
    """Paths under root, relative, that the check reads.

    In a git checkout only tracked files count; the flake check gets a
    source tree without .git that holds only the files it should read.
    Links and FIFOs are left out, so a reader is never pointed at /dev/zero."""
    if (root / ".git").exists():
        done = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True)
        if done.returncode != 0:
            raise Failed(f"git ls-files: {done.stderr.decode()[:300]}")
        paths = [p for p in done.stdout.decode().split("\0") if p]
    else:
        paths = [str(p.relative_to(root)) for p in sorted(root.rglob("*"))]
    keep = []
    for path in paths:
        full = root / path
        if not path.endswith(FUNCTIONS + FILES) or TEST.search(path):
            continue
        if full.is_symlink() or not full.is_file():
            continue
        keep.append(path)
    return sorted(keep)


def lizard(root, files):
    """lizard's CSV rows: nloc, ccn, tokens, params, length, location, path, name, signature, start, end."""
    if not files:
        return []
    with tempfile.NamedTemporaryFile("w", suffix=".txt") as listing:
        listing.write("\n".join(files) + "\n")
        listing.flush()
        done = subprocess.run(["lizard", "--csv", "-f", listing.name], cwd=root,
                              capture_output=True, text=True)
    if done.returncode != 0:
        raise Failed(f"lizard: {done.stderr.strip()[:300]}")
    rows = list(csv.reader(done.stdout.splitlines()))
    for row in rows:
        row[-5] = os.path.normpath(row[-5])
    return rows


def body_start(lines, start, end, path):
    """First line after the signature: the one that opens the body ends in { or, in Python, in :."""
    if path.endswith(".lua"):
        return start + 1
    opener, comment = (":", "#") if path.endswith(".py") else ("{", "//")
    for number in range(start, min(end, len(lines)) + 1):
        if lines[number - 1].split(comment)[0].rstrip().endswith(opener):
            return number + 1
    return start + 1


def functions(rows, read):
    """[(member, path, start, body)] per function; body is [(line number, text)].

    member is "path name", with " #2" on the second function of that name in a file."""
    by_file = defaultdict(list)
    for row in rows:
        path, name, start, end = row[-5], row[-4] or "(anonymous)", int(row[-2]), int(row[-1])
        by_file[path].append((start, end, name))
    found = []
    for path, spans in sorted(by_file.items()):
        found += file_functions(path, spans, read(path))
    return found


def file_functions(path, spans, lines):
    """functions() for one file; nested functions are left out of the outer body."""
    seen, found = defaultdict(int), []
    for start, end, name in sorted(spans):
        seen[name] += 1
        inner = {n for s, e, _ in spans if (s, e) != (start, end) and start <= s and e <= end
                 for n in range(s, e + 1)}
        first = body_start(lines, start, end, path)
        body = [(n, normalize(lines[n - 1])) for n in range(first, min(end, len(lines)) + 1)
                if n not in inner]
        member = f"{path} {name}" + (f" #{seen[name]}" if seen[name] > 1 else "")
        found.append((member, path, start, [(n, text) for n, text in body if text]))
    return found


def whole_file(path, lines):
    """The one body of a file lizard cannot split; its member is the path."""
    body = [(n, normalize(line)) for n, line in enumerate(lines, 1)]
    return (path, path, 1, [(n, text) for n, text in body if text])


def digest(texts):
    return hashlib.sha256("\n".join(texts).encode()).hexdigest()


def whole_copies(units):
    """{frozenset of members: [(member, first line)]} for bodies found twice or more."""
    same = defaultdict(list)
    for member, _path, start, body in units:
        if len(body) >= WHOLE:
            same[digest(text for _, text in body)].append((member, start))
    return {frozenset(m for m, _ in places): sorted(places) for places in same.values() if len(places) > 1}


def run_copies(units, whole):
    """{frozenset of members: [(member, first line of the run)]} for runs of RUN lines.

    A run shared by exactly the members of a whole copy is that copy."""
    windows = defaultdict(list)
    for member, _path, _start, body in units:
        for at in range(len(body) - RUN + 1):
            windows[digest(text for _, text in body[at:at + RUN])].append((member, body[at][0]))
    runs = {}
    for places in windows.values():
        members = frozenset(m for m, _ in places)
        if len(members) < 2 or members in whole:
            continue
        first = runs.setdefault(members, {})
        for member, line in places:
            first[member] = min(first.get(member, line), line)
    return {members: sorted(first.items()) for members, first in runs.items()}


def found_copies(units):
    whole = whole_copies(units)
    return {**{("whole", m): p for m, p in whole.items()},
            **{("run", m): p for m, p in run_copies(units, whole).items()}}


def read_known(text):
    """{(kind, frozenset members): entry} and the problems with the list itself."""
    known, problems = {}, []
    for number, entry in enumerate(json.loads(text)["copies"], 1):
        name = f"{KNOWN_PATH} entry {number}"
        if not isinstance(entry, dict) or set(entry) - FIELDS or not FIELDS - {"review"} <= set(entry):
            problems.append(f"{name}: want kind, members, tier, item, why, review on tier 1")
            continue
        key = (entry["kind"], frozenset(entry["members"]))
        if entry["kind"] not in KINDS or len(key[1]) < 2 or len(key[1]) != len(entry["members"]):
            problems.append(f"{name}: kind is whole or run, members two or more different bodies")
        if entry["tier"] not in (1, 2, 3) or not ITEM.fullmatch(str(entry["item"])) or not entry["why"].strip():
            problems.append(f"{name}: tier 1, 2 or 3, a Plane item number and a why")
        if (entry["tier"] == 1) != ("review" in entry):
            problems.append(f"{name}: a review date on tier 1 and only there")
        elif "review" in entry:
            # How far out it may be is checked with the debt register.
            datetime.date.fromisoformat(entry["review"])
        if key in known:
            problems.append(f"{name}: listed twice")
        known[key] = entry
    return known, problems


def compare(found, known):
    """Unlisted copies and listed ones not found, as printable lines."""
    new = [f"new {kind} copy: " + ", ".join(f"{m} (line {line})" for m, line in found[(kind, members)])
           for kind, members in sorted(set(found) - set(known), key=label)]
    gone = [f"listed {kind} copy not found: " + ", ".join(sorted(members))
            + "; one side changed or it was cleaned up, update the list"
            for kind, members in sorted(set(known) - set(found), key=label)]
    return new, gone


def label(key):
    kind, members = key
    return (kind, sorted(members))


def skeleton(key):
    kind, members = key
    return {"kind": kind, "members": sorted(members), "tier": 2, "item": "", "why": ""}


def render(entries):
    """The list in a stable order, one entry per block."""
    ordered = sorted(entries, key=lambda e: (e["kind"], sorted(e["members"])))
    for entry in ordered:
        entry["members"] = sorted(entry["members"])
    return json.dumps({"copies": ordered}, indent=2, ensure_ascii=False) + "\n"


def scan(root):
    files = code_files(root)

    def read(path):
        return (root / path).read_text("utf-8", "replace").splitlines()
    units = functions(lizard(root, [f for f in files if f.endswith(FUNCTIONS)]), read)
    units += [whole_file(f, read(f)) for f in files if f.endswith(FILES)]
    return found_copies(units)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("root", type=Path, help="the gjallarOS checkout or source tree")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--list", action="store_true")
    mode.add_argument("--new", action="store_true")
    mode.add_argument("--sort", action="store_true")
    args = parser.parse_args(argv)
    if args.sort:
        KNOWN.write_text(render(json.loads(KNOWN.read_text(encoding="utf-8"))["copies"]), encoding="utf-8")
        return 0
    try:
        known, problems = read_known((args.root / KNOWN_PATH).read_text(encoding="utf-8"))
        found = scan(args.root.resolve())
    except (Failed, ValueError, KeyError, TypeError, OSError) as failed:
        print(f"copies: {failed}", file=sys.stderr)
        return 2
    if args.list:
        for kind, members in sorted(found, key=label):
            print(f"{kind}: " + ", ".join(f"{m} (line {line})" for m, line in found[(kind, members)]))
        return 0
    if args.new:
        print(json.dumps([skeleton(key) for key in sorted(set(found) - set(known), key=label)], indent=2))
        return 0
    new, gone = compare(found, known)
    print(f"copies: {len(found)} found, {len(known)} listed")
    for line in problems + new + gone:
        print(line)
    if new:
        print(f"remove the copy, or list it in {KNOWN_PATH}: copies.py ROOT --new prints the entries")
    return 1 if problems or new or gone else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
