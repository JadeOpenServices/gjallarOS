"""Build-time check of the pinned shim (Plane 293, wiki 951).

The fixed-output hash pins the files. This check makes sure a new pin is
the right kind of file before it can land:

  shimx64.efi  one signature under each CA_SHA256 given, and no other
               signature; each one verified by sbverify
  vendor cert  the cert built into the shim has VENDOR_SHA256
  mmx64.efi    (and fbx64.efi) one signature, under that vendor cert
  hashes       the Authenticode SHA-256 of each file is the digest its
               signature carries

It then writes FACTS (JSON): the signer CAs; every cert in the shim's
signature chains and the vendor cert, each by SHA-256 and by the SHA-256
of its signed part (the two forms a dbx entry names a cert by); each file's
Authenticode SHA-256 (the form a dbx entry names a file by) and the shim's
SBAT lines. gjallarctl reads it to check the firmware's db and dbx before
it picks the shim path.

Usage: check-shim.py WORKDIR FACTS SHIM MM FB VENDOR_SHA256 CA_SHA256...
"""

import base64
import hashlib
import json
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


def authenticode(data):
    """SHA-256 over the file minus the checksum, the security directory
    entry and the signature table (Microsoft PE Authenticode, with the
    sections laid out in file order, which the digest check below proves)."""
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    checksum = pe + 24 + 64
    secdir, _ = pe_parts(data)
    off, size = struct.unpack_from("<II", data, secdir)
    end = off if off else len(data)
    h = hashlib.sha256()
    h.update(data[:checksum])
    h.update(data[checksum + 4 : secdir])
    h.update(data[secdir + 8 : end])
    h.update(data[off + size :] if off else b"")
    return h.digest()


def tbs_sha256(der):
    """SHA-256 of a certificate's signed part, the form an X.509 SHA-256
    dbx entry names."""
    def header(at):
        length = der[at + 1]
        at += 2
        if length & 0x80:
            n = length & 0x7F
            length = int.from_bytes(der[at : at + n], "big")
            at += n
        return at, length

    if der[0] != 0x30:
        raise SystemExit("cert is not a DER sequence")
    inner, _ = header(0)
    if der[inner] != 0x30:
        raise SystemExit("cert has no signed part")
    body, length = header(inner)
    return hashlib.sha256(der[inner : body + length]).hexdigest()


def pem_der(pem):
    return base64.b64decode("".join(pem.strip().splitlines()[1:-1]))


def signed_digest(blob, digest, label):
    # The SpcIndirectDataContent in each signature holds the image digest
    # as a plain OCTET STRING: 04 20 <32 bytes>.
    if b"\x04\x20" + digest not in blob:
        raise SystemExit(f"{label}: Authenticode hash differs from the signed digest")


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
    work, facts, shim, mm, fb, vendor_fp, *ca_fps = sys.argv[1:]
    data = open(shim, "rb").read()
    hashes = {"shimx64.efi": authenticode(data)}

    seen, chain_certs = set(), {}
    for copy, blob in signatures(data):
        signed_digest(blob, hashes["shimx64.efi"], "shim")
        chain = certs(blob, work)
        chain_certs.update(chain)
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

    for path, name in ((mm, "mmx64.efi"), (fb, "fbx64.efi")):
        raw = open(path, "rb").read()
        sigs = signatures(raw)
        if len(sigs) != 1:
            raise SystemExit(f"{name}: expected one signature")
        hashes[name] = authenticode(raw)
        signed_digest(sigs[0][1], hashes[name], name)
        verify(sigs[0][0], vendor_pem, work, name)

    sbat = sections.get(".sbat")
    if sbat is None:
        raise SystemExit("shim: no SBAT section")
    lines = [l for l in sbat.rstrip(b"\0").decode().splitlines() if l]

    with open(facts, "w") as f:
        json.dump({
            "schema": 1,
            "signerCAs": sorted(ca_fps),
            "chainCerts": sorted(chain_certs),
            "chainTBS": sorted({tbs_sha256(pem_der(p)) for p in chain_certs.values()}),
            "vendorCA": vendor_fp,
            "vendorCATBS": tbs_sha256(vendor),
            "authenticode": {k: v.hex() for k, v in sorted(hashes.items())},
            "sbat": lines,
        }, f, indent=2, sort_keys=True)
        f.write("\n")


main()
