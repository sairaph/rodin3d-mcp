package render3d

import (
	"math"
	"slices"
)

// Simplify returns a copy of m with about target triangles, for quick frames
// while the camera moves: the vertices are clustered on a uniform grid over
// the mesh's box, each cluster becomes one vertex (the mean position, the
// normalised mean normal, the first vertex's texture coordinates), and
// triangles whose corners fall into fewer than three clusters are dropped;
// the others keep their material. The grid is sized by up to eight trial
// clusterings. A mesh at or under
// target is returned as it is. Seams of the texture layout are not kept, so
// textured modes show smeared colours along them; the full-quality frame
// uses m.
func Simplify(m *Mesh, target int) *Mesh {
	if m == nil || target < 4 || len(m.Idx)/3 <= target {
		return m
	}
	mn, mx := bounds(m.Pos)
	ext := math.Max(mx[0]-mn[0], math.Max(mx[1]-mn[1], mx[2]-mn[2]))
	if !(ext > 0) || math.IsInf(ext, 0) {
		return m
	}
	// The triangle count falls with the grid resolution, roughly with its
	// square on a dense surface and more slowly where cells already hold
	// single vertices. Start at 2 sqrt(target) cells along the longest
	// side, step by the miss until the target is bracketed, then bisect;
	// keep the largest result at or under 1.2 target.
	res := 2 * math.Sqrt(float64(target))
	lo, hi := 0.0, math.Inf(1) // resolutions known to give too few / too many
	var best *Mesh
	for try := 0; try < 8; try++ {
		out := cluster(m, mn, ext, int(res))
		got := len(out.Idx) / 3
		if got >= 1 && got <= target*6/5 && (best == nil || got > len(best.Idx)/3) {
			best = out
		}
		if got >= target*4/5 && got <= target*6/5 {
			break
		}
		if got > target {
			hi = res
		} else {
			lo = res
		}
		switch {
		case lo > 0 && !math.IsInf(hi, 1):
			res = (lo + hi) / 2
		case got == 0:
			res *= 4
		default:
			res *= math.Max(0.25, math.Min(4, float64(target)/float64(got)))
		}
		if res < 2 || hi-lo < 1 {
			break
		}
	}
	if best == nil {
		return m
	}
	// cluster sizes the index slice for half the source's triangles; the
	// copy is kept as long as the mesh is shown, so it keeps only what it uses.
	best.Idx = slices.Clone(best.Idx)
	if best.TriMat != nil {
		best.TriMat = slices.Clone(best.TriMat)
	}
	return best
}

// cluster merges the vertices of m that share a cell of a grid with res
// cells along ext, starting at mn.
func cluster(m *Mesh, mn vec3, ext float64, res int) *Mesh {
	res = max(min(res, 1<<20), 2)
	cell := ext / float64(res)
	key := func(p [3]float32) uint64 {
		var k uint64
		for a := 0; a < 3; a++ {
			c := int64((float64(p[a]) - mn[a]) / cell)
			c = max(min(c, 1<<21-1), 0)
			k = k<<21 | uint64(c)
		}
		return k
	}
	ids := make(map[uint64]int32, len(m.Pos)/4)
	of := make([]int32, len(m.Pos))
	var sum [][3]float64
	var nsum [][3]float64
	var count []int32
	var uv [][2]float32
	for i, p := range m.Pos {
		k := key(p)
		id, ok := ids[k]
		if !ok {
			id = int32(len(sum))
			ids[k] = id
			sum = append(sum, [3]float64{})
			count = append(count, 0)
			if m.Nrm != nil {
				nsum = append(nsum, [3]float64{})
			}
			if m.UV != nil {
				uv = append(uv, m.UV[i])
			}
		}
		of[i] = id
		for a := 0; a < 3; a++ {
			sum[id][a] += float64(p[a])
		}
		if m.Nrm != nil {
			for a := 0; a < 3; a++ {
				nsum[id][a] += float64(m.Nrm[i][a])
			}
		}
		count[id]++
	}
	out := &Mesh{Pos: make([][3]float32, len(sum))}
	for i, s := range sum {
		n := float64(count[i])
		out.Pos[i] = [3]float32{float32(s[0] / n), float32(s[1] / n), float32(s[2] / n)}
	}
	if m.Nrm != nil {
		out.Nrm = make([][3]float32, len(nsum))
		for i, s := range nsum {
			v := vec3(s).norm()
			out.Nrm[i] = [3]float32{float32(v[0]), float32(v[1]), float32(v[2])}
		}
	}
	if m.UV != nil {
		out.UV = uv
	}
	out.Idx = make([]uint32, 0, len(m.Idx)/2)
	out.Mats = m.Mats
	if m.TriMat != nil {
		out.TriMat = make([]uint16, 0, len(m.TriMat)/2)
	}
	for t := 0; t+2 < len(m.Idx); t += 3 {
		a, b, c := of[m.Idx[t]], of[m.Idx[t+1]], of[m.Idx[t+2]]
		if a == b || b == c || a == c {
			continue
		}
		out.Idx = append(out.Idx, uint32(a), uint32(b), uint32(c))
		if m.TriMat != nil {
			out.TriMat = append(out.TriMat, m.TriMat[t/3])
		}
	}
	return out
}
