"""Copy a LibreWolf profile into the Home Manager Firefox profile.

LibreWolf is only read. The Firefox profile is backed up to a tarball first.
SQLite databases go through the backup API from a private copy so a stale
-wal file can never be replayed onto the copied database. Files that Home
Manager owns (user.js, search, extensions, chrome) are left alone.

With --once (used by Home Manager activation) it only runs while Firefox has
never been opened, and leaves a marker so it never runs again.

Usage: gjallar-librewolf-import [--dry-run] [--once] [--src DIR] [--dst DIR]
"""
import argparse
import configparser
import os
import shutil
import sqlite3
import sys
import tarfile
import tempfile
import time
from pathlib import Path

HOME = Path.home()
SKIP = {
    "lock", ".parentlock", "parent.lock",
    "user.js", "search.json.mozlz4",        # Home Manager
    "extensions", "chrome",                 # Home Manager
    "compatibility.ini", "extensions.json", "addonStartup.json.lz4",
    "startupCache", "cache2", "crashes", "minidumps", "datareporting",
    "saved-telemetry-pings", "times.json",
}
SQLITE_SIDE = ("-wal", "-shm", "-journal")


def default_profile(ini_dirs):
    for base in ini_dirs:
        ini = base / "profiles.ini"
        if not ini.is_file():
            continue
        cp = configparser.ConfigParser(strict=False, interpolation=None)
        cp.read(ini)
        # Install sections name the profile a build really opens, then the
        # profile marked Default=1.
        installs, defaults = [], []
        for s in cp.sections():
            if s.startswith("Install") and cp.has_option(s, "Default"):
                installs.append(cp.get(s, "Default"))
            elif s.startswith("Profile") and cp.get(s, "Default", fallback="0") == "1":
                defaults.append(cp.get(s, "Path"))
        paths = installs + defaults
        for p in paths:
            d = base / p if not os.path.isabs(p) else Path(p)
            if d.is_dir():
                return d
    return None


def running(profile):
    lock = profile / "lock"
    if not os.path.islink(lock):
        return False
    target = os.readlink(lock)
    pid = target.rsplit("+", 1)[-1]
    return pid.isdigit() and Path(f"/proc/{pid}").exists()


def is_sqlite(p):
    if p.name.endswith(SQLITE_SIDE) or p.stat().st_size < 100:
        return False
    with open(p, "rb") as f:
        return f.read(16) == b"SQLite format 3\x00"


def copy_sqlite(src, dst, tmp):
    work = Path(tmp) / src.name
    shutil.copy2(src, work)
    for side in SQLITE_SIDE:
        s = src.with_name(src.name + side)
        if s.exists():
            shutil.copy2(s, work.with_name(work.name + side))
    for side in SQLITE_SIDE:
        d = dst.with_name(dst.name + side)
        if d.exists() or d.is_symlink():
            d.unlink()
    if dst.exists():
        dst.unlink()
    a = sqlite3.connect(work)
    b = sqlite3.connect(dst)
    a.backup(b)
    ok = b.execute("PRAGMA integrity_check").fetchone()[0]
    b.close()
    a.close()
    for f in Path(tmp).iterdir():
        f.unlink()
    shutil.copystat(src, dst)
    return ok


def main():
    ap = argparse.ArgumentParser(prog="gjallar-librewolf-import")
    ap.add_argument("--src", type=Path)
    ap.add_argument("--dst", type=Path)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--once", action="store_true",
                    help="import only into a Firefox profile that was never opened")
    a = ap.parse_args()

    src = a.src or default_profile([HOME / ".librewolf", HOME / ".config/librewolf/librewolf"])
    # Firefox keeps using ~/.mozilla while it exists and only falls back to
    # the XDG folder without it; Home Manager writes the profile there too.
    dst = a.dst or default_profile([HOME / ".mozilla/firefox", HOME / ".config/mozilla/firefox"])
    if not src or not dst:
        if a.once:
            return
        sys.exit(f"profile not found: src={src} dst={dst}")
    src, dst = src.resolve(), dst.resolve()
    marker = dst / ".gjallar-librewolf-imported"
    if a.once and (marker.exists() or (dst / "places.sqlite").exists()):
        return
    print(f"from {src}\n  to {dst}")
    if src == dst:
        sys.exit("source and target are the same")
    for p, name in ((src, "LibreWolf"), (dst, "Firefox")):
        if running(p):
            if a.once:
                print(f"{name} is running; LibreWolf import waits for the next switch")
                return
            sys.exit(f"{name} is running ({p}/lock); close it first")

    plan = []
    for root, dirs, files in os.walk(src):
        r = Path(root)
        rel = r.relative_to(src)
        if rel == Path("."):
            dirs[:] = [d for d in dirs if d not in SKIP]
        for f in files:
            if rel == Path(".") and f in SKIP:
                continue
            if f.endswith(SQLITE_SIDE):
                continue
            plan.append(rel / f)
    size = sum((src / p).stat().st_size for p in plan if not (src / p).is_symlink())
    print(f"{len(plan)} files, {size / 2**20:.1f} MiB")
    if a.dry_run:
        for p in plan:
            print("  ", p)
        return

    stamp = time.strftime("%Y%m%d-%H%M%S")
    backup = dst.parent / f"{dst.name}.before-librewolf-{stamp}.tar"
    with tarfile.open(backup, "w") as t:
        t.add(dst, arcname=dst.name)
    print(f"backup {backup}")

    bad, dbs = [], 0
    with tempfile.TemporaryDirectory() as tmp:
        for p in plan:
            s, d = src / p, dst / p
            if s.is_symlink():
                continue
            if d.is_symlink():
                print(f"  keep managed {p}")
                continue
            d.parent.mkdir(parents=True, exist_ok=True)
            if is_sqlite(s):
                ok = copy_sqlite(s, d, tmp)
                dbs += 1
                if ok != "ok":
                    bad.append(f"{p}: {ok}")
            else:
                shutil.copy2(s, d)
    os.utime(dst)
    print(f"copied {len(plan)} files ({dbs} databases)")

    def count(db, sql):
        try:
            c = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
            n = c.execute(sql).fetchone()[0]
            c.close()
            return n
        except sqlite3.Error as e:
            return f"err {e}"
    for name, db, sql in (
        ("history", "places.sqlite", "select count(*) from moz_places"),
        ("bookmarks", "places.sqlite", "select count(*) from moz_bookmarks where type=1"),
        ("cookies", "cookies.sqlite", "select count(*) from moz_cookies"),
        ("permissions", "permissions.sqlite", "select count(*) from moz_perms"),
        ("form history", "formhistory.sqlite", "select count(*) from moz_formhistory"),
    ):
        if (src / db).exists():
            print(f"  {name}: {count(src / db, sql)} -> {count(dst / db, sql)}")
    for f in ("logins.json", "logins.db", "key4.db", "cert9.db", "sessionstore.jsonlz4", "containers.json", "prefs.js"):
        if (src / f).exists():
            print(f"  {f}: {'yes' if (dst / f).exists() else 'missing'}")
    if bad:
        sys.exit("integrity check failed: " + "; ".join(bad))
    marker.write_text(time.strftime("%Y-%m-%d %H:%M:%S\n"))
    print("done; LibreWolf profile untouched")


if __name__ == "__main__":
    main()
