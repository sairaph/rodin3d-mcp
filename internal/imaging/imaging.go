// Package imaging turns server images (webp, jpeg, png) into PNGs of a
// chosen size, in pure Go.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Decode reads a webp, jpeg or png image.
func Decode(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	switch {
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webp.Decode(bytes.NewReader(data))
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return jpeg.Decode(bytes.NewReader(data))
	case len(data) >= 8 && string(data[1:4]) == "PNG":
		return png.Decode(bytes.NewReader(data))
	}
	return nil, fmt.Errorf("not a webp, jpeg or png image")
}

// Sniff returns the MIME type of a webp, jpeg or png image by its first
// bytes, or "" for anything else.
func Sniff(data []byte) string {
	switch {
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && string(data[1:4]) == "PNG":
		return "image/png"
	}
	return ""
}

// Size reads the width and height of a webp, jpeg or png image without
// decoding its pixels.
func Size(data []byte) (int, int, error) {
	var cfg image.Config
	var err error
	switch Sniff(data) {
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	default:
		return 0, 0, fmt.Errorf("not a webp, jpeg or png image")
	}
	return cfg.Width, cfg.Height, err
}

// Fit scales img so its longer edge is at most edge pixels (never up).
func Fit(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if edge <= 0 || (w <= edge && h <= edge) {
		return img
	}
	nw, nh := edge, edge
	if w >= h {
		nh = max(h*edge/w, 1)
	} else {
		nw = max(w*edge/h, 1)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// PNG encodes img.
func PNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ToPNG decodes data and returns it as a PNG whose longer edge is at most
// edge pixels, with its size.
func ToPNG(data []byte, edge int) ([]byte, int, int, error) {
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	img = Fit(img, edge)
	out, err := PNG(img)
	b := img.Bounds()
	return out, b.Dx(), b.Dy(), err
}
