package driver

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

type point struct{ X, Y int }

// markers are the two calibration markers (browser/marker.js) found in a
// screenshot: their top-left corners in image pixels.
type markers struct {
	ImageWidth    int
	First, Second point
}

func isMagenta(r, g, b uint8) bool { return r > 220 && g < 60 && b > 220 }

// findMarkers finds the two calibration markers in a PNG screenshot; nil when
// the screen does not show them.
func findMarkers(data []byte) (*markers, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	var blobs []point
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < width; x++ {
			r, g, b := rgbAt(img, bounds.Min.X+x, bounds.Min.Y+y)
			if !isMagenta(r, g, b) {
				continue
			}
			near := false
			for _, blob := range blobs {
				if abs(blob.X-x) < 40 && abs(blob.Y-y) < 40 {
					near = true
					break
				}
			}
			if !near {
				blobs = append(blobs, point{x, y})
			}
		}
	}
	// Magenta in the page itself (an ad) must not fool it: the markers are
	// the pair as far apart on both axes.
	var best *markers
	for _, first := range blobs {
		for _, second := range blobs {
			dx := second.X - first.X
			dy := second.Y - first.Y
			if dx > 20 && float64(abs(dx-dy)) <= max(3, float64(dx)*0.04) && (best == nil || first.X+first.Y < best.First.X+best.First.Y) {
				best = &markers{ImageWidth: width, First: first, Second: second}
			}
		}
	}
	return best, nil
}

// rgbAt is the pixel's colour without alpha, fast for the usual PNG layouts.
func rgbAt(img image.Image, x, y int) (r, g, b uint8) {
	switch m := img.(type) {
	case *image.NRGBA:
		i := m.PixOffset(x, y)
		return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
	case *image.RGBA:
		i := m.PixOffset(x, y)
		return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
	}
	c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	return c.R, c.G, c.B
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
