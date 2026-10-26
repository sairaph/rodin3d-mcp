package service

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// TestCheckInputSetWeights covers every weight case generate_model
// decides: none, all in (0, 1], some, 0, above 1, NaN.
func TestCheckInputSetWeights(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, n := range []string{"a.png", "b.png"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	w := func(v float64) *float64 { return &v }
	nan := 0.0
	nan = nan / nan
	for _, tc := range []struct {
		name string
		ws   [2]*float64
		ok   bool
	}{
		{"none", [2]*float64{nil, nil}, true},
		{"all", [2]*float64{w(0.3), w(1)}, true},
		{"some", [2]*float64{w(0.5), nil}, false},
		{"zero", [2]*float64{w(0), w(0.5)}, false},
		{"above 1", [2]*float64{w(1.2), w(0.5)}, false},
		{"NaN", [2]*float64{w(nan), w(0.5)}, false},
	} {
		imgs := []viewer.InputImage{{Path: files[0], View: "F", Weight: tc.ws[0]}, {Path: files[1], View: "B", Weight: tc.ws[1]}}
		err := CheckInputSet(imgs)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok %v", tc.name, err, tc.ok)
		}
		if err != nil && Classify(err).Code != "invalid_input" {
			t.Errorf("%s: code %s", tc.name, Classify(err).Code)
		}
	}
}
