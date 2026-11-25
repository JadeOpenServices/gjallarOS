"""Binary cache server for tests/nix/nix-download-retry-fix.nix.

nix-download-retry-cache.py ROOT PORT CUT

Serves the files under ROOT on 127.0.0.1:PORT with Range support, like the
release cache host. Every .nar reply sends at most CUT bytes and then closes
the connection, the way a reset stream ends a download. Other files are
served whole, so Nix always gets the narinfo.
"""
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT, PORT, CUT = os.path.realpath(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3])
RANGE = re.compile(r"bytes=(\d+)-")


class Handler(BaseHTTPRequestHandler):
    def reply(self, with_body):
        path = os.path.realpath(os.path.join(ROOT, self.path.lstrip("/")))
        if not path.startswith(ROOT + "/") or not os.path.isfile(path):
            self.send_error(404)
            return
        size = os.path.getsize(path)
        m = RANGE.fullmatch(self.headers.get("Range", ""))
        start = int(m.group(1)) if m else 0
        if start >= size:
            self.send_error(416)
            return
        self.send_response(206 if m else 200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(size - start))
        if m:
            self.send_header("Content-Range", f"bytes {start}-{size - 1}/{size}")
        self.end_headers()
        self.close_connection = True
        if not with_body:
            return
        limit = size - start
        if path.endswith(".nar"):
            limit = min(limit, CUT)
        with open(path, "rb") as f:
            f.seek(start)
            self.wfile.write(f.read(limit))
        self.wfile.flush()

    def do_GET(self):
        self.reply(True)

    def do_HEAD(self):
        self.reply(False)


HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
