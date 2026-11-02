//go:build linux || (darwin && cgo)

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

// icon draws a 32×32 PNG square: violet, or red when the server is down.
func icon(down bool) []byte {
	const n = 32
	fill := color.RGBA{0x6b, 0x2f, 0xa3, 0xff}
	if down {
		fill = color.RGBA{0xc8, 0x10, 0x2e, 0xff}
	}
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if x < 3 || y < 3 || x >= n-3 || y >= n-3 {
				img.Set(x, y, color.RGBA{0xe9, 0xe4, 0xee, 0xff})
			} else {
				img.Set(x, y, fill)
			}
		}
	}
	var out bytes.Buffer
	png.Encode(&out, img)
	return out.Bytes()
}

// mini opens the small window in the browser: a system WebView needs a
// cgo build with GTK or WebKit, the browser is always there.
type mini struct{}

func (mini) open(g *gate.Gate) { g.OpenPage("/mini") }
func (mini) close()            {}
