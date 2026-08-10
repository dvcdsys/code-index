# cix · CodeIndeX — macOS icon set

Icon assets for the cix desktop app, generated from the design source
`CodeIndeX Icons.dc.html`. Brand palette (from the CIX logo): cream `#F1E6CF` /
`#F7EEDC`, red `#D73E2C`, dark red `#7A2A1E`, ink `#1F140F`.

The mark is a blocky, pixel-grid magnifier — same block-and-trace construction
as the CIX logotype. It simplifies as it gets smaller: at 128 px+ it carries the
offset "trace" outline and three code lines, at 64 px the outline drops, at
32/16 px only the ring and handle remain.

> Two adaptations were made when importing this set into the repo. The export
> pipeline could not write `@` into filenames, so retina files arrived as
> `-2x.png`; they have been renamed to `@2x.png`, which is what `iconutil`
> requires. And the menu-bar wiring below was written for AppKit/Swift; this app
> is Go, so the equivalent is `systray.SetTemplateIcon(png, png)` — the
> "must be a template image" rule is unchanged and just as load-bearing.

Nothing here is used at build time by name-lookup magic: `mac/scripts/build-app.sh`
and `mac/scripts/make-dmg.sh` name these paths explicitly, so replacing the
artwork is a matter of dropping in files with the same names.

---

## 1. `cix.iconset/` — application icon

The app's own icon: cream background, red magnifier. This is what the user sees
in Finder, in Get Info, and on the mounted disk image.

| file | px | notes |
|---|---|---|
| `icon_512x512@2x.png` | 1024 | full detail: trace outline + 3 code lines |
| `icon_512x512.png`, `icon_256x256@2x.png` | 512 | full detail |
| `icon_256x256.png`, `icon_128x128@2x.png` | 256 | full detail |
| `icon_128x128.png` | 128 | full detail |
| `icon_32x32@2x.png` | 64 | outline dropped, 2 code lines |
| `icon_32x32.png`, `icon_16x16@2x.png` | 32 | ring + handle only, pixel-snapped |
| `icon_16x16.png` | 16 | ring + handle only, pixel-snapped |

`build-app.sh` runs `iconutil -c icns` on this directory and installs the result
as `cix.app/Contents/Resources/cix.icns`, matching
`CFBundleIconFile` = `cix` in `mac/Info.plist.in`.

Do **not** add your own rounded corners, shadow, or padding — the squircle, the
1-px ink border and the safe margin are already baked in at every size.

## 2. `cix-installer.iconset/` — installer / disk-image icon

Inverted colourway: red field, cream magnifier, blocky `+` in the bottom-right
corner. In Finder and in Downloads the installer must never be mistaken for the
app itself, so it is a colour inversion rather than a different drawing.

`make-dmg.sh` converts it and installs it as the volume icon
(`.VolumeIcon.icns` plus the Finder custom-icon flag).

## 3. `menubar/` — menu bar (status bar) icon

Monochrome **template** images for the status item — the icon that sits in the
top bar next to the clock. Pure black + alpha; macOS itself inverts it for dark
menu bars and tints it while the menu is open.

| file | use |
|---|---|
| `cixTemplate-18.png` | @1x — the standard 18×18 status-item glyph |
| `cixTemplate-36.png` | @2x for the 18 px glyph |
| `cixTemplate-44.png` | @2x for a 22 px glyph (roomier bar layouts) |
| `cixTemplate-88.png` | @4x / source for tracing a vector template |

The 18 and 36 px files are copied into the bundle; 44 and 88 are kept here as
part of the source set.

Never recolour the glyph in code, and never use the red app icon in the menu
bar — at 18 px colour turns to mush and breaks dark mode.

## 4. `dmg/` — disk image window background

`dmg-background.png` (640×420) and `@2x` (1280×840): cream sheet, red rule at
the top, CIX wordmark, and a blocky arrow pointing right — from `cix.app`
toward `Applications`. Icon placeholders are intentionally **not** drawn;
Finder puts the real icons on top.

The layout the arrow points at, implemented in `make-dmg.sh`:

- window content size 640×420
- `cix.app` centre at **(175, 250)**
- `Applications` symlink centre at **(465, 250)**
- icon size 104, arranged by position, toolbar/sidebar/status bar hidden

## 5. `web/` — not imported

The archive also contains favicons and an animated installer icon for the
download page. Those belong to `site/`, not to the app bundle, and were left out
of this directory so that everything here is something the build actually
consumes.

---

## Rules of use

1. **Cream = the app, red = the installer.** Never swap them; that distinction
   is the only thing separating the two files in a Downloads folder.
2. **Menu bar is always the black template**, never the coloured icon.
3. Keep the pixel grid: scale only by whole factors (×2, ×4). Smooth-scaling the
   small sizes destroys the blocky construction.
4. Nothing here needs an extra shadow, gradient, gloss, or corner mask.
5. Need a size that is not here? Regenerate from the design source rather than
   upscaling a PNG.
