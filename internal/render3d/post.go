package render3d

import "math"

// shadowMap is an orthographic depth map for the directional light (Toon:
// _directionalLight castShadow, bias -.005 over near -1..far 10, normalBias .01).
type shadowMap struct {
	g            *gbuf
	r, u, fwd    vec3
	cx, cy, half float64
	res          int
	offset       float64
}

func (s *shadeCtx) buildShadow() {
	var l *light
	for i := range s.lights {
		if s.lights[i].kind == lightDir && s.lights[i].shadow {
			l = &s.lights[i]
		}
	}
	if l == nil {
		return
	}
	fwd := l.target.sub(l.pos).norm() // from the light into the scene
	r := fwd.cross(vec3{0, 1, 0})
	if r.length() < 1e-6 {
		r = vec3{1, 0, 0}
	}
	r = r.norm()
	u := r.cross(fwd)
	res := s.shadowRes
	if res <= 0 {
		res = 2048
	}
	sm := &shadowMap{r: r, u: u, fwd: fwd, res: res, cx: s.center.dot(r), cy: s.center.dot(u)}
	bmin, bmax := bounds(s.m.Pos)
	sm.half = bmax.sub(bmin).length()/2*1.05 + 1e-6
	sm.offset = -s.center.dot(fwd) + sm.half + 1
	p := projected{sx: make([]float32, len(s.m.Pos)), sy: make([]float32, len(s.m.Pos)), z: make([]float32, len(s.m.Pos))}
	parallel(len(s.m.Pos), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			sx, sy, z := sm.proj(v3(s.m.Pos[i]))
			p.sx[i], p.sy[i], p.z[i] = float32(sx), float32(sy), float32(z)
		}
	})
	sm.g = newGbuf(res, res)
	rasterize(sm.g, p, s.m.Idx, 1e-6)
	s.shadow = sm
}

func (sm *shadowMap) proj(P vec3) (float64, float64, float64) {
	x := (P.dot(sm.r)-sm.cx)/sm.half*0.5 + 0.5
	y := 0.5 - (P.dot(sm.u)-sm.cy)/sm.half*0.5
	return x * float64(sm.res), y * float64(sm.res), P.dot(sm.fwd) + sm.offset
}

func (s *shadeCtx) shadowAt(l *light, f *frag) float64 {
	sm := s.shadow
	if sm == nil || !l.shadow {
		return 1
	}
	P := f.P.add(f.Ng.mul(0.01))
	x, y, z := sm.proj(P)
	const bias = 0.005 * 11 // shadow.bias -.005 over the 11-unit depth range
	lit, n := 0.0, 0.0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			ix, iy := int(x)+dx, int(y)+dy
			n++
			if ix < 0 || iy < 0 || ix >= sm.res || iy >= sm.res {
				lit++
				continue
			}
			iz := sm.g.invZ[iy*sm.res+ix]
			if iz <= 0 || z-bias <= 1/float64(iz) {
				lit++
			}
		}
	}
	return lit / n
}

// applySAO is three.js SAOPass with the bundle's params (bias .3, scale 100,
// kernel radius 32 px, min resolution 0, blur on; camera far 1000), computed at
// output resolution and multiplied into the linear colour of every surface.
func (s *shadeCtx) applySAO(intensity float64, w, h int) {
	ss, W := s.ss, s.W
	n := w * h
	vz := make([]float32, n)   // view z (negative in front), 0 = background
	vp := make([]float32, n*3) // view position
	vn := make([]float32, n*3) // view normal
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < w; x++ {
				o := (y*ss+ss/2)*W + x*ss + ss/2
				f, ok := s.fragAt(o)
				if !ok {
					continue
				}
				p := s.cam.view(f.P)
				nn := f.ON.norm().mul(f.face)
				if f.ground || s.m.Nrm == nil {
					nn = f.Ng
				}
				nv := s.cam.viewDir(nn)
				i := y*w + x
				vz[i] = float32(p[2])
				for k := 0; k < 3; k++ {
					vp[i*3+k], vn[i*3+k] = float32(p[k]), float32(nv[k])
				}
			}
		}
	})
	scale := float64(h) / 700 // web canvas is about 700 device px tall
	kernel := 32 * scale
	const samples, rings = 7, 4
	angleStep := 2 * math.Pi * rings / samples
	const scaleDivFar = 100.0 / 1000
	const bias = 0.3
	ao := make([]float32, n)
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			if s.cancel() {
				return
			}
			for x := 0; x < w; x++ {
				i := y*w + x
				ao[i] = 1
				if vz[i] == 0 {
					continue
				}
				c := vec3{float64(vp[i*3]), float64(vp[i*3+1]), float64(vp[i*3+2])}
				cn := vec3{float64(vn[i*3]), float64(vn[i*3+1]), float64(vn[i*3+2])}
				u, v := (float64(x)+0.5)/float64(w), (float64(y)+0.5)/float64(h)
				sn := math.Sin(u*12.9898+v*78.233) * 43758.5453
				angle := (sn - math.Floor(sn)) * 2 * math.Pi
				rad := kernel / samples
				occ, wsum := 0.0, 0.0
				for k := 0; k < samples; k++ {
					sx := x + int(math.Round(math.Cos(angle)*rad))
					sy := y + int(math.Round(math.Sin(angle)*rad))
					rad += kernel / samples
					angle += angleStep
					if sx < 0 || sy < 0 || sx >= w || sy >= h {
						continue
					}
					j := sy*w + sx
					if vz[j] == 0 {
						continue
					}
					d := vec3{float64(vp[j*3]), float64(vp[j*3+1]), float64(vp[j*3+2])}.sub(c)
					dist := d.length()
					sd := scaleDivFar * dist
					if sd > 0 {
						occ += math.Max(0, cn.dot(d)/sd-bias) / (1 + sd*sd)
					}
					wsum++
				}
				if wsum > 0 {
					ao[i] = float32(sat(1 - occ*intensity/wsum))
				}
			}
		}
	})
	// separable Gaussian blur, radius 8 and sigma 4 web px, over surfaces only
	rad := int(math.Round(8 * scale))
	sigma := 4 * scale
	wts := make([]float64, rad+1)
	for k := range wts {
		wts[k] = math.Exp(-float64(k*k) / (2 * sigma * sigma))
	}
	blur := func(src []float32, dx, dy int) []float32 {
		dst := make([]float32, n)
		parallel(h, func(lo, hi int) {
			for y := lo; y < hi; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					if vz[i] == 0 {
						dst[i] = 1
						continue
					}
					sum, wsum := 0.0, 0.0
					for k := -rad; k <= rad; k++ {
						xx, yy := x+k*dx, y+k*dy
						if xx < 0 || yy < 0 || xx >= w || yy >= h {
							continue
						}
						j := yy*w + xx
						if vz[j] == 0 {
							continue
						}
						wk := wts[abs(k)]
						sum += wk * float64(src[j])
						wsum += wk
					}
					dst[i] = float32(sum / wsum)
				}
			}
		})
		return dst
	}
	ao = blur(blur(ao, 1, 0), 0, 1)
	parallel(s.H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < W; x++ {
				o := y*W + x
				if s.g.tri[o] < 0 {
					continue
				}
				a := ao[(y/ss)*w+x/ss]
				s.col[o*4] *= a
				s.col[o*4+1] *= a
				s.col[o*4+2] *= a
			}
		}
	})
}

func abs(k int) int {
	if k < 0 {
		return -k
	}
	return k
}

// applyOutline approximates OutlineEffect (thickness .003 in clip space, black):
// a sample becomes outline when a nearer surface lies within the outline width.
func (s *shadeCtx) applyOutline() {
	W, H := s.W, s.H
	R := int(math.Round(0.003 * float64(H) / 2))
	if R < 1 {
		R = 1
	}
	depth := make([]float32, W*H)
	for i, iz := range s.g.invZ {
		if s.g.tri[i] < 0 || iz <= 0 {
			depth[i] = float32(math.Inf(1))
		} else {
			depth[i] = 1 / iz
		}
	}
	minf := func(src []float32, dx, dy int) []float32 {
		dst := make([]float32, W*H)
		parallel(H, func(lo, hi int) {
			for y := lo; y < hi; y++ {
				for x := 0; x < W; x++ {
					m := float32(math.Inf(1))
					for k := -R; k <= R; k++ {
						xx, yy := x+k*dx, y+k*dy
						if xx < 0 || yy < 0 || xx >= W || yy >= H {
							continue
						}
						if v := src[yy*W+xx]; v < m {
							m = v
						}
					}
					dst[y*W+x] = m
				}
			}
		})
		return dst
	}
	near := minf(minf(depth, 1, 0), 0, 1)
	eps := float32(0.03 * s.N)
	for i := range depth {
		if near[i] < depth[i]-eps {
			s.col[i*4], s.col[i*4+1], s.col[i*4+2], s.col[i*4+3] = 0, 0, 0, 1
		}
	}
}
