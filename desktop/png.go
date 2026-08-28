package main

import (
	"image"
	"image/png"
	"io"
)

func pngEncode(w io.Writer, img image.Image) error {
	return png.Encode(w, img)
}
