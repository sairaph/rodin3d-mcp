package render3d

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // card textures
	_ "image/png"
	"math"

	_ "golang.org/x/image/webp" // card textures may be webp
)

// Texture is an 8-bit RGBA image sampled bilinearly with repeat wrapping.
// SRGB textures are decoded to linear on sampling (three.js colorSpace srgb);
// others are used as stored (NoColorSpace).
type Texture struct {
	W, H int
	Pix  []uint8
	SRGB bool
}

var srgbLUT, linLUT [256]float32

func init() {
	for i := range srgbLUT {
		c := float64(i) / 255
		srgbLUT[i] = float32(srgbToLinear(c))
		linLUT[i] = float32(c)
	}
}

func srgbToLinear(c float64) float64 {
	if c < 0.04045 {
		return c * 0.0773993808
	}
	return math.Pow(c*0.9478672986+0.0521327014, 2.4)
}

func linearToSRGB(c float64) float64 {
	if c < 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 0.41666) - 0.055
}

// maxTexturePixels bounds a decoded texture (8192 x 8192, 256 MiB as RGBA).
const maxTexturePixels = 8192 * 8192

// DecodeTexture decodes a PNG, JPEG or WebP image. srgb marks colour data
// (sampled as linear after the sRGB decode); other maps are used as stored.
func DecodeTexture(b []byte, srgb bool) (*Texture, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxTexturePixels {
		return nil, fmt.Errorf("render3d: texture of %dx%d pixels is too large", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r := img.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, r.Min, draw.Src)
	return &Texture{W: r.Dx(), H: r.Dy(), Pix: rgba.Pix, SRGB: srgb}, nil
}

// Sample returns linear RGBA at uv (glTF convention: v down from the top, the
// way three.js loads glTF textures with flipY false).
func (t *Texture) Sample(u, v float64) [4]float32 {
	lut := &linLUT
	if t.SRGB {
		lut = &srgbLUT
	}
	x := u*float64(t.W) - 0.5
	y := v*float64(t.H) - 0.5
	fx, fy := math.Floor(x), math.Floor(y)
	ax, ay := float32(x-fx), float32(y-fy)
	x0, y0 := wrap(int(fx), t.W), wrap(int(fy), t.H)
	x1, y1 := wrap(int(fx)+1, t.W), wrap(int(fy)+1, t.H)
	var out [4]float32
	o00, o10 := (y0*t.W+x0)*4, (y0*t.W+x1)*4
	o01, o11 := (y1*t.W+x0)*4, (y1*t.W+x1)*4
	for c := 0; c < 4; c++ {
		l := lut
		if c == 3 {
			l = &linLUT
		}
		a := l[t.Pix[o00+c]]*(1-ax) + l[t.Pix[o10+c]]*ax
		b := l[t.Pix[o01+c]]*(1-ax) + l[t.Pix[o11+c]]*ax
		out[c] = a*(1-ay) + b*ay
	}
	return out
}

func wrap(i, n int) int {
	i %= n
	if i < 0 {
		i += n
	}
	return i
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
