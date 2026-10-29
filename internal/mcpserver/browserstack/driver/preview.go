package driver

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// previewQuality is the JPEG quality of a preview.
const previewQuality = 80

// Preview turns a screenshot PNG into a JPEG no wider or taller than maxDim
// (aspect kept, area averaged), small enough to show it to a model inline.
func Preview(data []byte, maxDim int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := w, h
	if maxDim > 0 && (w > maxDim || h > maxDim) {
		if w >= h {
			tw, th = maxDim, max(1, (h*maxDim+w/2)/w)
		} else {
			tw, th = max(1, (w*maxDim+h/2)/h), maxDim
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		y0, y1 := y*h/th, max((y+1)*h/th, y*h/th+1)
		for x := 0; x < tw; x++ {
			x0, x1 := x*w/tw, max((x+1)*w/tw, x*w/tw+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb := opaqueAt(src, b.Min.X+sx, b.Min.Y+sy)
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					n++
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: previewQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// opaqueAt is the pixel's colour over white.
func opaqueAt(img image.Image, x, y int) (r, g, b uint8) {
	c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if c.A == 255 {
		return c.R, c.G, c.B
	}
	blend := func(v uint8) uint8 { return uint8((uint32(v)*uint32(c.A) + 255*(255-uint32(c.A))) / 255) }
	return blend(c.R), blend(c.G), blend(c.B)
}
