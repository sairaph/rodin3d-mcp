package render3d

import (
	"image"
	"math"
	"testing"
)

// Golden pixels of model file materials. The quad of quadMesh faces the
// front camera; its triangle 0 is the lower right half of the picture,
// triangle 1 the upper left. At 64 x 64 pixels (44, 44) lies inside
// triangle 0 and (20, 20) inside triangle 1, away from every edge, so all
// of a pixel's samples see the same surface.

// display is the 8-bit picture value of a linear colour through the
// textured modes' output (Neutral tone mapping, sRGB).
func display(c vec3) [3]int {
	c = neutral(c)
	var out [3]int
	for k := range out {
		out[k] = int(sat(encodeSRGB(c[k]))*255 + 0.5)
	}
	return out
}

func pixel(img *image.NRGBA, x, y int) [3]int {
	o := img.PixOffset(x, y)
	return [3]int{int(img.Pix[o]), int(img.Pix[o+1]), int(img.Pix[o+2])}
}

func near(a, b [3]int, tol int) bool {
	for k := range a {
		if d := a[k] - b[k]; d < -tol || d > tol {
			return false
		}
	}
	return true
}

func renderQuad(t *testing.T, m *Mesh, mode Mode) *image.NRGBA {
	t.Helper()
	img, _, err := Render(m, nil, Options{Mode: mode, View: ViewFront, Size: 64, Samples: 16})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func flatMat(r, g, b float64) *Material {
	m := DefaultMaterial()
	m.BaseColor = [4]float64{r, g, b, 1}
	return m
}

// TestMaterialPerTriangle: each triangle draws its own material, in the
// unlit shaded mode, in pbr for an unlit material and in the tracer.
func TestMaterialPerTriangle(t *testing.T) {
	red, blue := flatMat(0.5, 0.05, 0.05), flatMat(0.05, 0.05, 0.5)
	m := quadMesh()
	m.Mats, m.TriMat = []*Material{red, blue}, []uint16{0, 1}
	img := renderQuad(t, m, ModeShaded)
	if got, want := pixel(img, 44, 44), display(vec3{0.5, 0.05, 0.05}); !near(got, want, 1) {
		t.Errorf("shaded triangle 0: %v, want %v", got, want)
	}
	if got, want := pixel(img, 20, 20), display(vec3{0.05, 0.05, 0.5}); !near(got, want, 1) {
		t.Errorf("shaded triangle 1: %v, want %v", got, want)
	}
	red.Unlit, blue.Unlit = true, true
	for _, mode := range []Mode{ModePBR, ModeTracer} {
		img := renderQuad(t, m, mode)
		if got, want := pixel(img, 44, 44), display(vec3{0.5, 0.05, 0.05}); !near(got, want, 1) {
			t.Errorf("%s unlit triangle 0: %v, want %v", mode, got, want)
		}
		if got, want := pixel(img, 20, 20), display(vec3{0.05, 0.05, 0.5}); !near(got, want, 1) {
			t.Errorf("%s unlit triangle 1: %v, want %v", mode, got, want)
		}
	}
	// Without TriMat every triangle has the first material.
	m.TriMat = nil
	img = renderQuad(t, m, ModeShaded)
	if got, want := pixel(img, 20, 20), display(vec3{0.5, 0.05, 0.05}); !near(got, want, 1) {
		t.Errorf("no TriMat, triangle 1: %v, want %v", got, want)
	}
}

// strip is a 4 x 1 sRGB texture: red, green, blue, white.
func strip() *Texture {
	return &Texture{W: 4, H: 1, SRGB: true, Pix: []uint8{
		255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 255, 255, 255, 255}}
}

// TestTextureTransformAndWrap: a transform of scale 0 samples one texture
// coordinate everywhere, its offset; past the unit square the wrap modes
// pick their texel. The base colour factor tints the texel.
func TestTextureTransformAndWrap(t *testing.T) {
	texel := func(i int) vec3 {
		p := strip().Pix[i*4:]
		return vec3{float64(srgbLUT[p[0]]), float64(srgbLUT[p[1]]), float64(srgbLUT[p[2]])}
	}
	for _, tc := range []struct {
		name string
		u    float64 // offset: texel i has its centre at (i + 0.5) / 4
		wrap Wrap
		want int
	}{
		{"inside", 0.375, WrapRepeat, 1},
		{"repeat past 1", 1.375, WrapRepeat, 1},
		{"clamp past 1", 1.375, WrapClamp, 3},
		{"mirror past 1", 1.375, WrapMirror, 2},
		{"repeat below 0", -0.375, WrapRepeat, 2},
		{"clamp below 0", -0.375, WrapClamp, 0},
		{"mirror below 0", -0.375, WrapMirror, 1},
	} {
		mt := DefaultMaterial()
		mt.BaseColor = [4]float64{0.5, 1, 1, 1}
		mt.Base = &TexRef{Tex: strip(), WrapU: tc.wrap, Transform: NewUVTransform([2]float64{tc.u, 0.5}, 0, [2]float64{0, 0})}
		m := quadMesh()
		m.Mats = []*Material{mt}
		img := renderQuad(t, m, ModeShaded)
		want := display(texel(tc.want).hadamard(vec3{0.5, 1, 1}))
		for _, p := range [][2]int{{44, 44}, {20, 20}} {
			if got := pixel(img, p[0], p[1]); !near(got, want, 1) {
				t.Errorf("%s at %v: %v, want texel %d %v", tc.name, p, got, tc.want, want)
			}
		}
	}
	// KHR_texture_transform: scale, then rotate, then offset.
	tr := NewUVTransform([2]float64{0.1, 0.2}, math.Pi/2, [2]float64{2, 3})
	r := &TexRef{Transform: tr}
	if p := r.uv([2]float64{1, 0}); math.Abs(p[0]-0.1) > 1e-12 || math.Abs(p[1]+1.8) > 1e-12 {
		t.Errorf("transform of (1, 0): %v, want (0.1, -1.8)", p)
	}
	if d := r.duv([2]float64{0, 1}); math.Abs(d[0]-3) > 1e-12 || math.Abs(d[1]) > 1e-12 {
		t.Errorf("transform of the step (0, 1): %v, want (3, 0)", d)
	}
}

// TestEmissionAndOcclusion: pbr and the tracer add a material's emission;
// full occlusion leaves pbr's environment light out (it has no lights);
// the shaded mode draws the emission of a material without a base colour
// texture (the shaded pack).
func TestEmissionAndOcclusion(t *testing.T) {
	glow := vec3{0.3, 0.2, 0.1}
	mt := flatMat(0, 0, 0) // metalness and roughness 1: almost no reflection
	mt.Emissive = glow
	m := quadMesh()
	m.Mats = []*Material{mt}
	for _, mode := range []Mode{ModePBR, ModeTracer} {
		if got, want := pixel(renderQuad(t, m, mode), 44, 44), display(glow); !near(got, want, 3) {
			t.Errorf("%s emission: %v, want about %v", mode, got, want)
		}
	}
	// The emissive texture times the factor, unlit in shaded.
	half := &Texture{W: 1, H: 1, Pix: []uint8{128, 128, 128, 255}}
	mt.Emission = &TexRef{Tex: half}
	want := display(glow.mul(float64(linLUT[128])))
	if got := pixel(renderQuad(t, m, ModeShaded), 44, 44); !near(got, want, 1) {
		t.Errorf("shaded emission: %v, want %v", got, want)
	}

	white := flatMat(1, 1, 1)
	white.Metallic = 0
	m.Mats = []*Material{white}
	open := pixel(renderQuad(t, m, ModePBR), 44, 44)
	white.Occlusion = &TexRef{Tex: &Texture{W: 1, H: 1, Pix: []uint8{0, 0, 0, 255}}}
	if got := pixel(renderQuad(t, m, ModePBR), 44, 44); got != [3]int{0, 0, 0} {
		t.Errorf("full occlusion: %v, want black", got)
	}
	white.OcclusionStrength = 0.5
	got := pixel(renderQuad(t, m, ModePBR), 44, 44)
	if !(got[0] > 0 && got[0] < open[0]) {
		t.Errorf("half occlusion: %v, want between black and %v", got, open)
	}
}

// TestMaterialsWithoutUV: a mesh with materials draws the textured modes
// without texture coordinates (factors only); one without needs them.
func TestMaterialsWithoutUV(t *testing.T) {
	m := quadMesh()
	m.UV = nil
	m.Mats = []*Material{flatMat(0.5, 0.05, 0.05)}
	m.Mats[0].Base = &TexRef{Tex: strip()} // not sampled
	if got, want := pixel(renderQuad(t, m, ModeShaded), 44, 44), display(vec3{0.5, 0.05, 0.05}); !near(got, want, 1) {
		t.Errorf("no texture coordinates: %v, want %v", got, want)
	}
	m.Mats = nil
	if _, _, err := Render(m, flatMaps(), Options{Mode: ModePBR, Size: 32}); err == nil {
		t.Error("no materials, no texture coordinates: want an InputError")
	}
}

func TestValidateMaterials(t *testing.T) {
	for name, edit := range map[string]func(m *Mesh){
		"TriMat too short":    func(m *Mesh) { m.Mats, m.TriMat = []*Material{DefaultMaterial()}, []uint16{0} },
		"index out of range":  func(m *Mesh) { m.Mats, m.TriMat = []*Material{DefaultMaterial()}, []uint16{0, 1} },
		"TriMat without Mats": func(m *Mesh) { m.TriMat = []uint16{0, 0} },
		"nil material":        func(m *Mesh) { m.Mats = []*Material{nil} },
		"map without picture": func(m *Mesh) {
			mt := DefaultMaterial()
			mt.Normal = &TexRef{}
			m.Mats = []*Material{mt}
		},
	} {
		m := quadMesh()
		edit(m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestSimplifyKeepsMaterials: the quick copy keeps each kept triangle's
// material.
func TestSimplifyKeepsMaterials(t *testing.T) {
	const n = 80
	m := &Mesh{}
	for y := 0; y <= n; y++ {
		for x := 0; x <= n; x++ {
			m.Pos = append(m.Pos, [3]float32{float32(x)/n*2 - 1, float32(y)/n*2 - 1, 0})
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			a := uint32(y*(n+1) + x)
			k := uint16(0)
			if x >= n/2 {
				k = 1
			}
			m.Idx = append(m.Idx, a, a+1, a+n+2, a, a+n+2, a+n+1)
			m.TriMat = append(m.TriMat, k, k)
		}
	}
	m.Mats = []*Material{flatMat(1, 0, 0), flatMat(0, 0, 1)}
	s := Simplify(m, 500)
	if s == m || len(s.TriMat) != len(s.Idx)/3 || len(s.Mats) != 2 {
		t.Fatalf("simplified: %d triangles, %d material indices, %d materials", len(s.Idx)/3, len(s.TriMat), len(s.Mats))
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for i, k := range s.TriMat {
		x := (s.Pos[s.Idx[i*3]][0] + s.Pos[s.Idx[i*3+1]][0] + s.Pos[s.Idx[i*3+2]][0]) / 3
		if (x < -0.2 && k != 0) || (x > 0.2 && k != 1) {
			t.Fatalf("triangle %d at x %.2f has material %d", i, x, k)
		}
	}
}
