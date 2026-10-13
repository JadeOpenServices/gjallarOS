"""Copy a LibreWolf profile into the Home Manager Firefox profile.

LibreWolf is only read. The Firefox profile is backed up to a tarball first.
SQLite databases go through the backup API from a private copy so a stale
-wal file can never be replayed onto the copied database. Files that Home
Manager owns (user.js, search, extensions, chrome) are left alone.

Add-ons that Home Manager installs are links into the Nix store and are left
to Home Manager. Add-ons the user installed by hand in LibreWolf are plain
.xpi files; they are copied with their data, so they keep their settings.

With --once (used by Home Manager activation) the full import only runs while
Firefox has never been opened, and leaves a marker so it never runs again.
Profiles imported before hand-installed add-ons were copied get only that
step, once, with a second marker.

--extensions copies only the hand-installed add-ons, for a Firefox profile
that is already in use.

Usage: gjallar-librewolf-import [--dry-run] [--once] [--extensions] [--src DIR] [--dst DIR]
"""
import argparse
import configparser
import json
import os
import re
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
EXT_MARKER = ".gjallar-librewolf-extensions-imported"


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


def ext_uuids(profile):
    try:
        prefs = (profile / "prefs.js").read_text(errors="replace")
    except OSError:
        return {}
    m = re.search(r'user_pref\("extensions\.webextensions\.uuids", "(.*)"\);', prefs)
    if not m:
        return {}
    try:
        return json.loads(json.loads(f'"{m.group(1)}"'))
    except ValueError:
        return {}


def copy_tree(s, d, tmp, bad):
    """Copy a file or folder, SQLite files through copy_sqlite."""
    files = [s] if s.is_file() else [p for p in s.rglob("*") if p.is_file()]
    for f in files:
        if f.is_symlink() or f.name.endswith(SQLITE_SIDE):
            continue
        t = d / f.relative_to(s) if s.is_dir() else d
        t.parent.mkdir(parents=True, exist_ok=True)
        if is_sqlite(f):
            ok = copy_sqlite(f, t, tmp)
            if ok != "ok":
                bad.append(f"{f}: {ok}")
        else:
            shutil.copy2(f, t)


def copy_extensions(src, dst, tmp, bad):
    """Copy hand-installed add-ons (plain .xpi files) and their data.

    Firefox finds them in the profile on the next start and keeps them
    enabled (autoDisableScopes leaves the profile scope alone). Data is keyed
    by the add-on's internal UUID, so it is only copied when Firefox uses the
    same UUID as LibreWolf; otherwise the add-on starts empty.
    """
    sx, dx = src / "extensions", dst / "extensions"
    if not sx.is_dir():
        return []
    src_ids, dst_ids = ext_uuids(src), ext_uuids(dst)
    done = []
    for x in sorted(sx.glob("*.xpi")):
        if x.is_symlink():
            continue
        t = dx / x.name
        if t.exists() or t.is_symlink():
            continue
        dx.mkdir(exist_ok=True)
        shutil.copy2(x, t)
        ext_id = x.name[:-4]
        uuid = src_ids.get(ext_id)
        if uuid and dst_ids.get(ext_id) == uuid:
            for d in (src / "storage/default").glob(f"moz-extension+++{uuid}*"):
                if (dst / "storage/default" / d.name).exists():
                    shutil.rmtree(dst / "storage/default" / d.name)
                copy_tree(d, dst / "storage/default" / d.name, tmp, bad)
            data = src / "browser-extension-data" / ext_id
            if data.is_dir():
                copy_tree(data, dst / "browser-extension-data" / ext_id, tmp, bad)
            done.append(ext_id)
        else:
            done.append(f"{ext_id} (without its data)")
    return done


def main():
    ap = argparse.ArgumentParser(prog="gjallar-librewolf-import")
    ap.add_argument("--src", type=Path)
    ap.add_argument("--dst", type=Path)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--once", action="store_true",
                    help="import only into a Firefox profile that was never opened")
    ap.add_argument("--extensions", action="store_true",
                    help="copy only the add-ons installed by hand, with their data")
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
    ext_marker = dst / EXT_MARKER
    only_ext = a.extensions
    if a.once and (marker.exists() or (dst / "places.sqlite").exists()):
        # Imported before add-ons were copied: do that step once.
        if not marker.exists() or ext_marker.exists():
            return
        only_ext = True
    print(f"from {src}\n  to {dst}")
    if src == dst:
        sys.exit("source and target are the same")
    for p, name in ((src, "LibreWolf"), (dst, "Firefox")):
        if running(p):
            if a.once:
                print(f"{name} is running; LibreWolf import waits for the next switch")
                return
            sys.exit(f"{name} is running ({p}/lock); close it first")
    if only_ext:
        if a.dry_run:
            for x in sorted((src / "extensions").glob("*.xpi")):
                if not x.is_symlink() and not (dst / "extensions" / x.name).exists():
                    print("  add-on", x.name[:-4])
            return
        bad = []
        with tempfile.TemporaryDirectory() as tmp:
            done = copy_extensions(src, dst, tmp, bad)
        for e in done:
            print(f"  add-on {e}")
        if bad:
            sys.exit("integrity check failed: " + "; ".join(bad))
        ext_marker.write_text(time.strftime("%Y-%m-%d %H:%M:%S\n"))
        return

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
        added = copy_extensions(src, dst, tmp, bad)
    os.utime(dst)
    print(f"copied {len(plan)} files ({dbs} databases)")
    for e in added:
        print(f"  add-on {e}")

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
    ext_marker.write_text(time.strftime("%Y-%m-%d %H:%M:%S\n"))
    print("done; LibreWolf profile untouched")


if __name__ == "__main__":
    main()
