package service

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// TestWeightRule runs every weight case through generate_model's own path
// (prepareImages, then viewWeights, as Generate calls them) and through
// CheckInputSet, the check input sets get when the viewer stores them: both
// must decide alike, so a set the viewer stores is one generate_model
// takes. Cases: none, all in (0, 1], 1 exactly, some, 0, below 0, above 1,
// NaN, +Inf.
func TestWeightRule(t *testing.T) {
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
	for _, tc := range []struct {
		name string
		ws   [2]*float64
		ok   bool
	}{
		{"none", [2]*float64{nil, nil}, true},
		{"all", [2]*float64{w(0.3), w(0.9)}, true},
		{"one exactly", [2]*float64{w(1), w(0.05)}, true},
		{"some", [2]*float64{w(0.5), nil}, false},
		{"zero", [2]*float64{w(0), w(0.5)}, false},
		{"negative", [2]*float64{w(0.5), w(-0.1)}, false},
		{"above 1", [2]*float64{w(1.2), w(0.5)}, false},
		{"NaN", [2]*float64{w(math.NaN()), w(0.5)}, false},
		{"+Inf", [2]*float64{w(0.5), w(math.Inf(1))}, false},
	} {
		list := []GenerateImage{{Path: files[0], View: "F", Weight: tc.ws[0]}, {Path: files[1], View: "B", Weight: tc.ws[1]}}
		images, err := prepareImages(list)
		var sent []float64
		if err == nil {
			sent, err = viewWeights(images, "gen-2.5")
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s: generate_model: err %v, want ok %v", tc.name, err, tc.ok)
		}
		if err != nil && Classify(err).Code != "invalid_input" {
			t.Errorf("%s: generate_model: code %s", tc.name, Classify(err).Code)
		}
		if err == nil && len(sent) != 2 {
			t.Errorf("%s: %d weights sent", tc.name, len(sent))
		}
		set := []viewer.InputImage{{Path: files[0], View: "F", Weight: tc.ws[0]}, {Path: files[1], View: "B", Weight: tc.ws[1]}}
		serr := CheckInputSet(set)
		if (serr == nil) != tc.ok {
			t.Errorf("%s: CheckInputSet: err %v, want ok %v", tc.name, serr, tc.ok)
		}
		if serr != nil && Classify(serr).Code != "invalid_input" {
			t.Errorf("%s: CheckInputSet: code %s", tc.name, Classify(serr).Code)
		}
	}
}

// sentOK checks the weights viewWeights sent for ws: in hundredths, each
// at least 0.01, summing to 1, each within 0.07 of its share (rounding
// moves each share by at most half a hundredth and the 0.01 floor by at
// most one; the last, or the largest, takes what that adds up to).
func sentOK(t *testing.T, ws, out []float64) {
	t.Helper()
	sum, total := 0.0, 0
	for _, w := range ws {
		sum += w
	}
	for i, o := range out {
		c := int(math.Round(o * 100))
		if math.Abs(o*100-float64(c)) > 1e-9 || c < 1 {
			t.Fatalf("%v: weight %d sent as %v", ws, i, o)
		}
		if d := math.Abs(o - ws[i]/sum); d > 0.07 {
			t.Fatalf("%v: weight %d sent as %v, its share is %.4f", ws, i, o, ws[i]/sum)
		}
		total += c
	}
	if total != 100 {
		t.Fatalf("%v: sent %v, sum %d hundredths", ws, out, total)
	}
}

func sendWeights(t *testing.T, ws []float64) []float64 {
	t.Helper()
	images := make([]preparedImage, len(ws))
	for i := range ws {
		images[i].weight = &ws[i]
	}
	out, err := viewWeights(images, "gen-2.5")
	if err != nil {
		t.Fatalf("%v: %v", ws, err)
	}
	return out
}

// TestViewWeights: ordinary sets go out as the web splits them (the last
// image takes the remainder), and the sent weights are never 0 for a weight
// above 0: the review's sets (the last image used to get 0), every set the
// viewer window can make (0.05 to 1 in steps of 0.05, 1 to 5 images), and
// random sets of any weights in (0, 1].
func TestViewWeights(t *testing.T) {
	for _, tc := range []struct{ ws, want []float64 }{
		// The web's split: rounded shares, the last takes the remainder.
		{[]float64{1}, []float64{1}},
		{[]float64{1, 1}, []float64{0.5, 0.5}},
		{[]float64{1, 1, 1}, []float64{0.33, 0.33, 0.34}},
		{[]float64{0.33, 0.33, 0.33}, []float64{0.33, 0.33, 0.34}},
		{[]float64{1, 1, 1, 1, 1}, []float64{0.2, 0.2, 0.2, 0.2, 0.2}},
		{[]float64{0.5, 1}, []float64{0.33, 0.67}},
		{[]float64{1, 0.5, 0.5}, []float64{0.5, 0.25, 0.25}},
		{[]float64{1, 1, 1, 1, 0.3}, []float64{0.23, 0.23, 0.23, 0.23, 0.08}},
		{[]float64{1, 1, 1, 1, 0.1}, []float64{0.24, 0.24, 0.24, 0.24, 0.04}},
		// The web's split would send 0 (last left with nothing, or a share
		// rounding to 0): at least 0.01 each, the largest takes the rest.
		{[]float64{1, 1, 1, 1, 0.05}, []float64{0.24, 0.25, 0.25, 0.25, 0.01}},
		{[]float64{0.1, 0.9, 0.9, 0.9, 0.05}, []float64{0.04, 0.30, 0.32, 0.32, 0.02}},
		{[]float64{0.004, 1}, []float64{0.01, 0.99}},
		{[]float64{1, 0.004}, []float64{0.99, 0.01}},
	} {
		out := sendWeights(t, tc.ws)
		sentOK(t, tc.ws, out)
		for i := range out {
			if out[i] != tc.want[i] {
				t.Fatalf("%v: sent %v, want %v", tc.ws, out, tc.want)
			}
		}
	}
	var grid func(ws []float64, n int)
	grid = func(ws []float64, n int) {
		if len(ws) == n {
			sentOK(t, ws, sendWeights(t, append([]float64(nil), ws...)))
			return
		}
		for s := 1; s <= 20; s++ {
			grid(append(ws, float64(s)*0.05), n)
		}
	}
	for n := 1; n <= MaxImages; n++ {
		grid(nil, n)
	}
	rg := rand.New(rand.NewSource(1))
	for k := 0; k < 200000; k++ {
		ws := make([]float64, 1+rg.Intn(MaxImages))
		for i := range ws {
			ws[i] = 1 - rg.Float64() // (0, 1]
		}
		sentOK(t, ws, sendWeights(t, ws))
	}
}
