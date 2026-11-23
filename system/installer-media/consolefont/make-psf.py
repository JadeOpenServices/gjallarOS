#!/usr/bin/env python3
"""Jade Code as a Linux console font (PSF2 with a Unicode table).

From THEMEBRAIN section 18 (code/terminal/consolefont/make_psf.py there);
here the font file is an argument. The console takes only bitmap fonts.
This turns Jade Code (weight 400) into PSF2 files, one per cell width.
Jade Code's cell is exactly 2:1 (advance 1200, ascent 1800, descent 600,
at 1950 units per em), so a font W wide is 2W high; check_cmap stops the
build when the font says otherwise.

Usage: make-psf.py FONT OUTDIR --widths 14,22 [--threshold N]
The widths come from stick.nix (consoleWidths).
Writes OUTDIR/JadeCode-<W>x<2W>.psfu. Same input gives the same bytes.
The stick picks the width (gjallar-stick font --mode console).
Kernel 6.18 takes up to 64 by 128 (drivers/tty/vt/vt.c), but the
framebuffer console takes only fonts of exactly 256 or 512 glyphs and
answers anything else with EINVAL (fbcon_set_font in
drivers/video/fbdev/core/fbcon.c). It also clears cells with glyph 32, so
that one must be the space. Each file therefore has 512 glyphs with ASCII
and Latin-1 at their own code points (see layout).
"""
import argparse
import os
import struct
import sys

from PIL import Image, ImageDraw, ImageFont

UPM, ADV, ASC = 1950, 1200, 1800

# Jade Code has no U+2010 hyphen and no U+2717 ballot x; the hyphen is an
# alias of "-" below, the x is left out. check_cmap stops the build if a
# listed character has no glyph, so none turns into the box by accident.
# The font has ASCII, Latin-1, the punctuation German and English text
# uses, arrows, box drawing and block elements, and the replacement glyph.
EXTRA = "–—‘’‚“”„•…‹›€™←↑→↓↔✓·"
GLYPHS = 512


def layout():
    """The character in each of the GLYPHS slots, None for an empty one.
    ASCII and Latin-1 sit at their own code points, so slot 32 is the
    space; the replacement takes slot 0 and the rest fill the free slots
    in order."""
    slots = [None] * GLYPHS
    for c in list(range(0x20, 0x7F)) + list(range(0xA0, 0x100)):
        slots[c] = chr(c)
    rest = ["�"] + list(EXTRA) + [chr(c) for c in range(0x2500, 0x25A0)]
    free = [i for i, ch in enumerate(slots) if ch is None]
    assert len(rest) <= len(free), (len(rest), len(free))
    for i, ch in zip(free, rest):
        slots[i] = ch
    return slots


CHARS = layout()
# Same shape, other code points: the console maps these to one glyph.
ALIAS = {"-": ["−", "‐"], "�": []}


def check_cmap(path):
    from fontTools.ttLib import TTFont
    tt = TTFont(path)
    got = (tt["head"].unitsPerEm, tt["hmtx"]["zero"][0], tt["hhea"].ascent, -tt["hhea"].descent)
    assert got == (UPM, ADV, ASC, 2 * ADV - ASC), f"{path}: metrics {got}"
    cmap = tt.getBestCmap()
    missing = [f"U+{ord(c):04X}" for c in CHARS if c not in (None, "�") and ord(c) not in cmap]
    assert not missing, f"no glyph in {path}: {missing}"


def render(path, w, threshold):
    h = 2 * w
    font = ImageFont.truetype(path, size=w * UPM / ADV, layout_engine=ImageFont.Layout.BASIC)
    font.set_variation_by_axes([400])
    base = w * ASC / ADV
    glyphs = []
    for ch in CHARS:
        img = Image.new("L", (w, h), 0)
        d = ImageDraw.Draw(img)
        if ch == "�":
            d.rectangle([1, 1, w - 2, h - 2], outline=255)
        elif ch is not None:
            d.text((0, base), ch, font=font, fill=255, anchor="ls")
        rows = []
        stride = (w + 7) // 8
        px = img.load()
        for y in range(h):
            row = bytearray(stride)
            for x in range(w):
                if px[x, y] >= threshold:
                    row[x // 8] |= 0x80 >> (x % 8)
            rows.append(bytes(row))
        glyphs.append(b"".join(rows))
    return glyphs


def psf2(w, glyphs):
    h = 2 * w
    size = ((w + 7) // 8) * h
    head = struct.pack("<IIIIIIII", 0x864AB572, 0, 32, 0x01, len(glyphs), size, h, w)
    assert len(glyphs) == GLYPHS and not any(glyphs[32]), "glyph 32 must be the empty space"
    table = b""
    for ch in CHARS:
        cps = [] if ch is None else [ch] + ALIAS.get(ch, [])
        table += b"".join(c.encode("utf-8") for c in cps) + b"\xff"
    return head + b"".join(glyphs) + table


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("font")
    ap.add_argument("outdir")
    ap.add_argument("--threshold", type=int, default=112, help="grey level 0-255 that counts as ink")
    ap.add_argument("--widths", required=True, help="cell widths in pixels, such as 14,22")
    a = ap.parse_args()
    assert len(CHARS) == GLYPHS and CHARS[32] == " ", "fbcon needs 256 or 512 glyphs, space at 32"
    check_cmap(a.font)
    os.makedirs(a.outdir, exist_ok=True)
    for w in [int(x) for x in a.widths.split(",")]:
        data = psf2(w, render(a.font, w, a.threshold))
        out = os.path.join(a.outdir, f"JadeCode-{w}x{2 * w}.psfu")
        with open(out, "wb") as f:
            f.write(data)
        print(out, len(data), file=sys.stderr)


if __name__ == "__main__":
    main()
