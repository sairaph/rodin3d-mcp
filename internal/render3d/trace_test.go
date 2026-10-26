package render3d

import (
	"math"
	"testing"
)

// texel is a 1x1 linear texture holding the normal map value n (each
// component -1..1).
func texel(n vec3) *Texture {
	b := func(x float64) uint8 { return uint8(math.Round((x*0.5 + 0.5) * 255)) }
	return &Texture{W: 1, H: 1, Pix: []uint8{b(n[0]), b(n[1]), b(n[2]), 255}}
}

// TestNormalMapOrientation checks both renderers against three.js's
// tangent frame: a map's +x tilts the normal towards growing u, +y towards
// growing v; on a face seen from behind (double-sided) both turn round.
// The quad faces +Z (towards the camera); u grows along +X, v along -Y
// (down the picture, as glTF images are stored).
func TestNormalMapOrientation(t *testing.T) {
	right, down := vec3{1, 0, 0}, vec3{0, -1, 0}
	N := vec3{0, 0, 1}
	for _, tc := range []struct {
		name string
		mn   vec3
		want vec3 // direction the normal leans towards, front face
	}{
		{"+x leans to +u", vec3{0.7, 0, 0.7}, right},
		{"-x leans to -u", vec3{-0.7, 0, 0.7}, right.mul(-1)},
		{"+y leans to +v", vec3{0, 0.7, 0.7}, down},
		{"-y leans to -v", vec3{0, -0.7, 0.7}, down.mul(-1)},
	} {
		for _, face := range []float64{1, -1} {
			want := tc.want.mul(face)
			// Raster: screen derivatives per pixel, rows downwards.
			const px = 0.01
			f := frag{face: face, uv: [2]float64{0.5, 0.5}, dPdx: right.mul(px), dPdy: down.mul(px),
				dUVdx: [2]float64{px, 0}, dUVdy: [2]float64{0, px}}
			s := &shadeCtx{maps: &Maps{Normal: texel(tc.mn)}}
			if got := s.normalMap(&f, N); got.dot(want) <= 0.5 {
				t.Errorf("raster %s, face %v: normal %v, want it towards %v", tc.name, face, got, want)
			}
			// Tracer: from the triangle's edges. A face seen from behind has
			// the other winding.
			p0, p1, p2 := vec3{0, 0, 0}, vec3{1, 0, 0}, vec3{0, 1, 0}
			uv0, uv1, uv2 := [2]float64{0, 1}, [2]float64{1, 1}, [2]float64{0, 0}
			if face < 0 {
				p1, p2, uv1, uv2 = p2, p1, uv2, uv1
			}
			e1, e2 := p1.sub(p0), p2.sub(p0)
			c := texel(tc.mn).Sample(0.5, 0.5)
			got, ok := normalMapFrame(e1, e2, uv1[0]-uv0[0], uv1[1]-uv0[1], uv2[0]-uv0[0], uv2[1]-uv0[1], N, c)
			if !ok || got.dot(want) <= 0.5 {
				t.Errorf("tracer %s, face %v: normal %v, want it towards %v", tc.name, face, got, want)
			}
		}
	}
}

// soup is a deterministic triangle soup in the unit cube.
func soup(n int) *Mesh {
	rg := newRNG(1, 2, 3)
	m := &Mesh{}
	for i := 0; i < n; i++ {
		c := [3]float32{float32(rg.float()*2 - 1), float32(rg.float()*2 - 1), float32(rg.float()*2 - 1)}
		for k := 0; k < 3; k++ {
			m.Pos = append(m.Pos, [3]float32{c[0] + float32(rg.float()*0.2-0.1), c[1] + float32(rg.float()*0.2-0.1), c[2] + float32(rg.float()*0.2-0.1)})
			m.Idx = append(m.Idx, uint32(len(m.Pos)-1))
		}
	}
	return m
}

// TestBVHMatchesBruteForce compares the hierarchy's nearest and any hits
// with testing every triangle, for rays from outside and inside the soup.
func TestBVHMatchesBruteForce(t *testing.T) {
	m := soup(3000)
	b := buildBVH(m)
	all := &bvh{tris: make([]int32, len(m.Idx)/3), tv: make([][9]float32, len(m.Idx)/3)}
	for i := range all.tris {
		all.tris[i] = int32(i)
		p0, p1, p2 := m.Pos[m.Idx[i*3]], m.Pos[m.Idx[i*3+1]], m.Pos[m.Idx[i*3+2]]
		all.tv[i] = [9]float32{p0[0], p0[1], p0[2], p1[0] - p0[0], p1[1] - p0[1], p1[2] - p0[2], p2[0] - p0[0], p2[1] - p0[1], p2[2] - p0[2]}
	}
	rg := newRNG(4, 5, 6)
	hits := 0
	for i := 0; i < 2000; i++ {
		o := vec3{rg.float()*6 - 3, rg.float()*6 - 3, rg.float()*6 - 3}
		if i%2 == 1 {
			o = o.mul(0.2) // inside the soup
		}
		d := vec3{rg.float()*2 - 1, rg.float()*2 - 1, rg.float()*2 - 1}.norm()
		r := newRay(o, d)
		var want hit
		found := false
		tmax := math.Inf(1)
		for k := range all.tris {
			if tt, u, v, ok := all.triangle(&r, k, tmax); ok {
				want, found, tmax = hit{tri: int32(k), t: tt, u: u, v: v}, true, tt
			}
		}
		got, ok := b.intersect(&r, math.Inf(1), false)
		if ok != found || (ok && (got.tri != want.tri || math.Abs(got.t-want.t) > 1e-9)) {
			t.Fatalf("ray %d: bvh %v %+v, brute force %v %+v", i, ok, got, found, want)
		}
		if _, any := b.intersect(&r, math.Inf(1), true); any != found {
			t.Fatalf("ray %d: any-hit %v, nearest %v", i, any, found)
		}
		if found {
			hits++
			if _, ok := b.intersect(&r, want.t*0.999, true); ok && want.t > 0 {
				// another triangle may lie nearer only if brute force missed it
				t.Fatalf("ray %d: a hit nearer than the nearest", i)
			}
		}
	}
	if hits < 200 {
		t.Fatalf("only %d of 2000 rays hit; the test does not exercise the hierarchy", hits)
	}
}

// quadMesh is a textured quad facing +Z.
func quadMesh() *Mesh {
	return &Mesh{
		Pos: [][3]float32{{-1, -1, 0}, {1, -1, 0}, {1, 1, 0}, {-1, 1, 0}},
		Nrm: [][3]float32{{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}},
		UV:  [][2]float32{{0, 1}, {1, 1}, {1, 0}, {0, 0}},
		Idx: []uint32{0, 1, 2, 0, 2, 3},
	}
}

func flatMaps() *Maps {
	return &Maps{Diffuse: &Texture{W: 1, H: 1, Pix: []uint8{200, 120, 60, 255}, SRGB: true},
		PBR: &Texture{W: 1, H: 1, Pix: []uint8{0, 128, 0, 255}}}
}

// TestTraceSplitInvariant: a picture of n samples is the same however the
// samples were added (each sample is seeded by pixel and index), up to
// float rounding in the sums.
func TestTraceSplitInvariant(t *testing.T) {
	m, maps := quadMesh(), flatMaps()
	o := Options{Mode: ModeTracer, Width: 48, Height: 32, View: ViewFront}
	a, err := NewTrace(m, maps, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Add(4); err != nil {
		t.Fatal(err)
	}
	b, _ := NewTrace(m, maps, o)
	for _, n := range []int{1, 3} {
		if err := b.Add(n); err != nil {
			t.Fatal(err)
		}
	}
	if a.Samples() != 4 || b.Samples() != 4 {
		t.Fatalf("samples %d and %d, want 4", a.Samples(), b.Samples())
	}
	ia, ib := a.Image(), b.Image()
	for i := range ia.Pix {
		if d := int(ia.Pix[i]) - int(ib.Pix[i]); d < -1 || d > 1 {
			t.Fatalf("byte %d: %d vs %d", i, ia.Pix[i], ib.Pix[i])
		}
	}
	// Render draws the same picture.
	img, _, err := Render(m, maps, Options{Mode: ModeTracer, Width: 48, Height: 32, View: ViewFront, Samples: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := range ia.Pix {
		if d := int(ia.Pix[i]) - int(img.Pix[i]); d < -1 || d > 1 {
			t.Fatalf("Render byte %d: %d vs %d", i, img.Pix[i], ia.Pix[i])
		}
	}
	// A cancelled Add counts nothing.
	cancel := make(chan struct{})
	close(cancel)
	o.Cancel = cancel
	c, _ := NewTrace(m, maps, o)
	if err := c.Add(2); err != ErrCanceled || c.Samples() != 0 {
		t.Fatalf("cancelled Add: %v, %d samples", err, c.Samples())
	}
}

// TestTraceRefusesWhatPBRRefuses: the tracer draws the textured model only.
func TestTraceRefusesWhatPBRRefuses(t *testing.T) {
	m := quadMesh()
	if _, err := NewTrace(m, nil, Options{Mode: ModeTracer, Size: 32}); err == nil {
		t.Fatal("no maps: want an InputError")
	}
	noUV := quadMesh()
	noUV.UV = nil
	if _, err := NewTrace(noUV, flatMaps(), Options{Mode: ModeTracer, Size: 32}); err == nil {
		t.Fatal("no texture coordinates: want an InputError")
	}
	if _, err := NewTrace(m, flatMaps(), Options{Mode: ModePBR, Size: 32}); err == nil {
		t.Fatal("another mode: want an error")
	}
	if _, err := NewTrace(m, flatMaps(), Options{Mode: ModeTracer, Size: 64, View: ViewGrid}); err == nil {
		t.Fatal("grid: want an error")
	}
}

// TestEnvSamplerDensity: sample reports the density pdf gives for the same
// direction, and the density integrates to 1 over the sphere.
func TestEnvSamplerDensity(t *testing.T) {
	s := environment(envRoom).sampler()
	rg := newRNG(7, 8, 9)
	for i := 0; i < 5000; i++ {
		d, p, _ := s.sample(&rg)
		if q := s.pdf(d); p > 0 && math.Abs(p-q) > 1e-6*p {
			t.Fatalf("sample %d: density %g, pdf %g", i, p, q)
		}
	}
	// Integrate pdf over the sphere on a grid of 4 x 4 points inside every
	// texel of the map, with the equirect Jacobian (2 pi^2 sin theta): the
	// density is constant per texel, so this sum is exact.
	w, h := s.lv.w*4, s.lv.h*4
	sum := 0.0
	for y := 0; y < h; y++ {
		tt := (float64(y) + 0.5) / float64(h)
		sinT := math.Cos((0.5 - tt) * math.Pi)
		for x := 0; x < w; x++ {
			sum += s.pdf(equirectDir((float64(x)+0.5)/float64(w), tt)) * 2 * math.Pi * math.Pi * sinT
		}
	}
	if got := sum / float64(w*h); math.Abs(got-1) > 1e-6 {
		t.Fatalf("pdf integrates to %.8f, want 1", got)
	}
}
