// Package render3d draws Rodin stage meshes in the web viewer's modes with a
// pure-Go deferred rasteriser: a G-buffer pass (depth, triangle, perspective
// correct barycentrics), then per-pixel shading per mode, then post passes
// (SAO, outline, wire overlay, tone mapping, background).
package render3d

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

// Mesh is triangle geometry in glTF axes (Y up, front +Z).
type Mesh struct {
	Pos [][3]float32
	Nrm [][3]float32 // nil: flat shading only
	UV  [][2]float32 // nil: no texture coordinates
	Idx []uint32

	accel *bvh // the path tracer's hierarchy, built on first trace (bvhMu)
}

type vec3 [3]float64

func (a vec3) add(b vec3) vec3      { return vec3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a vec3) sub(b vec3) vec3      { return vec3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a vec3) mul(s float64) vec3   { return vec3{a[0] * s, a[1] * s, a[2] * s} }
func (a vec3) hadamard(b vec3) vec3 { return vec3{a[0] * b[0], a[1] * b[1], a[2] * b[2]} }
func (a vec3) dot(b vec3) float64   { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a vec3) cross(b vec3) vec3 {
	return vec3{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func (a vec3) length() float64 { return math.Sqrt(a.dot(a)) }
func (a vec3) norm() vec3 {
	l := a.length()
	if l == 0 {
		return a
	}
	return a.mul(1 / l)
}
func v3(p [3]float32) vec3 { return vec3{float64(p[0]), float64(p[1]), float64(p[2])} }

// camera is a three.js style perspective camera looking at target.
type camera struct {
	eye, target  vec3
	right, up, f vec3 // f points from the eye to the target
	fovY, aspect float64
	focal        float64 // 1 / tan(fovY/2)
	near, far    float64
}

func newCamera(eye, target vec3, fovYDeg, aspect float64) camera {
	f := target.sub(eye).norm()
	worldUp := vec3{0, 1, 0}
	r := f.cross(worldUp)
	if r.length() < 1e-9 {
		r = vec3{1, 0, 0}
	}
	r = r.norm()
	u := r.cross(f)
	fov := fovYDeg * math.Pi / 180
	return camera{eye: eye, target: target, right: r, up: u, f: f, fovY: fov, aspect: aspect,
		focal: 1 / math.Tan(fov/2), near: 0.1, far: 1000}
}

// view returns view-space coordinates: x right, y up, z towards the viewer
// (points in front have negative z, as in three.js).
func (c *camera) view(p vec3) vec3 {
	d := p.sub(c.eye)
	return vec3{d.dot(c.right), d.dot(c.up), -d.dot(c.f)}
}

// viewDir rotates a world direction into view space.
func (c *camera) viewDir(d vec3) vec3 { return vec3{d.dot(c.right), d.dot(c.up), -d.dot(c.f)} }

// worldDir rotates a view-space direction back to world space.
func (c *camera) worldDir(d vec3) vec3 {
	return c.right.mul(d[0]).add(c.up.mul(d[1])).add(c.f.mul(-d[2]))
}

// gbuf is the geometry buffer of one render: per pixel the triangle id (-1 for
// none), perspective-correct barycentrics of vertices 1 and 2, and 1/depth.
type gbuf struct {
	w, h   int
	tri    []int32
	b1, b2 []float32
	invZ   []float32
}

func newGbuf(w, h int) *gbuf {
	g := &gbuf{w: w, h: h, tri: make([]int32, w*h), b1: make([]float32, w*h), b2: make([]float32, w*h), invZ: make([]float32, w*h)}
	for i := range g.tri {
		g.tri[i] = -1
	}
	return g
}

// projected holds screen positions (pixels) and view depth of each vertex.
type projected struct {
	sx, sy, z []float32 // z = view depth (positive in front)
}

func project(cam *camera, pos [][3]float32, w, h int) projected {
	n := len(pos)
	p := projected{sx: make([]float32, n), sy: make([]float32, n), z: make([]float32, n)}
	parallel(n, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			v := cam.view(v3(pos[i]))
			d := -v[2]
			p.z[i] = float32(d)
			if d <= 1e-6 {
				p.sx[i], p.sy[i] = float32(math.NaN()), float32(math.NaN())
				continue
			}
			nx := v[0] * cam.focal / cam.aspect / d
			ny := v[1] * cam.focal / d
			p.sx[i] = float32((nx*0.5 + 0.5) * float64(w))
			p.sy[i] = float32((1 - (ny*0.5 + 0.5)) * float64(h))
		}
	})
	return p
}

// rasterize fills the G-buffer. Rows are split into bands, one goroutine per
// band; triangles are binned by band first so each band only sees its own.
func rasterize(g *gbuf, p projected, idx []uint32, near float32) {
	nt := len(idx) / 3
	workers := runtime.NumCPU()
	bandH := (g.h + workers - 1) / workers
	if bandH < 8 {
		bandH = 8
	}
	nb := (g.h + bandH - 1) / bandH
	bins := make([][]int32, nb)
	// Bin in parallel chunks, then merge per band.
	chunks := workers
	local := make([][][]int32, chunks)
	parallelChunks(nt, chunks, func(c, lo, hi int) {
		lb := make([][]int32, nb)
		for t := lo; t < hi; t++ {
			a, b, d := idx[t*3], idx[t*3+1], idx[t*3+2]
			if p.z[a] < near || p.z[b] < near || p.z[d] < near {
				continue
			}
			y0 := min(p.sy[a], p.sy[b], p.sy[d])
			y1 := max(p.sy[a], p.sy[b], p.sy[d])
			if !(y1 >= 0 && y0 < float32(g.h)) {
				continue
			}
			x0 := min(p.sx[a], p.sx[b], p.sx[d])
			x1 := max(p.sx[a], p.sx[b], p.sx[d])
			if !(x1 >= 0 && x0 < float32(g.w)) {
				continue
			}
			by0 := max(int(y0-0.5)/bandH, 0)
			by1 := min(int(y1+0.5)/bandH, nb-1)
			for k := by0; k <= by1; k++ {
				lb[k] = append(lb[k], int32(t))
			}
		}
		local[c] = lb
	})
	for k := 0; k < nb; k++ {
		n := 0
		for c := range local {
			if local[c] != nil {
				n += len(local[c][k])
			}
		}
		bins[k] = make([]int32, 0, n)
		for c := range local {
			if local[c] != nil {
				bins[k] = append(bins[k], local[c][k]...)
			}
		}
	}
	var wg sync.WaitGroup
	for k := 0; k < nb; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			ylo, yhi := k*bandH, min((k+1)*bandH, g.h)-1
			for _, t := range bins[k] {
				rasterTri(g, p, idx, t, ylo, yhi)
			}
		}(k)
	}
	wg.Wait()
}

func rasterTri(g *gbuf, p projected, idx []uint32, t int32, ylo, yhi int) {
	i0, i1, i2 := idx[t*3], idx[t*3+1], idx[t*3+2]
	x0, y0, x1, y1, x2, y2 := p.sx[i0], p.sy[i0], p.sx[i1], p.sy[i1], p.sx[i2], p.sy[i2]
	area := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
	if area == 0 || area != area {
		return
	}
	minX := max(int(math.Floor(float64(min(x0, x1, x2)-0.5))), 0)
	maxX := min(int(math.Ceil(float64(max(x0, x1, x2)-0.5))), g.w-1)
	minY := max(int(math.Floor(float64(min(y0, y1, y2)-0.5))), ylo)
	maxY := min(int(math.Ceil(float64(max(y0, y1, y2)-0.5))), yhi)
	if minX > maxX || minY > maxY {
		return
	}
	inv := 1 / area
	iz0, iz1, iz2 := 1/p.z[i0], 1/p.z[i1], 1/p.z[i2]
	dw0x, dw0y := (y1-y2)*inv, (x2-x1)*inv
	dw1x, dw1y := (y2-y0)*inv, (x0-x2)*inv
	px, py := float32(minX)+0.5, float32(minY)+0.5
	rowW0 := ((x1-px)*(y2-py) - (x2-px)*(y1-py)) * inv
	rowW1 := ((x2-px)*(y0-py) - (x0-px)*(y2-py)) * inv
	const eps = -1e-7
	for y := minY; y <= maxY; y++ {
		w0, w1 := rowW0, rowW1
		o := y*g.w + minX
		for x := minX; x <= maxX; x++ {
			w2 := 1 - w0 - w1
			if w0 >= eps && w1 >= eps && w2 >= eps {
				iz := w0*iz0 + w1*iz1 + w2*iz2
				if iz > g.invZ[o] {
					g.invZ[o] = iz
					g.tri[o] = t
					// perspective-correct barycentrics
					g.b1[o] = w1 * iz1 / iz
					g.b2[o] = w2 * iz2 / iz
				}
			}
			w0 += dw0x
			w1 += dw1x
			o++
		}
		rowW0 += dw0y
		rowW1 += dw1y
	}
}

// parallel runs f over [0,n) on every CPU. The range is cut into blocks
// about eight per CPU that the workers take in turn, so rows that cost more
// (a ground plane, a model in the middle of the picture) do not leave the
// other workers idle.
func parallel(n int, f func(lo, hi int)) {
	if n <= 0 {
		return
	}
	workers := min(runtime.NumCPU(), n)
	block := max(1, n/(workers*8))
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				lo := int(next.Add(int64(block))) - block
				if lo >= n {
					return
				}
				f(lo, min(lo+block, n))
			}
		}()
	}
	wg.Wait()
}

func parallelChunks(n, chunks int, f func(c, lo, hi int)) {
	if n == 0 {
		return
	}
	if chunks > n {
		chunks = n
	}
	var wg sync.WaitGroup
	step := (n + chunks - 1) / chunks
	for c := 0; c < chunks; c++ {
		lo, hi := c*step, min((c+1)*step, n)
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(c, lo, hi int) {
			defer wg.Done()
			f(c, lo, hi)
		}(c, lo, hi)
	}
	wg.Wait()
}
