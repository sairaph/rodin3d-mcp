package basisu

import (
	"context"
	"os"
	"testing"
)

// The fixtures are the base colour texture of internal/modelfile's cube
// fixtures (testdata/gen.mjs there): 16 x 16, red top left, green top
// right, blue bottom left, yellow bottom right, encoded by ktx2-encoder
// 0.6.0 (basis_universal) as ETC1S and as UASTC with zstd.
var quadrants = [4][3]int{{220, 40, 40}, {40, 200, 60}, {40, 60, 220}, {230, 210, 40}}

func newDecoder(t *testing.T) *Decoder {
	t.Helper()
	d, err := New(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close(context.Background()) })
	return d
}

func TestDecodeQuadrants(t *testing.T) {
	d := newDecoder(t)
	for _, tc := range []struct {
		file string
		tol  int // per channel; ETC1S is the lossier
	}{{"quadrants_etc1s.ktx2", 24}, {"quadrants_uastc.ktx2", 8}} {
		b, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		if !IsKTX2(b) {
			t.Fatalf("%s: not KTX2", tc.file)
		}
		img, err := d.Decode(context.Background(), b)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if img.W != 16 || img.H != 16 || len(img.Pix) != 16*16*4 {
			t.Fatalf("%s: %dx%d, %d bytes", tc.file, img.W, img.H, len(img.Pix))
		}
		// The centre of each quadrant, away from block edges.
		for q, want := range quadrants {
			x, y := 4+8*(q%2), 4+8*(q/2)
			o := (y*16 + x) * 4
			for c := 0; c < 3; c++ {
				if d := int(img.Pix[o+c]) - want[c]; d < -tc.tol || d > tc.tol {
					t.Errorf("%s quadrant %d: pixel %v, want about %v", tc.file, q, img.Pix[o:o+4], want)
					break
				}
			}
			if img.Pix[o+3] != 255 {
				t.Errorf("%s quadrant %d: alpha %d", tc.file, q, img.Pix[o+3])
			}
		}
	}
}

// TestDecodeMalformed: a cut, a corrupted or a foreign file is an error,
// and the decoder still works after one.
func TestDecodeMalformed(t *testing.T) {
	d := newDecoder(t)
	ctx := context.Background()
	b, err := os.ReadFile("testdata/quadrants_uastc.ktx2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(ctx, []byte("\x89PNG\r\n\x1a\n")); err == nil {
		t.Error("a PNG: no error")
	}
	for _, n := range []int{12, 40, 80, len(b) / 2, len(b) - 1} {
		if _, err := d.Decode(ctx, b[:n]); err == nil {
			t.Errorf("cut to %d bytes: no error", n)
		}
	}
	bad := append([]byte(nil), b...)
	for i := 20; i < 48; i++ { // the header's sizes and level count
		bad[i] = 0xff
	}
	if _, err := d.Decode(ctx, bad); err == nil {
		t.Error("corrupted header: no error")
	}
	if _, err := d.Decode(ctx, b); err != nil {
		t.Fatalf("after the errors: %v", err)
	}
}
