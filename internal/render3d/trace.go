package render3d

import (
	"errors"
	"image"
	"math"
	"sort"
	"sync"
)

// The Tracer mode: a path-traced picture of the textured model, built up
// sample by sample. The web viewer's Tracer is MuTracer (@meimu/mutracer
// 0.1.1, a WebGPU path tracer; viewer bundle of 2026-10-26, chunk 7859): a
// scene of the model's meshes with their PBR materials (no ground, no
// lights), lit by an environment that is also its background, blurred
// (AgenticEnvironmentBackgroundBake, backgroundBlurriness .25), its
// RenderLoop at final bounces 2, its display tone mapping Neutral at
// exposure 1. Here the same scene is traced on the CPU: the procedural
// interior environment (one of the web's four Tracer environments; the
// web's default is city) at intensity 1, the PBR material the pbr mode
// draws (diffuse, normal, roughness and metalness maps), two bounces with
// the environment sampled at every bounce (next-event estimation) and
// multiple importance sampling against the BRDF.

const (
	// tracerBounces is MuTracer's final.bounces: the camera ray's hit and
	// one more.
	tracerBounces = 2
	// TracerSamples is the samples per pixel of a finished Tracer picture.
	TracerSamples = 64
	// tracerBgBlur is the background's blur (the web's backgroundBlurriness).
	tracerBgBlur = 0.25
	// tracerEnvIntensity is the scene's environment intensity (a new
	// three.js Scene: 1).
	tracerEnvIntensity = 1.0
)

// Trace is a Tracer picture in progress: Add traces more samples per
// pixel, Image shows the samples so far. Samples are seeded by pixel and
// sample index, so a picture of n samples is the same however it was
// split into Add calls.
type Trace struct {
	m    *Mesh
	maps *Maps
	acc  *bvh
	cam  camera
	w, h int
	n    int
	eps  float64

	env, bgEnv *envMap
	samp       *envSampler
	bgOverride *vec3
	wire       []uint8
	cancel     func() bool

	sum, miss []float32 // linear RGB per pixel: hit paths, background
	hits      []float32 // camera rays that met the model
}

// NewTrace prepares a Tracer picture of m with maps from o's camera
// (o.Camera, else o.View; not ViewGrid), at o.Width x o.Height or o.Size;
// o.Mode must be ModeTracer. The mesh's hierarchy is built on first use.
func NewTrace(m *Mesh, maps *Maps, o Options) (*Trace, error) {
	if o.Mode != ModeTracer {
		return nil, errors.New("render3d: NewTrace draws ModeTracer only")
	}
	def, frame, c, err := prepare(m, maps, &o)
	if err != nil {
		return nil, err
	}
	if o.View == ViewGrid && o.Camera == nil {
		return nil, errors.New("render3d: a trace has one camera; ViewGrid is drawn by Render")
	}
	w, h := o.Size, o.Size
	if o.Width > 0 && o.Height > 0 {
		w, h = o.Width, o.Height
	}
	return newTrace(m, maps, def, o, frame, c, w, h)
}

func newTrace(m *Mesh, maps *Maps, def modeDef, o Options, frame Frame, c Camera, w, h int) (*Trace, error) {
	cam, ok := frame.camera(c, float64(w)/float64(h))
	if !ok {
		return nil, errors.New("render3d: the mesh has no extent")
	}
	t := &Trace{m: m, maps: maps, cam: cam, w: w, h: h, eps: 1e-4 * frame.Size(), cancel: o.canceled,
		env: environment(def.env), bgEnv: environment(def.bgEnv),
		sum: make([]float32, w*h*3), miss: make([]float32, w*h*3), hits: make([]float32, w*h)}
	t.samp = t.env.sampler()
	if o.Background != nil {
		bg := *o.Background
		t.bgOverride = &vec3{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255}
	}
	if o.WireOverlay {
		t.wire = wireMask(m, &cam, w, h)
	}
	t.acc = accelOf(m)
	return t, nil
}

// Samples is the number of samples per pixel traced so far.
func (t *Trace) Samples() int { return t.n }

// Add traces n more samples per pixel. It returns ErrCanceled when the
// options' Cancel closed; the samples of that call are then not counted.
func (t *Trace) Add(n int) error {
	if n <= 0 {
		return nil
	}
	sum := make([]float32, len(t.sum))
	miss := make([]float32, len(t.miss))
	hits := make([]float32, len(t.hits))
	parallel(t.h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			if t.cancel() {
				return
			}
			for x := 0; x < t.w; x++ {
				o := y*t.w + x
				var s, b vec3
				var nh float32
				for k := 0; k < n; k++ {
					rg := newRNG(uint32(x), uint32(y), uint32(t.n+k))
					l, hitModel, bg := t.path(x, y, &rg)
					if hitModel {
						s = s.add(l)
						nh++
					} else {
						b = b.add(bg)
					}
				}
				sum[o*3], sum[o*3+1], sum[o*3+2] = float32(s[0]), float32(s[1]), float32(s[2])
				miss[o*3], miss[o*3+1], miss[o*3+2] = float32(b[0]), float32(b[1]), float32(b[2])
				hits[o] = nh
			}
		}
	})
	if t.cancel() {
		return ErrCanceled
	}
	for i := range t.sum {
		t.sum[i] += sum[i]
		t.miss[i] += miss[i]
	}
	for i := range t.hits {
		t.hits[i] += hits[i]
	}
	t.n += n
	return nil
}

// Image is the picture of the samples so far, tone mapped (Neutral) and
// sRGB encoded; over a flat background colour when one was asked.
func (t *Trace) Image() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, t.w, t.h))
	lc := neutral(hexLinear(8947848))
	line := vec3{linearToSRGB(sat(lc[0])), linearToSRGB(sat(lc[1])), linearToSRGB(sat(lc[2]))}
	display := func(c vec3) vec3 {
		c = neutral(c)
		return vec3{encodeSRGB(c[0]), encodeSRGB(c[1]), encodeSRGB(c[2])}
	}
	n := float64(max(t.n, 1))
	parallel(t.h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < t.w; x++ {
				o := y*t.w + x
				s := vec3{float64(t.sum[o*3]), float64(t.sum[o*3+1]), float64(t.sum[o*3+2])}
				var pix vec3
				if t.bgOverride != nil {
					a := float64(t.hits[o]) / n
					pix = *t.bgOverride
					if a > 0 {
						pix = display(s.mul(1 / float64(t.hits[o]))).mul(a).add(pix.mul(1 - a))
					}
				} else {
					b := vec3{float64(t.miss[o*3]), float64(t.miss[o*3+1]), float64(t.miss[o*3+2])}
					pix = display(s.add(b).mul(1 / n))
				}
				if t.wire != nil && t.wire[o] == 1 {
					pix = line
				}
				p := out.PixOffset(x, y)
				out.Pix[p] = uint8(sat(pix[0])*255 + 0.5)
				out.Pix[p+1] = uint8(sat(pix[1])*255 + 0.5)
				out.Pix[p+2] = uint8(sat(pix[2])*255 + 0.5)
				out.Pix[p+3] = 255
			}
		}
	})
	return out
}

// surface is what a path sees where it meets the model.
type surface struct {
	P, Ng, N, V vec3
	dc, f0      vec3 // diffuse colour (base x (1 - metalness)) and F0
	rough       float64
}

// path traces one sample through pixel x, y: the radiance of a path that
// met the model, or (hitModel false) the background seen past it.
func (t *Trace) path(x, y int, rg *rng) (l vec3, hitModel bool, bg vec3) {
	c := &t.cam
	nx := (float64(x)+rg.float())/float64(t.w)*2 - 1
	ny := 1 - (float64(y)+rg.float())/float64(t.h)*2
	r := newRay(c.eye, c.right.mul(nx*c.aspect/c.focal).add(c.up.mul(ny/c.focal)).add(c.f).norm())
	tp := vec3{1, 1, 1}
	pdfPrev := 0.0
	for depth := 0; depth < tracerBounces; depth++ {
		h, ok := t.acc.intersect(&r, math.Inf(1), false)
		if !ok {
			if depth == 0 {
				return vec3{}, false, t.bgEnv.radiance(r.d, tracerBgBlur)
			}
			le := t.env.levels[0].at(r.d).mul(tracerEnvIntensity)
			l = l.add(tp.hadamard(le).mul(misWeight(pdfPrev, t.samp.pdf(r.d))))
			break
		}
		s := t.surface(h, r.d)
		// The environment, sampled directly.
		if L, pdfL, le := t.samp.sample(rg); pdfL > 0 {
			nl := s.N.dot(L)
			if nl > 0 && s.Ng.dot(L) > 0 {
				sr := newRay(s.P.add(s.Ng.mul(t.eps)), L)
				if _, blocked := t.acc.intersect(&sr, math.Inf(1), true); !blocked {
					f := s.brdf(L)
					w := misWeight(pdfL, s.pdf(L))
					l = l.add(tp.hadamard(f).hadamard(le.mul(tracerEnvIntensity)).mul(nl * w / pdfL))
				}
			}
		}
		if depth == tracerBounces-1 {
			break
		}
		L, weight, pdf := s.sample(rg)
		if pdf <= 0 || s.Ng.dot(L) <= 0 {
			break
		}
		tp = tp.hadamard(weight)
		pdfPrev = pdf
		r = newRay(s.P.add(s.Ng.mul(t.eps)), L)
	}
	return l, true, vec3{}
}

func misWeight(a, b float64) float64 {
	if a <= 0 {
		return 0
	}
	return a * a / (a*a + b*b)
}

// surface reads the PBR material where ray direction d meets the hit.
func (t *Trace) surface(h hit, d vec3) surface {
	m := t.m
	i0, i1, i2 := m.Idx[h.tri*3], m.Idx[h.tri*3+1], m.Idx[h.tri*3+2]
	b1, b2 := h.u, h.v
	b0 := 1 - b1 - b2
	p0, p1, p2 := v3(m.Pos[i0]), v3(m.Pos[i1]), v3(m.Pos[i2])
	e1, e2 := p1.sub(p0), p2.sub(p0)
	var s surface
	s.P = p0.mul(b0).add(p1.mul(b1)).add(p2.mul(b2))
	s.V = d.mul(-1)
	s.Ng = e1.cross(e2).norm()
	face := 1.0
	if s.Ng.dot(s.V) < 0 {
		s.Ng, face = s.Ng.mul(-1), -1
	}
	s.N = s.Ng
	if m.Nrm != nil {
		on := v3(m.Nrm[i0]).mul(b0).add(v3(m.Nrm[i1]).mul(b1)).add(v3(m.Nrm[i2]).mul(b2))
		if ln := on.length(); ln > 0 {
			s.N = on.mul(face / ln)
		}
	}
	if s.N.dot(s.V) <= 0 {
		s.N = s.Ng
	}
	base, metal, rough := vec3{1, 1, 1}, 1.0, 1.0 // the pbr mode's material
	if m.UV != nil && t.maps != nil {
		uv0, uv1, uv2 := m.UV[i0], m.UV[i1], m.UV[i2]
		u := b0*float64(uv0[0]) + b1*float64(uv1[0]) + b2*float64(uv2[0])
		v := b0*float64(uv0[1]) + b1*float64(uv1[1]) + b2*float64(uv2[1])
		if t.maps.Diffuse != nil {
			c := t.maps.Diffuse.Sample(u, v)
			base = vec3{float64(c[0]), float64(c[1]), float64(c[2])}
		}
		if t.maps.PBR != nil {
			p := t.maps.PBR.Sample(u, v)
			rough, metal = float64(p[1]), float64(p[2])
		}
		if t.maps.Normal != nil {
			du1, dv1 := float64(uv1[0]-uv0[0]), float64(uv1[1]-uv0[1])
			du2, dv2 := float64(uv2[0]-uv0[0]), float64(uv2[1]-uv0[1])
			if n, ok := normalMapFrame(e1, e2, du1, dv1, du2, dv2, s.N, t.maps.Normal.Sample(u, v)); ok && n.dot(s.V) > 0 {
				s.N = n
			}
		}
	}
	s.rough = math.Max(rough, 0.0525)
	s.dc = base.mul(1 - metal)
	s.f0 = mix3(vec3{0.04, 0.04, 0.04}, base, metal)
	return s
}

// normalMapFrame applies a tangent-space normal map texel c at a triangle
// with edges e1, e2 and texture coordinate steps (du, dv) along them, as
// three.js's getTangentFrame does from screen derivatives: T and B are the
// surface gradients of u and v in the plane of the shading normal N (which
// faces the viewer), scaled together so the longer is unit length.
func normalMapFrame(e1, e2 vec3, du1, dv1, du2, dv2 float64, N vec3, c [4]float32) (vec3, bool) {
	a, b := e2.cross(N), N.cross(e1)
	T := a.mul(du1).add(b.mul(du2))
	B := a.mul(dv1).add(b.mul(dv2))
	det := math.Max(T.dot(T), B.dot(B))
	if det == 0 {
		return N, false
	}
	sc := 1 / math.Sqrt(det)
	mn := vec3{float64(c[0])*2 - 1, float64(c[1])*2 - 1, float64(c[2])*2 - 1}
	return T.mul(sc * mn[0]).add(B.mul(sc * mn[1])).add(N.mul(mn[2])).norm(), true
}

// brdf is the PBR material's BRDF towards L (three.js direct lighting:
// Lambert diffuse plus GGX with Smith visibility and Schlick Fresnel).
func (s *surface) brdf(L vec3) vec3 {
	return s.dc.mul(1 / math.Pi).add(brdfGGX(L, s.V, s.N, s.f0, s.rough))
}

func lum(c vec3) float64 { return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2] }

// specProb is the chance of sampling the GGX lobe rather than the diffuse.
func (s *surface) specProb() float64 {
	ws := lum(fSchlick(s.f0, 1, sat(s.N.dot(s.V))))
	wd := lum(s.dc)
	if wd <= 0 {
		return 1
	}
	return math.Max(0.1, math.Min(0.9, ws/(ws+wd)))
}

// pdf is the solid-angle density with which sample picks L.
func (s *surface) pdf(L vec3) float64 {
	nl := s.N.dot(L)
	if nl <= 0 {
		return 0
	}
	ps := s.specProb()
	return ps*s.ggxPdf(L) + (1-ps)*nl/math.Pi
}

// ggxPdf is the density of the visible-normal GGX sample (Heitz 2018) for L.
func (s *surface) ggxPdf(L vec3) float64 {
	H := L.add(s.V).norm()
	nv, nh := sat(s.N.dot(s.V)), sat(s.N.dot(H))
	if nv <= 0 {
		return 0
	}
	alpha := s.rough * s.rough
	a2 := alpha * alpha
	den := nh*nh*(a2-1) + 1
	D := a2 / (math.Pi * den * den)
	g1 := 2 * nv / (nv + math.Sqrt(a2+(1-a2)*nv*nv))
	return g1 * D / (4 * nv)
}

// sample picks a direction from the BRDF: the direction, the path weight
// brdf x cos / pdf and the pdf (0 when no direction was found).
func (s *surface) sample(rg *rng) (vec3, vec3, float64) {
	tx, ty := onb(s.N)
	var L vec3
	if rg.float() < s.specProb() {
		alpha := s.rough * s.rough
		v := vec3{s.V.dot(tx), s.V.dot(ty), s.V.dot(s.N)}
		h := sampleVNDF(v, alpha, rg.float(), rg.float())
		H := tx.mul(h[0]).add(ty.mul(h[1])).add(s.N.mul(h[2]))
		L = H.mul(2 * s.V.dot(H)).sub(s.V)
	} else {
		r, phi := math.Sqrt(rg.float()), 2*math.Pi*rg.float()
		L = tx.mul(r * math.Cos(phi)).add(ty.mul(r * math.Sin(phi))).add(s.N.mul(math.Sqrt(math.Max(0, 1-r*r))))
	}
	nl := s.N.dot(L)
	if nl <= 0 {
		return L, vec3{}, 0
	}
	pdf := s.pdf(L)
	if pdf <= 0 {
		return L, vec3{}, 0
	}
	return L, s.brdf(L).mul(nl / pdf), pdf
}

// onb returns two unit vectors that make a right-handed frame with n.
func onb(n vec3) (vec3, vec3) {
	a := vec3{1, 0, 0}
	if math.Abs(n[0]) > 0.9 {
		a = vec3{0, 1, 0}
	}
	t := a.sub(n.mul(a.dot(n))).norm()
	return t, n.cross(t)
}

// sampleVNDF samples a GGX visible normal for view v in the local frame
// (normal +Z), Heitz 2018.
func sampleVNDF(v vec3, alpha, u1, u2 float64) vec3 {
	vh := vec3{alpha * v[0], alpha * v[1], v[2]}.norm()
	lensq := vh[0]*vh[0] + vh[1]*vh[1]
	t1 := vec3{1, 0, 0}
	if lensq > 0 {
		t1 = vec3{-vh[1], vh[0], 0}.mul(1 / math.Sqrt(lensq))
	}
	t2 := vh.cross(t1)
	r, phi := math.Sqrt(u1), 2*math.Pi*u2
	p1, p2 := r*math.Cos(phi), r*math.Sin(phi)
	sv := 0.5 * (1 + vh[2])
	p2 = (1-sv)*math.Sqrt(math.Max(0, 1-p1*p1)) + sv*p2
	nh := t1.mul(p1).add(t2.mul(p2)).add(vh.mul(math.Sqrt(math.Max(0, 1-p1*p1-p2*p2))))
	return vec3{alpha * nh[0], alpha * nh[1], math.Max(1e-6, nh[2])}.norm()
}

// rng is splitmix64, seeded per pixel and sample.
type rng struct{ s uint64 }

func newRNG(x, y, i uint32) rng {
	r := rng{s: uint64(x)*0x9E3779B97F4A7C15 ^ uint64(y)*0xC2B2AE3D27D4EB4F ^ uint64(i)*0x165667B19E3779F9}
	r.next()
	return r
}

func (r *rng) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
	z = (z ^ z>>27) * 0x94D049BB133111EB
	return z ^ z>>31
}

// float returns a number in [0, 1).
func (r *rng) float() float64 { return float64(r.next()>>11) / (1 << 53) }

// envSampler draws directions from an environment's sharp level in
// proportion to its brightness (a texel's weight is its brightest
// neighbour, as bilinear lookups spread a texel over them, times the
// texel's solid angle).
type envSampler struct {
	lv    *envLevel
	rows  []float64   // cumulative row weights, rows[h-1] = total
	cols  [][]float64 // per row, cumulative texel weights
	wts   []float64
	total float64
}

var samplerMu sync.Mutex

// sampler returns e's sampler, building it on first use.
func (e *envMap) sampler() *envSampler {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	if e.samp == nil {
		e.samp = newEnvSampler(&e.levels[0])
	}
	return e.samp
}

func newEnvSampler(lv *envLevel) *envSampler {
	w, h := lv.w, lv.h
	s := &envSampler{lv: lv, rows: make([]float64, h), cols: make([][]float64, h), wts: make([]float64, w*h)}
	l := func(x, y int) float64 {
		x, y = wrap(x, w), clampI(y, 0, h-1)
		o := (y*w + x) * 3
		return 0.2126*float64(lv.rgb[o]) + 0.7152*float64(lv.rgb[o+1]) + 0.0722*float64(lv.rgb[o+2])
	}
	acc := 0.0
	for y := 0; y < h; y++ {
		sinT := math.Cos((0.5 - (float64(y)+0.5)/float64(h)) * math.Pi)
		row := make([]float64, w)
		ra := 0.0
		for x := 0; x < w; x++ {
			m := 0.0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					m = math.Max(m, l(x+dx, y+dy))
				}
			}
			wt := (m + 1e-4) * sinT
			s.wts[y*w+x] = wt
			ra += wt
			row[x] = ra
		}
		s.cols[y] = row
		acc += ra
		s.rows[y] = acc
	}
	s.total = acc
	return s
}

// sample draws a direction, its solid-angle density and the radiance there.
func (s *envSampler) sample(rg *rng) (vec3, float64, vec3) {
	w, h := s.lv.w, s.lv.h
	y := sort.SearchFloat64s(s.rows, rg.float()*s.total)
	y = min(y, h-1)
	row := s.cols[y]
	x := sort.SearchFloat64s(row, rg.float()*row[w-1])
	x = min(x, w-1)
	u := (float64(x) + rg.float()) / float64(w)
	tt := (float64(y) + rg.float()) / float64(h)
	d := equirectDir(u, tt)
	sinT := math.Cos((0.5 - tt) * math.Pi)
	if sinT <= 1e-8 {
		return d, 0, vec3{}
	}
	p := s.wts[y*w+x] / s.total * float64(w*h) / (2 * math.Pi * math.Pi * sinT)
	return d, p, s.lv.at(d)
}

// pdf is the density with which sample draws direction d.
func (s *envSampler) pdf(d vec3) float64 {
	w, h := s.lv.w, s.lv.h
	u := math.Atan2(d[2], d[0])/(2*math.Pi) + 0.5
	v := math.Asin(math.Max(-1, math.Min(1, d[1])))/math.Pi + 0.5
	x := clampI(int(u*float64(w)), 0, w-1)
	y := clampI(int((1-v)*float64(h)), 0, h-1)
	sinT := math.Cos((v - 0.5) * math.Pi)
	if sinT <= 1e-8 {
		return 0
	}
	return s.wts[y*w+x] / s.total * float64(w*h) / (2 * math.Pi * math.Pi * sinT)
}

// wireMask marks the output pixels a triangle edge line covers (the wire
// overlay), from a raster pass of the mesh.
func wireMask(m *Mesh, cam *camera, w, h int) []uint8 {
	s := &shadeCtx{m: m, cam: cam, ss: 1, W: w, H: h, pos: m.Pos, idx: m.Idx, g: newGbuf(w, h), cancel: func() bool { return false }}
	s.p = project(cam, m.Pos, w, h)
	rasterize(s.g, s.p, m.Idx, float32(cam.near))
	mask := make([]uint8, w*h)
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < w; x++ {
				if f, ok := s.fragAt(y*w + x); ok && onEdge(&f) {
					mask[y*w+x] = 1
				}
			}
		}
	})
	return mask
}
