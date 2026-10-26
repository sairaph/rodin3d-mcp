package render3d

import (
	"image"
	"math"
)

type shadeCtx struct {
	m      *Mesh
	maps   *Maps
	def    modeDef
	cam    *camera
	center vec3
	ss     int
	W, H   int // sample buffer size (output size times ss)
	N      float64
	flat   bool
	nModel int
	cancel func() bool // true once the render is no longer wanted

	bgOverride *vec3 // display colour replacing the mode's background; nil: none

	env    *envMap // lighting of MeshStandardMaterial; nil: none
	bgEnv  *envMap // bgEnv background
	lights []light

	pos        [][3]float32
	idx        []uint32
	groundBase uint32
	groundY    float64
	g          *gbuf
	p          projected
	col        []float32 // linear RGBA (alpha = coverage)
	wire       []uint8   // 1 where a triangle edge line covers the sample

	depthNear, depthFar float64
	shadow              *shadowMap
	shadowRes           int // shadow map size (Toon); 2048, 512 for quick frames
}

// frag is what a pixel sees of its triangle.
type frag struct {
	tri    int
	ground bool
	P      vec3 // world position
	N      vec3 // shading normal (world, towards the viewer side)
	Ng     vec3 // face normal (world, towards the viewer side)
	ON     vec3 // interpolated vertex normal, unflipped (object space = world here)
	V      vec3 // to the eye
	uv     [2]float64
	hasUV  bool
	face   float64 // +1 front facing, -1 back facing
	// screen derivatives per output pixel
	dUVdx, dUVdy [2]float64
	dONdx, dONdy vec3
	dPdx, dPdy   vec3
	b            [3]float64
	db           [3][2]float64 // d(b[i])/dx, d(b[i])/dy per output pixel
}

func (s *shadeCtx) fragAt(o int) (frag, bool) {
	var f frag
	t := s.g.tri[o]
	if t < 0 {
		return f, false
	}
	f.tri = int(t)
	i0, i1, i2 := s.idx[t*3], s.idx[t*3+1], s.idx[t*3+2]
	b1, b2 := float64(s.g.b1[o]), float64(s.g.b2[o])
	b0 := 1 - b1 - b2
	f.b = [3]float64{b0, b1, b2}
	p0, p1, p2 := v3(s.pos[i0]), v3(s.pos[i1]), v3(s.pos[i2])
	f.P = p0.mul(b0).add(p1.mul(b1)).add(p2.mul(b2))
	fn := p1.sub(p0).cross(p2.sub(p0)).norm()
	f.V = s.cam.eye.sub(f.P).norm()
	f.face = 1
	if fn.dot(f.V) < 0 {
		f.face = -1
	}
	f.Ng = fn.mul(f.face)
	f.ground = s.groundBase > 0 && i0 >= s.groundBase
	// affine screen-space barycentric gradients, scaled to output pixels
	x0, y0 := float64(s.p.sx[i0]), float64(s.p.sy[i0])
	x1, y1 := float64(s.p.sx[i1]), float64(s.p.sy[i1])
	x2, y2 := float64(s.p.sx[i2]), float64(s.p.sy[i2])
	area := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
	if area != 0 {
		k := float64(s.ss) / area
		f.db[0] = [2]float64{(y1 - y2) * k, (x2 - x1) * k}
		f.db[1] = [2]float64{(y2 - y0) * k, (x0 - x2) * k}
		f.db[2] = [2]float64{-(f.db[0][0] + f.db[1][0]), -(f.db[0][1] + f.db[1][1])}
	}
	f.dPdx = p0.mul(f.db[0][0]).add(p1.mul(f.db[1][0])).add(p2.mul(f.db[2][0]))
	f.dPdy = p0.mul(f.db[0][1]).add(p1.mul(f.db[1][1])).add(p2.mul(f.db[2][1]))
	if f.ground {
		f.N, f.ON = vec3{0, 1, 0}.mul(f.face), vec3{0, 1, 0}
		return f, true
	}
	if s.m.Nrm != nil {
		n0, n1, n2 := v3(s.m.Nrm[i0]), v3(s.m.Nrm[i1]), v3(s.m.Nrm[i2])
		f.ON = n0.mul(b0).add(n1.mul(b1)).add(n2.mul(b2))
		f.dONdx = n0.mul(f.db[0][0]).add(n1.mul(f.db[1][0])).add(n2.mul(f.db[2][0]))
		f.dONdy = n0.mul(f.db[0][1]).add(n1.mul(f.db[1][1])).add(n2.mul(f.db[2][1]))
	} else {
		f.ON = fn
	}
	if s.flat {
		f.N = f.Ng
	} else {
		f.N = f.ON.norm().mul(f.face)
	}
	if s.m.UV != nil {
		u0, u1, u2 := s.m.UV[i0], s.m.UV[i1], s.m.UV[i2]
		f.uv = [2]float64{b0*float64(u0[0]) + b1*float64(u1[0]) + b2*float64(u2[0]), b0*float64(u0[1]) + b1*float64(u1[1]) + b2*float64(u2[1])}
		for k := 0; k < 2; k++ {
			f.dUVdx[k] = float64(u0[k])*f.db[0][0] + float64(u1[k])*f.db[1][0] + float64(u2[k])*f.db[2][0]
			f.dUVdy[k] = float64(u0[k])*f.db[0][1] + float64(u1[k])*f.db[1][1] + float64(u2[k])*f.db[2][1]
		}
		f.hasUV = true
	}
	return f, true
}

func (s *shadeCtx) shadeAll() {
	parallel(s.H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			if s.cancel() {
				return
			}
			for x := 0; x < s.W; x++ {
				o := y*s.W + x
				f, ok := s.fragAt(o)
				if !ok {
					continue
				}
				if s.wire != nil && !f.ground && onEdge(&f) {
					s.wire[o] = 1
				}
				c, a := s.shade(&f)
				s.col[o*4], s.col[o*4+1], s.col[o*4+2], s.col[o*4+3] = float32(c[0]), float32(c[1]), float32(c[2]), float32(a)
			}
		}
	})
}

// onEdge reports whether the sample lies within half an output pixel of
// one of its triangle's edges (the wire overlay's 1 px lines).
func onEdge(f *frag) bool {
	for k := 0; k < 3; k++ {
		fw := math.Abs(f.db[k][0]) + math.Abs(f.db[k][1])
		if fw > 0 && f.b[k]/fw < 0.5 {
			return true
		}
	}
	return false
}

func sat(x float64) float64 { return math.Max(0, math.Min(1, x)) }

func mix3(a, b vec3, t float64) vec3 { return a.mul(1 - t).add(b.mul(t)) }

func (s *shadeCtx) shade(f *frag) (vec3, float64) {
	d := s.def
	if f.ground {
		return s.shadeGround(f)
	}
	switch d.mat {
	case matDepth:
		z := f.P.sub(s.cam.eye).dot(s.cam.f)
		v := 1 - (z-s.depthNear)/(s.depthFar-s.depthNear)
		v = sat(v)
		return vec3{v, v, v}, 1
	case matNormal:
		n := s.cam.viewDir(f.N)
		return vec3{n[0]*0.5 + 0.5, n[1]*0.5 + 0.5, n[2]*0.5 + 0.5}, 1
	case matChecker:
		return s.checker(f), 1
	case matBasic:
		t := s.maps.Shaded
		if t == nil {
			t = s.maps.Diffuse
		}
		c := t.Sample(f.uv[0], f.uv[1])
		return vec3{float64(c[0]), float64(c[1]), float64(c[2])}.hadamard(d.color), 1
	case matToon:
		c := s.maps.Diffuse.Sample(f.uv[0], f.uv[1])
		diff := vec3{float64(c[0]), float64(c[1]), float64(c[2])}.hadamard(d.color)
		return s.toon(f, diff), 1
	case matPhong:
		return s.phong(f, d.color), 1
	}
	// MeshStandardMaterial
	diff := d.color
	metal, rough := d.metal, d.rough
	N := f.N
	if d.useMaps {
		c := s.maps.Diffuse.Sample(f.uv[0], f.uv[1])
		diff = diff.hadamard(vec3{float64(c[0]), float64(c[1]), float64(c[2])})
		if s.maps.PBR != nil {
			p := s.maps.PBR.Sample(f.uv[0], f.uv[1])
			rough *= float64(p[1])
			metal *= float64(p[2])
		}
		if s.maps.Normal != nil && f.hasUV {
			N = s.normalMap(f, N)
		}
	}
	if d.copper {
		diff, rough, N = s.copper(f, diff, N)
	}
	c := s.standard(f, N, diff, metal, rough)
	if d.newGray && !d.direct {
		c = vec3{0.8 * math.Sqrt(math.Max(c[0], 0)), 0.8 * math.Sqrt(math.Max(c[1], 0)), 0.8 * math.Sqrt(math.Max(c[2], 0))}
	}
	return c, 1
}

// normalMap applies a tangent-space normal map with a per-triangle tangent
// frame from the UV gradients (three.js getTangentFrame).
func (s *shadeCtx) normalMap(f *frag, N vec3) vec3 {
	q0, q1 := f.dPdx, f.dPdy
	st0 := [2]float64{f.dUVdx[0], f.dUVdx[1]}
	st1 := [2]float64{f.dUVdy[0], f.dUVdy[1]}
	q1perp := q1.cross(N)
	q0perp := N.cross(q0)
	T := q1perp.mul(st0[0]).add(q0perp.mul(st1[0]))
	B := q1perp.mul(st0[1]).add(q0perp.mul(st1[1]))
	det := math.Max(T.dot(T), B.dot(B))
	if det == 0 {
		return N
	}
	// three.js takes dFdy with window y up; the derivatives here run with
	// pixel rows (y down), which turns T and B round (getTangentFrame does
	// not divide by the frame's determinant), so they are negated.
	sc := -1 / math.Sqrt(det)
	T, B = T.mul(sc*f.face), B.mul(sc*f.face)
	c := s.maps.Normal.Sample(f.uv[0], f.uv[1])
	mn := vec3{float64(c[0])*2 - 1, float64(c[1])*2 - 1, float64(c[2])*2 - 1}
	// glTF UVs have v down; three's frame uses the same texture coordinates.
	return T.mul(mn[0]).add(B.mul(mn[1])).add(N.mul(mn[2])).norm()
}

// copper is En(): colour map, roughness mix(.3,.7,G), bump x20 at uv*3, rust
// where the vertex normal changes fast across a pixel; the maps are the
// procedural copper of procedural.go.
func (s *shadeCtx) copper(f *frag, diff, N vec3) (vec3, float64, vec3) {
	u, v := f.uv[0], f.uv[1]
	col := copperColor
	edge := math.Abs(f.dONdx[0]) + math.Abs(f.dONdy[0]) + math.Abs(f.dONdx[1]) + math.Abs(f.dONdy[1]) + math.Abs(f.dONdx[2]) + math.Abs(f.dONdy[2])
	if sat(edge*2) > 0.3 {
		col = copperRustAt(u, v)
	}
	rough := 0.3 + 0.4*copperRoughnessAt(u, v)
	// bump: perturbNormalArb with dHdxy from the bump height at uv*3
	const bumpScale = 20.0
	bu, bv := u*3, v*3
	h := copperBumpAt(bu, bv)
	dBx := bumpScale * (copperBumpAt(bu+3*f.dUVdx[0], bv+3*f.dUVdx[1]) - h)
	dBy := bumpScale * (copperBumpAt(bu+3*f.dUVdy[0], bv+3*f.dUVdy[1]) - h)
	sx, sy := f.dPdx.norm(), f.dPdy.norm()
	R1 := sy.cross(N)
	R2 := N.cross(sx)
	fDet := sx.dot(R1) * f.face
	sg := 1.0
	if fDet < 0 {
		sg = -1
	}
	grad := R1.mul(dBx).add(R2.mul(dBy)).mul(sg)
	Nb := N.mul(math.Abs(fDet)).sub(grad).norm()
	return diff.hadamard(col), rough, Nb
}

// lightAt returns the direction to the light and its colour at P (three.js
// physically correct units, decay 2, spot cone as a step at angle pi/2).
func (s *shadeCtx) lightAt(l *light, P vec3) (vec3, vec3, bool) {
	switch l.kind {
	case lightDir:
		return l.pos.sub(l.target).norm(), l.color, true
	case lightPoint, lightSpot:
		lv := l.pos.sub(P)
		d := lv.length()
		L := lv.mul(1 / d)
		if l.kind == lightSpot {
			sd := l.pos.sub(l.target).norm()
			if L.dot(sd) <= l.coneCos {
				return L, vec3{}, false
			}
		}
		att := 1 / math.Max(d*d, 0.01)
		return L, l.color.mul(att), true
	}
	return vec3{}, vec3{}, false
}

func fSchlick(f0 vec3, f90, dotVH float64) vec3 {
	fr := math.Exp2((-5.55473*dotVH - 6.98316) * dotVH)
	return f0.mul(1 - fr).add(vec3{f90, f90, f90}.mul(fr))
}

func brdfGGX(L, V, N vec3, f0 vec3, rough float64) vec3 {
	alpha := rough * rough
	H := L.add(V).norm()
	dotNL, dotNV := sat(N.dot(L)), sat(N.dot(V))
	dotNH, dotVH := sat(N.dot(H)), sat(V.dot(H))
	F := fSchlick(f0, 1, dotVH)
	a2 := alpha * alpha
	gv := dotNL * math.Sqrt(a2+(1-a2)*dotNV*dotNV)
	gl := dotNV * math.Sqrt(a2+(1-a2)*dotNL*dotNL)
	Vis := 0.5 / math.Max(gv+gl, 1e-6)
	den := dotNH*dotNH*(a2-1) + 1
	D := a2 / (math.Pi * den * den)
	return F.mul(Vis * D)
}

func (s *shadeCtx) standard(f *frag, N vec3, base vec3, metal, rough float64) vec3 {
	V := f.V
	rough = math.Max(rough, 0.0525)
	dc := base.mul(1 - metal)
	f0 := mix3(vec3{0.04, 0.04, 0.04}, base, metal)
	var dd, ds, id, is vec3
	for i := range s.lights {
		l := &s.lights[i]
		if l.kind == lightAmbient {
			id = id.add(l.color.hadamard(dc).mul(1 / math.Pi))
			continue
		}
		L, lc, ok := s.lightAt(l, f.P)
		if !ok {
			continue
		}
		nl := sat(N.dot(L))
		if nl == 0 {
			continue
		}
		irr := lc.mul(nl * s.shadowAt(l, f))
		ds = ds.add(irr.hadamard(brdfGGX(L, V, N, f0, rough)))
		dd = dd.add(irr.hadamard(dc).mul(1 / math.Pi))
	}
	if s.env != nil && s.def.envIntensity > 0 {
		ei := s.def.envIntensity
		irr := s.env.radiance(N, 1).mul(math.Pi * ei)
		R := V.mul(-1).sub(N.mul(2 * N.dot(V.mul(-1))))
		r4 := rough * rough * rough * rough
		R = mix3(R, N, r4).norm()
		rad := s.env.radiance(R, rough).mul(ei)
		// computeMultiscattering with DFGApprox
		dotNV := sat(N.dot(V))
		c0 := [4]float64{-1, -0.0275, -0.572, 0.022}
		c1 := [4]float64{1, 0.0425, 1.04, -0.04}
		r := [4]float64{rough*c0[0] + c1[0], rough*c0[1] + c1[1], rough*c0[2] + c1[2], rough*c0[3] + c1[3]}
		a004 := math.Min(r[0]*r[0], math.Exp2(-9.28*dotNV))*r[0] + r[1]
		fa, fb := -1.04*a004+r[2], 1.04*a004+r[3]
		FssEss := f0.mul(fa).add(vec3{fb, fb, fb})
		Ess := fa + fb
		Ems := 1 - Ess
		Favg := f0.add(vec3{1, 1, 1}.sub(f0).mul(0.047619))
		Fms := vec3{}
		for k := 0; k < 3; k++ {
			Fms[k] = FssEss[k] * Favg[k] / (1 - Ems*Favg[k])
		}
		multi := Fms.mul(Ems)
		total := FssEss.add(multi)
		cw := irr.mul(1 / math.Pi)
		diffK := dc.mul(1 - math.Max(total[0], math.Max(total[1], total[2])))
		is = is.add(rad.hadamard(FssEss)).add(multi.hadamard(cw))
		id = id.add(diffK.hadamard(cw))
	}
	return dd.add(ds).add(id).add(is)
}

func (s *shadeCtx) phong(f *frag, diff vec3) vec3 {
	d := s.def
	N, V := f.N, f.V
	var out vec3
	for i := range s.lights {
		l := &s.lights[i]
		if l.kind == lightAmbient {
			out = out.add(l.color.hadamard(diff).mul(1 / math.Pi))
			continue
		}
		L, lc, ok := s.lightAt(l, f.P)
		if !ok {
			continue
		}
		nl := sat(N.dot(L))
		irr := lc.mul(nl)
		out = out.add(irr.hadamard(diff).mul(1 / math.Pi))
		H := L.add(V).norm()
		nh, vh := sat(N.dot(H)), sat(V.dot(H))
		F := fSchlick(d.specular, 1, vh)
		D := (1 / math.Pi) * (d.shininess*0.5 + 1) * math.Pow(nh, d.shininess)
		out = out.add(irr.hadamard(F.mul(0.25 * D)))
	}
	return out
}

// toon is MeshToonMaterial with the bundle's 4-step gradient (0, .25, .5, .75).
func (s *shadeCtx) toon(f *frag, diff vec3) vec3 {
	N := f.N
	var out vec3
	for i := range s.lights {
		l := &s.lights[i]
		if l.kind == lightAmbient {
			out = out.add(l.color.hadamard(diff).mul(1 / math.Pi))
			continue
		}
		L, lc, ok := s.lightAt(l, f.P)
		if !ok {
			continue
		}
		x := N.dot(L)*0.5 + 0.5
		step := math.Min(math.Floor(x*4), 3)
		gv := float64(uint8(step/4*256)) / 255
		irr := lc.mul(gv * s.shadowAt(l, f))
		out = out.add(irr.hadamard(diff).mul(1 / math.Pi))
	}
	return out
}

// checker is the UV checker: 16 x 16 squares over the unit UV square,
// light and dark, tinted by position (red grows with u, green with v) so a
// flipped or mirrored island shows, lit by a light at the camera.
func (s *shadeCtx) checker(f *frag) vec3 {
	u, v := f.uv[0]-math.Floor(f.uv[0]), f.uv[1]-math.Floor(f.uv[1])
	base := 0.12
	if (int(u*16)+int(v*16))%2 == 0 {
		base = 0.75
	}
	tint := vec3{0.45 + 0.55*u, 0.45 + 0.55*v, 0.7}
	lit := 0.3 + 0.7*sat(f.N.dot(f.V))
	return tint.mul(base * lit)
}

// shadeGround is the ground plane's material: standard, roughness .5,
// base colour .462 times a black texture (so black), alpha the contact
// shadow (groundAlpha), alpha blended.
func (s *shadeCtx) shadeGround(f *frag) (vec3, float64) {
	dx, dz := f.P[0]-s.center[0], f.P[2]-s.center[2]
	return s.standard(f, f.N, vec3{}, 0, 0.5), groundAlpha(math.Sqrt(dx*dx + dz*dz))
}

// finish composites over the background, applies the output transform and
// downsamples.
func (s *shadeCtx) finish(w, h int) *image.NRGBA {
	d := s.def
	W, H, ss := s.W, s.H, s.ss
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	toDisplay := func(c vec3) vec3 {
		switch {
		case d.outputPass:
			c = neutral(c)
			return vec3{encodeSRGB(c[0]), encodeSRGB(c[1]), encodeSRGB(c[2])}
		case d.direct:
			c = vec3{encodeSRGB(c[0]), encodeSRGB(c[1]), encodeSRGB(c[2])}
			if d.newGray {
				// zt() runs after the colour-space conversion when drawing straight
				// to the screen.
				c = vec3{0.8 * math.Sqrt(c[0]), 0.8 * math.Sqrt(c[1]), 0.8 * math.Sqrt(c[2])}
			}
			return c
		}
		return vec3{sat(c[0]), sat(c[1]), sat(c[2])} // OutputPass disabled: raw values
	}
	// LineMaterial #888888, tone mapped (Neutral) and sRGB encoded on the direct path.
	lc := neutral(hexLinear(8947848))
	lineDisplay := vec3{linearToSRGB(sat(lc[0])), linearToSRGB(sat(lc[1])), linearToSRGB(sat(lc[2]))}
	// CSS backgrounds are functions of the canvas position of a square
	// canvas; a canvas that is not square shows them as CSS background-size
	// cover does: scaled to its longer side, centred, the rest cut off.
	L := float64(max(W, H))
	bgU := func(px int) float64 { return 0.5 + (float64(px)+0.5-float64(W)/2)/L }
	bgV := func(py int) float64 { return 0.5 + (float64(py)+0.5-float64(H)/2)/L }
	// The clear colour of the direct path is only sRGB encoded.
	directBg := vec3{linearToSRGB(d.bgColor[0]), linearToSRGB(d.bgColor[1]), linearToSRGB(d.bgColor[2])}
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			if s.cancel() {
				return
			}
			for x := 0; x < w; x++ {
				var acc vec3
				for sy := 0; sy < ss; sy++ {
					for sx := 0; sx < ss; sx++ {
						px, py := x*ss+sx, y*ss+sy
						o := py*W + px
						c := vec3{float64(s.col[o*4]), float64(s.col[o*4+1]), float64(s.col[o*4+2])}
						a := float64(s.col[o*4+3])
						var pix vec3
						switch {
						case s.bgOverride != nil:
							pix = *s.bgOverride
							if a > 0 {
								pix = toDisplay(c).mul(a).add(pix.mul(1 - a))
							}
						case d.bg == bgColor:
							if d.direct {
								// direct path: the clear colour is only sRGB encoded; the
								// material patch (newGray) touches the mesh alone
								pix = directBg.mul(1 - a)
								if a > 0 {
									pix = pix.add(toDisplay(c).mul(a))
								}
							} else {
								pix = toDisplay(c.mul(a).add(d.bgColor.mul(1 - a)))
							}
						case d.bg == bgEnv:
							bc := vec3{}
							if s.bgEnv != nil {
								bc = s.bgEnv.radiance(s.rayDir(px, py), d.bgBlur)
							}
							pix = toDisplay(c.mul(a).add(bc.mul(1 - a)))
						default:
							under := vec3{0.5, 0.5, 0.5}
							if d.bgImage != nil {
								under = d.bgImage(bgU(px), bgV(py))
							}
							if a > 0 {
								pix = toDisplay(c).mul(a).add(under.mul(1 - a))
							} else {
								pix = under
							}
						}
						if s.wire != nil && s.wire[o] == 1 {
							pix = lineDisplay
						}
						acc = acc.add(pix)
					}
				}
				acc = acc.mul(1 / float64(ss*ss))
				o := out.PixOffset(x, y)
				out.Pix[o] = uint8(sat(acc[0])*255 + 0.5)
				out.Pix[o+1] = uint8(sat(acc[1])*255 + 0.5)
				out.Pix[o+2] = uint8(sat(acc[2])*255 + 0.5)
				out.Pix[o+3] = 255
			}
		}
	})
	return out
}

// rayDir is the world direction through a canvas pixel.
func (s *shadeCtx) rayDir(px, py int) vec3 {
	nx := ((float64(px)+0.5)/float64(s.W))*2 - 1
	ny := 1 - ((float64(py)+0.5)/float64(s.H))*2
	c := s.cam
	return c.right.mul(nx * c.aspect / c.focal).add(c.up.mul(ny / c.focal)).add(c.f).norm()
}

// neutral is three.js NeutralToneMapping (Khronos PBR Neutral), exposure 1.
func neutral(c vec3) vec3 {
	const start = 0.8 - 0.04
	const desat = 0.15
	x := math.Min(c[0], math.Min(c[1], c[2]))
	off := 0.04
	if x < 0.08 {
		off = x - 6.25*x*x
	}
	c = c.sub(vec3{off, off, off})
	peak := math.Max(c[0], math.Max(c[1], c[2]))
	if peak < start {
		return c
	}
	dd := 1 - start
	np := 1 - dd*dd/(peak+dd-start)
	c = c.mul(np / peak)
	g := 1 - 1/(desat*(peak-np)+1)
	return mix3(c, vec3{np, np, np}, g)
}
