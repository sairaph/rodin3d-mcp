package service

import (
	"bytes"
	"image"
	"image/png"
	"runtime"
	"testing"
)

// TestViewShare: two users of one map file share one decode while either
// holds it; once none does, the share forgets it (the window keeps nothing
// for content it left).
func TestViewShare(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	f := mapFile{kind: "diffuse", data: buf.Bytes(), sum: sum(buf.Bytes())}
	s := &Service{}
	a, err := s.viewTexture(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.viewTexture(f)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("a second user decoded the map again")
	}
	if !a.SRGB || a.W != 8 {
		t.Fatalf("decoded %dx%d srgb %v", a.W, a.H, a.SRGB)
	}
	other := mapFile{kind: "normal", data: f.data, sum: f.sum}
	c, err := s.viewTexture(other)
	if err != nil {
		t.Fatal(err)
	}
	if c == a || c.SRGB {
		t.Fatal("the same file as another kind shared the colour map")
	}
	runtime.KeepAlive(a)
	runtime.KeepAlive(c)
	a, b, c = nil, nil, nil
	runtime.GC()
	key := f.kind + " " + f.sum
	if shared(&s.view.mu, s.view.maps, key) != nil {
		t.Fatal("the share kept a map nobody holds")
	}
}
