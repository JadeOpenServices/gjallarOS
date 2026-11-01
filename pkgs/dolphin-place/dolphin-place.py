"""Add a folder to the Places panel (KIO user-places.xbel), or take it out.

    dolphin-place add PATH TITLE ICON
    dolphin-place remove PATH

Nextcloud adds ~/Nextcloud at activation, at enrollment and at each login;
gjallar-wine adds and removes each app's files folder. A fresh account has
no user-places.xbel until a KDE file view first opens, so the activation
alone never added the entry (2026-10-09). A missing file gets the Home
entry KIO itself writes plus the new one and no kde_places_version, so KIO
appends the rest of its defaults and skips the URLs already present.
"""
import html
import os
import re
import sys
import time
from urllib.parse import quote, unquote
from xml.sax.saxutils import escape, quoteattr

USAGE = "usage: dolphin-place add PATH TITLE ICON | dolphin-place remove PATH"

home = os.path.expanduser("~")
data = os.environ.get("XDG_DATA_HOME") or os.path.join(home, ".local", "share")
places = os.path.join(data, "user-places.xbel")

BOOKMARK = re.compile(r'<bookmark href="([^"]*)"')


def url(path):
    # The characters QUrl leaves as they are, so the entry reads like one
    # KIO wrote.
    return "file://" + quote(path, safe="/!$&'()*+,;=:@-._~")


def bookmark(path, title, icon_name, ident, system=False):
    extra = "\n    <isSystemItem>true</isSystemItem>" if system else ""
    return (
        f" <bookmark href={quoteattr(url(path))}>\n"
        f"  <title>{escape(title)}</title>\n"
        "  <info>\n"
        '   <metadata owner="http://freedesktop.org">\n'
        f"    <bookmark:icon name={quoteattr(icon_name)}/>\n"
        "   </metadata>\n"
        '   <metadata owner="http://www.kde.org">\n'
        f"    <ID>{ident}</ID>{extra}\n"
        "   </metadata>\n"
        "  </info>\n"
        " </bookmark>\n"
    )


def find(text, path):
    """Where the entry for path starts, compared by the path it names, so an
    entry KIO wrote with its own escaping is found too; -1 if none."""
    for match in BOOKMARK.finditer(text):
        href = html.unescape(match.group(1))
        if href.startswith("file://") and unquote(href[len("file://"):]) == path:
            return match.start()
    return -1


def write(text):
    os.makedirs(data, exist_ok=True)
    tmp = f"{places}.gjallar-place.{os.getpid()}"
    with open(tmp, "w", encoding="utf-8") as handle:
        handle.write(text)
    os.chmod(tmp, 0o600)
    os.replace(tmp, places)


def read():
    try:
        with open(places, encoding="utf-8") as handle:
            return handle.read()
    except FileNotFoundError:
        return None


def add(path, title, icon):
    # A folder that does not exist yet would be a dead entry.
    if not os.path.isdir(path):
        return 0
    text = read()
    now = int(time.time())
    if text is None:
        write(
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            "<!DOCTYPE xbel>\n"
            '<xbel xmlns:bookmark="http://www.freedesktop.org/standards/desktop-bookmarks"'
            ' xmlns:kdepriv="http://www.kde.org/kdepriv"'
            ' xmlns:mime="http://www.freedesktop.org/standards/shared-mime-info">\n'
            + bookmark(home, "Home", "user-home", f"{now}/0", system=True)
            + bookmark(path, title, icon, f"{now}/900")
            + "</xbel>\n"
        )
        return 0
    if find(text, path) != -1:
        return 0
    # KIO's IDs are the time and a counter; the count of entries keeps two
    # added in the same second apart.
    entry = bookmark(path, title, icon, f"{now}/{900 + text.count('<bookmark ')}")
    # Next to the other local places: before Network, else before Trash,
    # else at the end.
    for anchor in ('<bookmark href="remote:/">', '<bookmark href="trash:/">', "</xbel>"):
        at = text.find(anchor)
        if at == -1:
            continue
        at = text.rfind("\n", 0, at) + 1
        write(text[:at] + entry + text[at:])
        return 0
    print(f"{places}: not an XBEL file, {path} not added", file=sys.stderr)
    return 1


def remove(path):
    text = read()
    if text is None:
        return 0
    at = find(text, path)
    if at == -1:
        return 0
    end = text.find("</bookmark>", at)
    if end == -1:
        print(f"{places}: the entry for {path} does not end, not removed", file=sys.stderr)
        return 1
    start = text.rfind("\n", 0, at) + 1
    end = text.find("\n", end)
    end = len(text) if end == -1 else end + 1
    write(text[:start] + text[end:])
    return 0


def main(args):
    if len(args) == 4 and args[0] == "add":
        return add(args[1], args[2], args[3])
    if len(args) == 2 and args[0] == "remove":
        return remove(args[1])
    print(USAGE, file=sys.stderr)
    return 2


raise SystemExit(main(sys.argv[1:]))
