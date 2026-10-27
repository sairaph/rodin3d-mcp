package turntable

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// TestTilesAndFrames: every look and frame of a synthetic sheet (each tile
// filled with its own column and row) comes from the tile the web shows,
// and every camera turn picks the nearest of the 16 frames.
func TestTilesAndFrames(t *testing.T) {
	const tile = 16
	sheet := image.NewNRGBA(image.Rect(0, 0, 2*Frames*tile, 2*tile))
	for y := 0; y < 2*tile; y++ {
		for x := 0; x < 2*Frames*tile; x++ {
			sheet.Set(x, y, color.NRGBA{R: uint8(x / tile), G: uint8(y / tile), A: 255})
		}
	}
	for li, look := range Looks {
		for k := -Frames; k < 2*Frames; k++ {
			img, err := Tile(sheet, look, k)
			if err != nil {
				t.Fatal(err)
			}
			want := 2*(((k%Frames)+Frames)%Frames) + li%2
			if c := color.NRGBAModel.Convert(img.At(tile/2, tile/2)).(color.NRGBA); int(c.R) != want || int(c.G) != li/2 {
				t.Fatalf("%s frame %d: tile col %d row %d, want col %d row %d", look, k, c.R, c.G, want, li/2)
			}
		}
	}
	if _, err := Tile(sheet, "other", 0); err == nil {
		t.Fatal("an unknown look was read")
	}
	if err := Check(image.NewNRGBA(image.Rect(0, 0, 360, 360))); err == nil {
		t.Fatal("a square picture passed as a sheet")
	}
	for k := 0; k < Frames; k++ {
		for _, d := range []float64{-0.49, 0, 0.49} {
			theta := (float64(k) + d) * Step
			if got := FrameAt(theta); got != k {
				t.Fatalf("theta %.3f: frame %d, want %d", theta, got, k)
			}
			if got := FrameAt(theta - 2*math.Pi); got != k {
				t.Fatalf("theta %.3f: frame %d, want %d", theta-2*math.Pi, got, k)
			}
		}
	}
}
