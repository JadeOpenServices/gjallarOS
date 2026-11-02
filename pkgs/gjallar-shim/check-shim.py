"""Build-time check of the pinned shim (Plane 293, wiki 951).

The fixed-output hash pins the files. This check makes sure a new pin is
the right kind of file before it can land:

  shimx64.efi  one signature under each CA_SHA256 given, and no other
               signature; each one verified by sbverify
  vendor cert  the cert built into the shim has VENDOR_SHA256
  mmx64.efi    (and fbx64.efi) one signature, under that vendor cert

Usage: check-shim.py WORKDIR SHIM MM FB VENDOR_SHA256 CA_SHA256...
"""

import base64
import hashlib
import os
import re
import struct
import subprocess
import sys


def pe_parts(data):
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe : pe + 4] != b"PE\0\0":
        raise SystemExit("not a PE file")
    nsect = struct.unpack_from("<H", data, pe + 6)[0]
    optsize = struct.unpack_from("<H", data, pe + 20)[0]
    symptr, nsym = struct.unpack_from("<II", data, pe + 12)
    opt = pe + 24
    magic = struct.unpack_from("<H", data, opt)[0]
    datadir = opt + (112 if magic == 0x20B else 96)
    secdir = datadir + 4 * 8
    sections = {}
    table = opt + optsize
    for i in range(nsect):
        raw = data[table + 40 * i : table + 40 * i + 40]
        name = raw[:8].rstrip(b"\0").decode()
        vsize, _, rsize, roff = struct.unpack_from("<IIII", raw, 8)
        if name.startswith("/") and symptr:
            at = symptr + 18 * nsym + int(name[1:])
            name = data[at : data.index(b"\0", at)].decode()
        sections[name] = data[roff : roff + min(vsize, rsize)]
    return secdir, sections


def signatures(data):
    """Each WIN_CERTIFICATE entry as a copy of the file holding only it."""
    secdir, _ = pe_parts(data)
    off, size = struct.unpack_from("<II", data, secdir)
    if off == 0 or off + size != len(data):
        raise SystemExit("no signature table at the end of the file")
    copies, at = [], off
    while at < off + size:
        length, rev, kind = struct.unpack_from("<IHH", data, at)
        if rev != 0x200 or kind != 2 or length < 8:
            raise SystemExit("unexpected signature entry")
        entry = data[at : at + length]
        entry += b"\0" * (-len(entry) % 8)
        copy = bytearray(data[:off] + entry)
        struct.pack_into("<II", copy, secdir, off, len(entry))
        copies.append((bytes(copy), data[at + 8 : at + length]))
        at += (length + 7) & ~7
    return copies


def certs(pkcs7, work):
    """SHA-256 fingerprint to PEM, for every cert in a PKCS#7 blob."""
    der = os.path.join(work, "sig.der")
    with open(der, "wb") as f:
        f.write(pkcs7)
    out = subprocess.run(
        ["openssl", "pkcs7", "-inform", "der", "-in", der, "-print_certs"],
        check=True, capture_output=True, text=True,
    ).stdout
    found = {}
    for pem in re.findall(r"-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----", out, re.S):
        body = "".join(pem.splitlines()[1:-1])
        found[hashlib.sha256(base64.b64decode(body)).hexdigest()] = pem + "\n"
    return found


def verify(copy, ca_pem, work, label):
    efi = os.path.join(work, "one.efi")
    ca = os.path.join(work, "ca.pem")
    with open(efi, "wb") as f:
        f.write(copy)
    with open(ca, "w") as f:
        f.write(ca_pem)
    r = subprocess.run(["sbverify", "--cert", ca, efi], capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"{label}: signature does not verify\n{r.stdout}{r.stderr}")


def main():
    work, shim, mm, fb, vendor_fp, *ca_fps = sys.argv[1:]
    data = open(shim, "rb").read()

    seen = set()
    for copy, blob in signatures(data):
        chain = certs(blob, work)
        cas = [fp for fp in ca_fps if fp in chain]
        if len(cas) != 1:
            raise SystemExit("shim: a signature not under exactly one expected CA")
        verify(copy, chain[cas[0]], work, "shim")
        seen.add(cas[0])
    if seen != set(ca_fps):
        raise SystemExit(f"shim: signed under {len(seen)} of {len(ca_fps)} expected CAs")

    _, sections = pe_parts(data)
    table = sections.get(".vendor_cert")
    if table is None:
        raise SystemExit("shim: no vendor cert")
    auth_size, _, auth_off, _ = struct.unpack_from("<IIII", table, 0)
    vendor = table[auth_off : auth_off + auth_size]
    if hashlib.sha256(vendor).hexdigest() != vendor_fp:
        raise SystemExit("shim: vendor cert is not the expected one")
    vendor_pem = subprocess.run(
        ["openssl", "x509", "-inform", "der"], input=vendor, check=True, capture_output=True,
    ).stdout.decode()

    for path in (mm, fb):
        sigs = signatures(open(path, "rb").read())
        if len(sigs) != 1:
            raise SystemExit(f"{os.path.basename(path)}: expected one signature")
        verify(sigs[0][0], vendor_pem, work, os.path.basename(path))


main()
