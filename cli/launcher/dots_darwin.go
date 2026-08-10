package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// Status dots for the menu rows.
//
// A coloured dot beside a row is how macOS status apps show liveness, and it
// carries the state without spending menu width on words like "— ready". That
// matters here: an NSMenu is as wide as its widest row, so every character in a
// status string is width the model name does not get.
//
// These are NOT template images. A template image is recoloured by macOS from
// its alpha channel alone, which would turn every dot the same colour and erase
// the only thing they say. The menu-bar glyph is a template image; these are
// not, deliberately.
//
// Generated rather than shipped as files: they are three flat circles, and a
// generator is smaller than the PNGs plus the build-script lines to copy them.

// Colours picked to stay legible on both light and dark menu backgrounds, and
// to survive the most common form of colour blindness by differing in lightness
// as well as hue — a green/red pair alone would not.
var (
	dotGreen = color.NRGBA{R: 0x30, G: 0xB0, B: 0x50, A: 0xFF}
	dotAmber = color.NRGBA{R: 0xE0, G: 0x94, B: 0x1B, A: 0xFF}
	dotRed   = color.NRGBA{R: 0xD7, G: 0x3E, B: 0x2C, A: 0xFF} // the CIX brand red
	dotGrey  = color.NRGBA{R: 0x8E, G: 0x8E, B: 0x93, A: 0xFF}

	// dotBlank is a fully transparent dot used purely as a spacer. AppKit
	// indents a menu item's title by its image width, so a row without an image
	// starts further left than its neighbours and the group reads as ragged.
	// An invisible image of the same size keeps the titles on one edge.
	dotBlank = color.NRGBA{}
)

// dotSize is the rendered pixel size. 24 px at @2x renders as a 12 pt dot,
// which is the size AppKit gives a menu item image next to body text.
const dotSize = 24

var (
	dotOnce  sync.Once
	dotCache map[color.NRGBA][]byte
)

// dotPNG returns a PNG of a filled circle in c, encoded once and reused.
func dotPNG(c color.NRGBA) []byte {
	dotOnce.Do(func() {
		dotCache = map[color.NRGBA][]byte{}
		for _, col := range []color.NRGBA{dotGreen, dotAmber, dotRed, dotGrey, dotBlank} {
			dotCache[col] = renderDot(col)
		}
	})
	if b, ok := dotCache[c]; ok {
		return b
	}
	return renderDot(c)
}

func renderDot(c color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, dotSize, dotSize))
	centre := float64(dotSize-1) / 2
	// Leave a pixel of margin so the antialiased edge is not clipped by the
	// image bounds, which reads as a flat-sided circle at this size.
	radius := centre - 1

	for y := range dotSize {
		for x := range dotSize {
			dx := float64(x) - centre
			dy := float64(y) - centre
			d := math.Hypot(dx, dy)
			// Coverage over a one-pixel band at the edge: cheap antialiasing,
			// and at 24 px the difference between this and a hard edge is the
			// difference between a circle and a cog.
			var alpha float64
			switch {
			case d <= radius-0.5:
				alpha = 1
			case d >= radius+0.5:
				alpha = 0
			default:
				alpha = radius + 0.5 - d
			}
			if alpha <= 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: c.R, G: c.G, B: c.B,
				A: uint8(math.Round(alpha * float64(c.A))),
			})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// Encoding a fixed-size in-memory NRGBA image cannot fail; a nil icon
		// simply leaves the menu row without one.
		return nil
	}
	return buf.Bytes()
}
