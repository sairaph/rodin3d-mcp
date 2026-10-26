package render3d

import (
	"math"
	"sync"
)

// bvh is a bounding volume hierarchy over a mesh's triangles for the path
// tracer: binned SAH splits, at most bvhLeaf triangles per leaf, nodes in
// one array with the two children of a node side by side.
type bvh struct {
	nodes []bvhNode
	tris  []int32      // triangle ids in leaf order
	tv    [][9]float32 // per leaf-order triangle: v0, edge1, edge2
}

type bvhNode struct {
	min, max [3]float32
	// first is the left child (the right one is first+1) of an inner node,
	// or the first leaf-order triangle of a leaf.
	first int32
	// count > 0: a leaf of count triangles; else -(split axis + 1).
	count int32
}

const (
	bvhLeaf = 4
	bvhBins = 12
	// bvhMaxDepth bounds the tree depth, so the traversal stack (64) always
	// has room: it holds at most depth + 1 nodes.
	bvhMaxDepth = 60
)

// bvhMu guards Mesh.accel: a mesh's hierarchy is built once, on first trace.
var bvhMu sync.Mutex

// accelOf returns m's hierarchy, building it on first use.
func accelOf(m *Mesh) *bvh {
	bvhMu.Lock()
	b := m.accel
	bvhMu.Unlock()
	if b != nil {
		return b
	}
	b = buildBVH(m)
	bvhMu.Lock()
	if m.accel == nil {
		m.accel = b
	} else {
		b = m.accel
	}
	bvhMu.Unlock()
	return b
}

func buildBVH(m *Mesh) *bvh {
	nt := len(m.Idx) / 3
	b := &bvh{tris: make([]int32, nt)}
	bmin := make([][3]float32, nt)
	bmax := make([][3]float32, nt)
	cen := make([][3]float32, nt)
	parallel(nt, func(lo, hi int) {
		for t := lo; t < hi; t++ {
			b.tris[t] = int32(t)
			p0, p1, p2 := m.Pos[m.Idx[t*3]], m.Pos[m.Idx[t*3+1]], m.Pos[m.Idx[t*3+2]]
			for k := 0; k < 3; k++ {
				bmin[t][k] = min(p0[k], p1[k], p2[k])
				bmax[t][k] = max(p0[k], p1[k], p2[k])
				cen[t][k] = (bmin[t][k] + bmax[t][k]) / 2
			}
		}
	})
	b.nodes = make([]bvhNode, 1, 2*nt/bvhLeaf+1)
	type task struct{ node, lo, hi, depth int }
	stack := []task{{0, 0, nt, 0}}
	for len(stack) > 0 {
		tk := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := &b.nodes[tk.node]
		n.min, n.max = [3]float32{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}, [3]float32{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
		cmin, cmax := n.min, n.max
		for _, t := range b.tris[tk.lo:tk.hi] {
			for k := 0; k < 3; k++ {
				n.min[k], n.max[k] = min(n.min[k], bmin[t][k]), max(n.max[k], bmax[t][k])
				cmin[k], cmax[k] = min(cmin[k], cen[t][k]), max(cmax[k], cen[t][k])
			}
		}
		count := tk.hi - tk.lo
		axis, split := -1, 0
		if count > bvhLeaf && tk.depth < bvhMaxDepth {
			axis, split = sahSplit(b.tris[tk.lo:tk.hi], bmin, bmax, cen, cmin, cmax)
		}
		if axis < 0 {
			n.first, n.count = int32(tk.lo), int32(count)
			continue
		}
		mid := tk.lo + split
		first := len(b.nodes)
		n.first, n.count = int32(first), -int32(axis+1)
		b.nodes = append(b.nodes, bvhNode{}, bvhNode{})
		stack = append(stack, task{first, tk.lo, mid, tk.depth + 1}, task{first + 1, mid, tk.hi, tk.depth + 1})
	}
	b.tv = make([][9]float32, nt)
	parallel(nt, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			t := b.tris[i]
			p0, p1, p2 := m.Pos[m.Idx[t*3]], m.Pos[m.Idx[t*3+1]], m.Pos[m.Idx[t*3+2]]
			b.tv[i] = [9]float32{p0[0], p0[1], p0[2], p1[0] - p0[0], p1[1] - p0[1], p1[2] - p0[2], p2[0] - p0[0], p2[1] - p0[1], p2[2] - p0[2]}
		}
	})
	return b
}

func boxArea(mn, mx [3]float32) float64 {
	dx, dy, dz := float64(mx[0]-mn[0]), float64(mx[1]-mn[1]), float64(mx[2]-mn[2])
	if dx < 0 || dy < 0 || dz < 0 {
		return 0
	}
	return dx*dy + dy*dz + dz*dx
}

// sahSplit picks the binned SAH split of tris over the centroid box and
// partitions tris in place: the first split triangles go left. axis is -1
// when no split costs less than a leaf, or the centroids do not spread.
func sahSplit(tris []int32, bmin, bmax, cen [][3]float32, cmin, cmax [3]float32) (axis, split int) {
	type bin struct {
		mn, mx [3]float32
		n      int
	}
	empty := func() bin {
		inf, ninf := float32(math.Inf(1)), float32(math.Inf(-1))
		return bin{mn: [3]float32{inf, inf, inf}, mx: [3]float32{ninf, ninf, ninf}}
	}
	grow := func(a *bin, mn, mx [3]float32, n int) {
		for k := 0; k < 3; k++ {
			a.mn[k], a.mx[k] = min(a.mn[k], mn[k]), max(a.mx[k], mx[k])
		}
		a.n += n
	}
	best, bestAxis, bestBin := math.Inf(1), -1, 0
	for k := 0; k < 3; k++ {
		ext := cmax[k] - cmin[k]
		if !(ext > 0) {
			continue
		}
		var bins [bvhBins]bin
		for i := range bins {
			bins[i] = empty()
		}
		scale := float32(bvhBins) / ext
		for _, t := range tris {
			i := min(int((cen[t][k]-cmin[k])*scale), bvhBins-1)
			grow(&bins[i], bmin[t], bmax[t], 1)
		}
		var right [bvhBins]float64
		var rightN [bvhBins]int
		acc := empty()
		for i := bvhBins - 1; i > 0; i-- {
			grow(&acc, bins[i].mn, bins[i].mx, bins[i].n)
			right[i], rightN[i] = boxArea(acc.mn, acc.mx), acc.n
		}
		acc = empty()
		for i := 0; i < bvhBins-1; i++ {
			grow(&acc, bins[i].mn, bins[i].mx, bins[i].n)
			if acc.n == 0 || rightN[i+1] == 0 {
				continue
			}
			c := boxArea(acc.mn, acc.mx)*float64(acc.n) + right[i+1]*float64(rightN[i+1])
			if c < best {
				best, bestAxis, bestBin = c, k, i
			}
		}
	}
	if bestAxis < 0 {
		return -1, 0
	}
	// A leaf costs its triangles times the node's area; keep it when no
	// split is cheaper and it is small enough.
	var all bin = empty()
	for _, t := range tris {
		grow(&all, bmin[t], bmax[t], 1)
	}
	if len(tris) <= 4*bvhLeaf && best >= boxArea(all.mn, all.mx)*float64(len(tris)) {
		return -1, 0
	}
	k := bestAxis
	scale := float32(bvhBins) / (cmax[k] - cmin[k])
	i, j := 0, len(tris)-1
	for i <= j {
		if min(int((cen[tris[i]][k]-cmin[k])*scale), bvhBins-1) <= bestBin {
			i++
		} else {
			tris[i], tris[j] = tris[j], tris[i]
			j--
		}
	}
	if i == 0 || i == len(tris) {
		return -1, 0
	}
	return k, i
}

// hit is the nearest triangle a ray meets: its id, distance and the
// barycentric weights of its vertices 1 and 2.
type hit struct {
	tri  int32
	t    float64
	u, v float64
}

// ray is a ray with its precomputed inverse direction.
type ray struct {
	o, d vec3
	inv  vec3
	neg  [3]bool
}

func newRay(o, d vec3) ray {
	r := ray{o: o, d: d}
	for k := 0; k < 3; k++ {
		r.inv[k] = 1 / d[k]
		r.neg[k] = d[k] < 0
	}
	return r
}

// box returns the entry distance of r into the node's box, or false.
func (r *ray) box(n *bvhNode, tmax float64) (float64, bool) {
	t0, t1 := 0.0, tmax
	for k := 0; k < 3; k++ {
		a := (float64(n.min[k]) - r.o[k]) * r.inv[k]
		b := (float64(n.max[k]) - r.o[k]) * r.inv[k]
		if a > b {
			a, b = b, a
		}
		// NaN (a zero direction component on the slab plane) keeps the
		// bounds as they are.
		if a > t0 {
			t0 = a
		}
		if b < t1 {
			t1 = b
		}
		if t0 > t1 {
			return 0, false
		}
	}
	return t0, true
}

// triangle intersects r with leaf-order triangle i (Moller-Trumbore, both
// faces), nearer than tmax.
func (b *bvh) triangle(r *ray, i int, tmax float64) (t, u, v float64, ok bool) {
	tv := &b.tv[i]
	e1 := vec3{float64(tv[3]), float64(tv[4]), float64(tv[5])}
	e2 := vec3{float64(tv[6]), float64(tv[7]), float64(tv[8])}
	p := r.d.cross(e2)
	det := e1.dot(p)
	if det > -1e-12 && det < 1e-12 {
		return 0, 0, 0, false
	}
	inv := 1 / det
	s := r.o.sub(vec3{float64(tv[0]), float64(tv[1]), float64(tv[2])})
	u = s.dot(p) * inv
	if u < 0 || u > 1 {
		return 0, 0, 0, false
	}
	q := s.cross(e1)
	v = r.d.dot(q) * inv
	if v < 0 || u+v > 1 {
		return 0, 0, 0, false
	}
	t = e2.dot(q) * inv
	if t <= 0 || t >= tmax {
		return 0, 0, 0, false
	}
	return t, u, v, true
}

// intersect returns the nearest hit of r closer than tmax. any stops at
// the first hit (shadow rays).
func (b *bvh) intersect(r *ray, tmax float64, any bool) (hit, bool) {
	var h hit
	found := false
	var stack [64]int32
	sp := 0
	if _, ok := r.box(&b.nodes[0], tmax); !ok {
		return h, false
	}
	stack[sp] = 0
	sp++
	for sp > 0 {
		sp--
		n := &b.nodes[stack[sp]]
		if _, ok := r.box(n, tmax); !ok {
			continue
		}
		if n.count > 0 {
			for i := int(n.first); i < int(n.first+n.count); i++ {
				if t, u, v, ok := b.triangle(r, i, tmax); ok {
					h, found, tmax = hit{tri: b.tris[i], t: t, u: u, v: v}, true, t
					if any {
						return h, true
					}
				}
			}
			continue
		}
		// Visit the near child first: push the far one below it.
		axis := -n.count - 1
		near, far := n.first, n.first+1
		if r.neg[axis] {
			near, far = far, near
		}
		if sp+2 > len(stack) {
			continue // deeper than any hierarchy built here; skip safely
		}
		stack[sp] = far
		stack[sp+1] = near
		sp += 2
	}
	return h, found
}
