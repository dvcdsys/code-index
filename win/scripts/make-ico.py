#!/usr/bin/env python3
"""make-ico.py — pack the app's iconset PNGs into a multi-size Windows .ico.

Windows reads PNG-compressed images inside an .ico (Vista and later), so the
existing artwork is embedded as is. Standard library only; no ImageMagick.

    python3 win/scripts/make-ico.py  mac/Resources/cix.iconset  win/Resources/cix.ico

The PNG iconset stays the single source of truth for the artwork (see
mac/Resources/README.md); the .ico is derived from it and committed so the
Windows build needs no extra tooling.
"""
import struct
import sys
from pathlib import Path

# (pixel size, iconset file that has exactly that many pixels)
SIZES = [
    (16, "icon_16x16.png"),
    (32, "icon_32x32.png"),
    (64, "icon_32x32@2x.png"),
    (128, "icon_128x128.png"),
    (256, "icon_256x256.png"),
]


def main(src: str, dst: str) -> None:
    images = []
    for px, name in SIZES:
        data = (Path(src) / name).read_bytes()
        w, h = struct.unpack(">II", data[16:24])  # IHDR width/height
        if (w, h) != (px, px):
            sys.exit(f"{name} is {w}x{h}, expected {px}x{px}")
        images.append((px, data))

    out = bytearray(struct.pack("<HHH", 0, 1, len(images)))
    offset = 6 + 16 * len(images)
    for px, data in images:
        # width/height byte 0 means 256
        out += struct.pack("<BBBBHHII", px % 256, px % 256, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    for _, data in images:
        out += data
    Path(dst).write_bytes(out)
    print(f"wrote {dst} ({len(images)} sizes, {len(out)} bytes)")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
