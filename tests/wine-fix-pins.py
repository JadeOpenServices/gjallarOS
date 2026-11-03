#!/usr/bin/env python3
"""Check that every download in the Wine fix table still has its pinned size
and sha256.

    python3 -I tests/wine-fix-pins.py internal/winefix/fixes.json

Each file is streamed and hashed in memory, never saved and never run. A
redirect must stay on https. A download stops at its pinned size plus one
byte, so a changed file cannot fill the runner. Exit 1 lists every file
that changed or is gone; the table row then needs a new pin or has to go.
"""
import hashlib
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

CHUNK = 1 << 16


class HTTPSOnly(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlsplit(newurl).scheme != "https":
            raise urllib.error.HTTPError(newurl, code, "redirect leaves https", headers, fp)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def check(opener, f):
    h = hashlib.sha256()
    n = 0
    with opener.open(f["url"], timeout=60) as r:
        while True:
            b = r.read(min(CHUNK, f["size"] + 1 - n))
            if not b:
                break
            n += len(b)
            h.update(b)
            if n > f["size"]:
                return "larger than the pinned %d bytes" % f["size"]
    if n != f["size"]:
        return "%d bytes, pinned %d" % (n, f["size"])
    if h.hexdigest() != f["sha256"]:
        return "sha256 %s, pinned %s" % (h.hexdigest(), f["sha256"])
    return None


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: wine-fix-pins.py fixes.json")
    with open(sys.argv[1], encoding="utf-8") as fh:
        table = json.load(fh)
    opener = urllib.request.build_opener(HTTPSOnly)
    bad = 0
    for fix in table["fixes"]:
        for f in fix.get("files", []):
            if urllib.parse.urlsplit(f["url"]).scheme != "https":
                problem = "not https"
            else:
                try:
                    problem = check(opener, f)
                except (urllib.error.URLError, OSError) as e:
                    problem = "download failed: %s" % e
            print("%-10s %-40s %s" % (fix["id"], f["url"].rsplit("/", 1)[-1], problem or "ok"))
            bad += problem is not None
    if bad:
        sys.exit("%d download(s) no longer match the table" % bad)


main()
