#!/usr/bin/env python3
"""Generate the placeholder icon set for cix.app.

These are placeholders. The three files this writes into mac/Resources/ are
committed, so neither CI nor a normal build ever runs this script — replacing
the artwork means dropping in new files with the same names and sizes, with no
code change anywhere.

    mac/Resources/menubar.png      18x18  template image (black + alpha only)
    mac/Resources/menubar@2x.png   36x36  template image
    mac/Resources/AppIcon.icns            full icns, built via iconutil

"Template image" is a hard requirement for the menu bar, not a style choice:
macOS recolours template images to match the menu bar (dark mode, tinting,
inactive state) and only looks at the alpha channel. A coloured PNG there
renders as a solid smudge. So the menu-bar glyphs below write pure black
pixels and vary only alpha.

Pure stdlib (zlib + struct) — deliberately no Pillow, so the script runs on a
clean machine. iconutil ships with macOS.
"""

from __future__ import annotations

import pathlib
import shutil
import struct
import subprocess
import sys
import tempfile
import zlib

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
RESOURCES = REPO_ROOT / "mac" / "Resources"

# Slate background for the app icon; the glyph is white on top of it.
BG = (30, 41, 59)
FG = (241, 245, 249)

# Sizes iconutil expects in an .iconset directory. Anything missing is simply
# absent from the icns, which macOS then scales badly — so emit the full set.
ICONSET = [
    ("icon_16x16.png", 16),
    ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32),
    ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128),
    ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256),
    ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512),
    ("icon_512x512@2x.png", 1024),
]


def write_png(path: pathlib.Path, width: int, height: int, pixels: bytearray) -> None:
    """Write an 8-bit RGBA PNG. `pixels` is width*height*4 bytes, row-major."""

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + tag
            + data
            + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
        )

    # Filter byte 0 (None) per scanline — no filtering, smallest possible code.
    raw = b"".join(
        b"\x00" + bytes(pixels[y * width * 4 : (y + 1) * width * 4])
        for y in range(height)
    )
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    path.write_bytes(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", ihdr)
        + chunk(b"IDAT", zlib.compress(raw, 9))
        + chunk(b"IEND", b"")
    )


def blend(pixels: bytearray, width: int, x: int, y: int, rgb, alpha: float) -> None:
    """Source-over one pixel. Callers pass fractional alpha for antialiasing."""
    if alpha <= 0:
        return
    alpha = min(alpha, 1.0)
    i = (y * width + x) * 4
    dst_a = pixels[i + 3] / 255.0
    out_a = alpha + dst_a * (1 - alpha)
    if out_a <= 0:
        return
    for c in range(3):
        src = rgb[c] / 255.0
        dst = pixels[i + c] / 255.0
        pixels[i + c] = int(round((src * alpha + dst * dst_a * (1 - alpha)) / out_a * 255))
    pixels[i + 3] = int(round(out_a * 255))


def rounded_rect(pixels, width, x0, y0, w, h, radius, rgb, samples=4):
    """Draw an antialiased rounded rectangle by supersampling coverage."""
    step = 1.0 / samples
    for py in range(int(y0), int(y0 + h) + 1):
        if py < 0 or py >= width:
            continue
        for px in range(int(x0), int(x0 + w) + 1):
            if px < 0 or px >= width:
                continue
            hits = 0
            for sy in range(samples):
                for sx in range(samples):
                    fx = px + (sx + 0.5) * step
                    fy = py + (sy + 0.5) * step
                    if inside_rounded(fx, fy, x0, y0, w, h, radius):
                        hits += 1
            if hits:
                blend(pixels, width, px, py, rgb, hits / (samples * samples))


def inside_rounded(fx, fy, x0, y0, w, h, r) -> bool:
    if fx < x0 or fx > x0 + w or fy < y0 or fy > y0 + h:
        return False
    # Clamp the sample into the inner rectangle; the leftover offset is the
    # distance to the nearest corner arc centre.
    cx = min(max(fx, x0 + r), x0 + w - r)
    cy = min(max(fy, y0 + r), y0 + h - r)
    dx, dy = fx - cx, fy - cy
    return dx * dx + dy * dy <= r * r


def draw_glyph(pixels, size, rgb, scale=1.0, offset=(0.0, 0.0)):
    """Three left-aligned bars — a stylised index.

    Coordinates are expressed on a 100x100 design grid and mapped onto `size`,
    so every raster below is the same drawing rather than three lookalikes.
    """
    unit = size / 100.0 * scale
    ox = offset[0] * size / 100.0
    oy = offset[1] * size / 100.0
    bar_h = 14.0
    radius = bar_h / 2.0
    for i, (top, bar_w) in enumerate(((16.0, 68.0), (43.0, 44.0), (70.0, 68.0))):
        rounded_rect(
            pixels,
            size,
            16.0 * unit + ox,
            top * unit + oy,
            bar_w * unit,
            bar_h * unit,
            radius * unit,
            rgb,
        )


def make_menubar(size: int) -> bytearray:
    pixels = bytearray(size * size * 4)
    # Template image: black glyph, transparent elsewhere. macOS reads alpha only.
    draw_glyph(pixels, size, (0, 0, 0))
    return pixels


def make_app_icon(size: int) -> bytearray:
    pixels = bytearray(size * size * 4)
    # macOS icon grid: the artwork occupies ~80% of the canvas, with the
    # squircle corner radius at ~22.4% of the artwork's edge.
    inset = size * 0.10
    art = size - inset * 2
    rounded_rect(pixels, size, inset, inset, art, art, art * 0.2237, BG)
    draw_glyph(pixels, size, FG, scale=0.80, offset=(10.0, 10.0))
    return pixels


def main() -> int:
    if shutil.which("iconutil") is None:
        print("make-placeholder-icons: iconutil not found — run this on macOS", file=sys.stderr)
        return 1

    RESOURCES.mkdir(parents=True, exist_ok=True)

    for name, size in (("menubar.png", 18), ("menubar@2x.png", 36)):
        write_png(RESOURCES / name, size, size, make_menubar(size))
        print(f"wrote mac/Resources/{name} ({size}x{size})")

    with tempfile.TemporaryDirectory() as tmp:
        iconset = pathlib.Path(tmp) / "AppIcon.iconset"
        iconset.mkdir()
        # Cache by pixel size: the iconset asks for several sizes twice.
        rendered: dict[int, bytearray] = {}
        for name, size in ICONSET:
            if size not in rendered:
                rendered[size] = make_app_icon(size)
            write_png(iconset / name, size, size, rendered[size])
        subprocess.run(
            ["iconutil", "-c", "icns", str(iconset), "-o", str(RESOURCES / "AppIcon.icns")],
            check=True,
        )
    print("wrote mac/Resources/AppIcon.icns")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
