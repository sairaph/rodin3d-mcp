package imaging

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
)

func TestWebPToPNG(t *testing.T) {
	out, w, h, err := ToPNG(fakehyper.WebP, 512)
	if err != nil {
		t.Fatal(err)
	}
	if w != 64 || h != 48 {
		t.Fatalf("size %dx%d, want 64x48 (never upscaled)", w, h)
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	}
}

func TestFitShrinksTheLongerEdge(t *testing.T) {
	_, w, h, err := ToPNG(fakehyper.JPEG(400, 200), 256)
	if err != nil || w != 256 || h != 128 {
		t.Fatalf("%dx%d %v", w, h, err)
	}
	_, w, h, err = ToPNG(fakehyper.PNG(100, 300), 150)
	if err != nil || w != 50 || h != 150 {
		t.Fatalf("%dx%d %v", w, h, err)
	}
}

func TestRejectsOtherData(t *testing.T) {
	if _, _, _, err := ToPNG([]byte("not an image at all"), 256); err == nil {
		t.Fatal("garbage decoded")
	}
}
