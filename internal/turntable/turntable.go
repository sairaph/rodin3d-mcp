// Package turntable reads the geometry draft picture of a Gen-1.5 task: not
// a mesh but the server's turntable sheet, a JPEG of 2 x 32 square tiles
// (11520 x 720, tiles of 360 px; seen on c6e419ac, gen-1.5 Fast,
// 2026-10-27). The web shows it as its draft carousel (cf/2094-2094.js
// 1936-2210): four looks of 16 frames each, a drag stepping 1/16 turn;
// look black is row 0 even tiles, red row 0 odd, grey row 1 even and
// colorful row 1 odd, so frame k of a look is tile 2k (+1) of its row.
// Frame 0 faces the front; frame k is k x 22.5 degrees round towards the
// model's right (+X): frame 4 shows the right side, 8 the back, 12 the
// left (read from the tiles). Every look turns the same way: frames 0, 2, 4
// and 8 of all four looks show the same side (live, c6e419ac, 2026-10-27),
// as the carousel moves the four looks by the same tile step together.
package turntable

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Looks of the sheet, in the web's order (its names: black, red, grey,
// colorful).
const (
	LookDark   = "dark"   // grey clay on a dark background (web black)
	LookCopper = "copper" // glossy copper on a terracotta background (web red)
	LookLight  = "light"  // grey clay with hard shadows on light blue (web grey)
	LookNormal = "normal" // surface normals as colours on light blue (web colorful)
)

// Looks lists the looks in the web's order.
var Looks = []string{LookDark, LookCopper, LookLight, LookNormal}

// Describe says what a look shows.
func Describe(look string) string {
	switch look {
	case LookDark:
		return "the server's render of the draft: grey clay on a dark background"
	case LookCopper:
		return "the server's render of the draft: glossy copper on a terracotta background"
	case LookLight:
		return "the server's render of the draft: grey clay with hard shadows on light blue"
	case LookNormal:
		return "the server's render of the draft: surface normals as colours on light blue"
	}
	return look
}

// Frames is the number of frames per look; Step the turn between frames.
const (
	Frames = 16
	Step   = 2 * math.Pi / Frames
)

// Check reports whether img is a turntable sheet: 32 square tiles wide and
// 2 high.
func Check(img image.Image) error {
	b := img.Bounds()
	t := b.Dy() / 2
	if t < 16 || b.Dy() != 2*t || b.Dx() != 2*Frames*t {
		return fmt.Errorf("the picture is %dx%d, not a turntable sheet of %d x 2 square tiles", b.Dx(), b.Dy(), 2*Frames)
	}
	return nil
}

// Tile returns frame k (0-15, taken round) of look as its own image.
func Tile(sheet image.Image, look string, k int) (image.Image, error) {
	li := -1
	for i, l := range Looks {
		if l == look {
			li = i
		}
	}
	if li < 0 {
		return nil, fmt.Errorf("unknown turntable look %q", look)
	}
	if err := Check(sheet); err != nil {
		return nil, err
	}
	b := sheet.Bounds()
	t := b.Dy() / 2
	k = ((k % Frames) + Frames) % Frames
	col, row := 2*k+li%2, li/2
	r := image.Rect(b.Min.X+col*t, b.Min.Y+row*t, b.Min.X+col*t+t, b.Min.Y+row*t+t)
	out := image.NewNRGBA(image.Rect(0, 0, t, t))
	draw.Draw(out, out.Bounds(), sheet, r.Min, draw.Src)
	return out, nil
}

// FrameAt is the frame nearest a camera turned theta radians about +Y from
// the front (+Z) towards +X (three.js spherical theta).
func FrameAt(theta float64) int {
	k := int(math.Round(theta / Step))
	return ((k % Frames) + Frames) % Frames
}

// ErrNoView is a view the turntable has no frame for.
var ErrNoView = errors.New("the turntable shows the model only from round the side (no top view)")

// View draws frame k of look into a w x h picture: the square tile fitted
// to the shorter side, scaled by zoom (1 fits), centred, on the tile's own
// corner colour.
func View(sheet image.Image, look string, k, w, h int, zoom float64) (*image.NRGBA, error) {
	tile, err := Tile(sheet, look, k)
	if err != nil {
		return nil, err
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := color.NRGBAModel.Convert(tile.At(0, 0))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)
	if !(zoom > 0) {
		zoom = 1
	}
	side := int(math.Round(float64(min(w, h)) * zoom))
	if side < 1 {
		side = 1
	}
	r := image.Rect((w-side)/2, (h-side)/2, (w-side)/2+side, (h-side)/2+side)
	xdraw.CatmullRom.Scale(out, r, tile, tile.Bounds(), xdraw.Over, nil)
	return out, nil
}
