// Package glb reads binary glTF 2.0 files: plain and KHR_draco_mesh_compression
// triangle primitives, node transforms, embedded images. It reads what Rodin
// GLBs and the web viewer's own assets use; other glTF features (sparse
// accessors, external buffers, morph targets, skins) are not read.
package glb

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/sairaph/rodin3d-mcp/internal/draco"
)

// Doc is the subset of the glTF JSON the reader uses.
type Doc struct {
	Asset struct {
		Generator string `json:"generator"`
	} `json:"asset"`
	ExtensionsUsed []string `json:"extensionsUsed"`
	Scenes         []struct {
		Nodes []int `json:"nodes"`
	} `json:"scenes"`
	Scene  *int   `json:"scene"`
	Nodes  []Node `json:"nodes"`
	Meshes []struct {
		Primitives []Primitive `json:"primitives"`
	} `json:"meshes"`
	Accessors []struct {
		BufferView    *int      `json:"bufferView"`
		ByteOffset    int       `json:"byteOffset"`
		ComponentType int       `json:"componentType"`
		Normalized    bool      `json:"normalized"`
		Count         int       `json:"count"`
		Type          string    `json:"type"`
		Min           []float64 `json:"min"`
		Max           []float64 `json:"max"`
	} `json:"accessors"`
	BufferViews []struct {
		ByteOffset int `json:"byteOffset"`
		ByteLength int `json:"byteLength"`
		ByteStride int `json:"byteStride"`
	} `json:"bufferViews"`
	Images []struct {
		BufferView *int   `json:"bufferView"`
		MimeType   string `json:"mimeType"`
	} `json:"images"`
}

// Node is a scene node.
type Node struct {
	Mesh        *int      `json:"mesh"`
	Children    []int     `json:"children"`
	Matrix      []float64 `json:"matrix"`
	Translation []float64 `json:"translation"`
	Rotation    []float64 `json:"rotation"`
	Scale       []float64 `json:"scale"`
}

// Primitive is a mesh primitive.
type Primitive struct {
	Attributes map[string]int `json:"attributes"`
	Indices    *int           `json:"indices"`
	Mode       *int           `json:"mode"`
	Material   *int           `json:"material"`
	Extensions struct {
		Draco *struct {
			BufferView int            `json:"bufferView"`
			Attributes map[string]int `json:"attributes"`
		} `json:"KHR_draco_mesh_compression"`
	} `json:"extensions"`
}

// File is a parsed GLB.
type File struct {
	Doc Doc
	Bin []byte
}

// Parse splits a GLB into its JSON and BIN chunks.
func Parse(b []byte) (*File, error) {
	if len(b) < 20 || string(b[:4]) != "glTF" {
		return nil, errors.New("glb: not a binary glTF")
	}
	if v := binary.LittleEndian.Uint32(b[4:]); v != 2 {
		return nil, fmt.Errorf("glb: version %d", v)
	}
	f := &File{}
	off := 12
	for off+8 <= len(b) {
		n := int(binary.LittleEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		if off+8+n > len(b) {
			return nil, errors.New("glb: chunk past end")
		}
		data := b[off+8 : off+8+n]
		switch typ {
		case "JSON":
			if err := json.Unmarshal(data, &f.Doc); err != nil {
				return nil, fmt.Errorf("glb: json: %w", err)
			}
		case "BIN\x00":
			f.Bin = data
		}
		off += 8 + n
	}
	return f, nil
}

func (f *File) view(i int) ([]byte, error) {
	if i < 0 || i >= len(f.Doc.BufferViews) {
		return nil, fmt.Errorf("glb: bufferView %d missing", i)
	}
	v := f.Doc.BufferViews[i]
	if v.ByteOffset < 0 || v.ByteLength < 0 || v.ByteOffset+v.ByteLength > len(f.Bin) {
		return nil, fmt.Errorf("glb: bufferView %d out of range", i)
	}
	return f.Bin[v.ByteOffset : v.ByteOffset+v.ByteLength], nil
}

// Image returns the bytes of an embedded image.
func (f *File) Image(i int) ([]byte, string, error) {
	if i < 0 || i >= len(f.Doc.Images) || f.Doc.Images[i].BufferView == nil {
		return nil, "", fmt.Errorf("glb: image %d not embedded", i)
	}
	b, err := f.view(*f.Doc.Images[i].BufferView)
	return b, f.Doc.Images[i].MimeType, err
}

// Mesh is the merged triangle geometry of a scene, in world space. Normals and
// UVs are nil when any primitive lacks them.
type Mesh struct {
	Pos   [][3]float32
	Nrm   [][3]float32
	UV    [][2]float32
	Idx   []uint32
	Check []PrimCheck
}

// PrimCheck compares a decoded primitive with its glTF accessors.
type PrimCheck struct {
	Draco                         bool
	Points, Faces                 int // decoded
	AccessorPoints, AccessorFaces int // from the accessors
	HasNormal, HasUV              bool
}

var compCount = map[string]int{"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4, "MAT4": 16}

func compSize(t int) int {
	switch t {
	case 5120, 5121:
		return 1
	case 5122, 5123:
		return 2
	case 5125, 5126:
		return 4
	}
	return 0
}

// floats reads a plain accessor as floats (normalised integers scaled).
func (f *File) floats(ai int) ([]float32, int, error) {
	if ai < 0 || ai >= len(f.Doc.Accessors) {
		return nil, 0, fmt.Errorf("glb: accessor %d missing", ai)
	}
	a := f.Doc.Accessors[ai]
	nc := compCount[a.Type]
	cs := compSize(a.ComponentType)
	if a.BufferView == nil || nc == 0 || cs == 0 {
		return nil, 0, fmt.Errorf("glb: accessor %d unsupported", ai)
	}
	v, err := f.view(*a.BufferView)
	if err != nil {
		return nil, 0, err
	}
	stride := f.Doc.BufferViews[*a.BufferView].ByteStride
	if stride == 0 {
		stride = nc * cs
	}
	if a.Count < 0 || a.ByteOffset < 0 || (a.Count > 0 && a.ByteOffset+(a.Count-1)*stride+nc*cs > len(v)) {
		return nil, 0, fmt.Errorf("glb: accessor %d out of range", ai)
	}
	out := make([]float32, a.Count*nc)
	for i := 0; i < a.Count; i++ {
		base := a.ByteOffset + i*stride
		for c := 0; c < nc; c++ {
			p := v[base+c*cs:]
			var x float32
			switch a.ComponentType {
			case 5126:
				x = math.Float32frombits(binary.LittleEndian.Uint32(p))
			case 5125:
				x = float32(binary.LittleEndian.Uint32(p))
			case 5123:
				x = float32(binary.LittleEndian.Uint16(p))
				if a.Normalized {
					x /= 65535
				}
			case 5122:
				x = float32(int16(binary.LittleEndian.Uint16(p)))
				if a.Normalized {
					x = float32(math.Max(float64(x)/32767, -1))
				}
			case 5121:
				x = float32(p[0])
				if a.Normalized {
					x /= 255
				}
			case 5120:
				x = float32(int8(p[0]))
				if a.Normalized {
					x = float32(math.Max(float64(x)/127, -1))
				}
			}
			out[i*nc+c] = x
		}
	}
	return out, nc, nil
}

// indices reads a plain index accessor (unsigned byte, short or int scalars).
func (f *File) indices(ai int) ([]uint32, error) {
	if ai < 0 || ai >= len(f.Doc.Accessors) {
		return nil, fmt.Errorf("glb: accessor %d missing", ai)
	}
	a := f.Doc.Accessors[ai]
	cs := compSize(a.ComponentType)
	if a.BufferView == nil || a.Type != "SCALAR" || (a.ComponentType != 5121 && a.ComponentType != 5123 && a.ComponentType != 5125) {
		return nil, fmt.Errorf("glb: index accessor %d unsupported", ai)
	}
	v, err := f.view(*a.BufferView)
	if err != nil {
		return nil, err
	}
	stride := f.Doc.BufferViews[*a.BufferView].ByteStride
	if stride == 0 {
		stride = cs
	}
	if a.Count < 0 || a.ByteOffset < 0 || (a.Count > 0 && a.ByteOffset+(a.Count-1)*stride+cs > len(v)) {
		return nil, fmt.Errorf("glb: accessor %d out of range", ai)
	}
	out := make([]uint32, a.Count)
	for i := range out {
		p := v[a.ByteOffset+i*stride:]
		switch a.ComponentType {
		case 5125:
			out[i] = binary.LittleEndian.Uint32(p)
		case 5123:
			out[i] = uint32(binary.LittleEndian.Uint16(p))
		default:
			out[i] = uint32(p[0])
		}
	}
	return out, nil
}

// LoadMesh flattens the default scene into one world-space mesh. Draco
// primitives are decoded with dec (nil: a Draco primitive is an error), and a
// decoded primitive whose point or face count differs from its accessors is
// an error too.
func (f *File) LoadMesh(ctx context.Context, dec *draco.Decoder) (*Mesh, error) {
	m := &Mesh{}
	allN, allUV := true, true
	type prim struct {
		pos, nrm, uv []float32
		idx          []uint32
	}
	var prims []prim
	var mats [][16]float64
	var walk func(n int, parent [16]float64, depth int) error
	walk = func(n int, parent [16]float64, depth int) error {
		if n < 0 || n >= len(f.Doc.Nodes) || depth > 64 {
			return fmt.Errorf("glb: bad node %d", n)
		}
		nd := f.Doc.Nodes[n]
		world := mul(parent, local(nd))
		if nd.Mesh != nil {
			if *nd.Mesh < 0 || *nd.Mesh >= len(f.Doc.Meshes) {
				return fmt.Errorf("glb: bad mesh %d", *nd.Mesh)
			}
			for _, p := range f.Doc.Meshes[*nd.Mesh].Primitives {
				if p.Mode != nil && *p.Mode != 4 {
					continue // only triangle lists
				}
				var pr prim
				chk := PrimCheck{}
				if pi, ok := p.Attributes["POSITION"]; ok && pi < len(f.Doc.Accessors) {
					chk.AccessorPoints = f.Doc.Accessors[pi].Count
				}
				if p.Indices != nil && *p.Indices < len(f.Doc.Accessors) {
					chk.AccessorFaces = f.Doc.Accessors[*p.Indices].Count / 3
				}
				if d := p.Extensions.Draco; d != nil {
					if dec == nil {
						return errors.New("glb: a Draco primitive needs a decoder")
					}
					buf, err := f.view(d.BufferView)
					if err != nil {
						return err
					}
					g, err := dec.Decode(ctx, buf)
					if err != nil {
						return err
					}
					if !g.Mesh {
						return errors.New("glb: a Draco primitive decoded to a point cloud")
					}
					if (chk.AccessorPoints > 0 && g.Points != chk.AccessorPoints) || (chk.AccessorFaces > 0 && g.Faces != chk.AccessorFaces) {
						return fmt.Errorf("glb: a Draco primitive decoded to %d points and %d faces, its accessors say %d and %d",
							g.Points, g.Faces, chk.AccessorPoints, chk.AccessorFaces)
					}
					chk.Draco = true
					get := func(name string, want int) []float32 {
						id, ok := d.Attributes[name]
						if !ok {
							return nil
						}
						a := g.ByUniqueID(uint32(id))
						if a == nil || a.Components != want {
							return nil
						}
						return a.Data
					}
					pr.pos, pr.nrm, pr.uv, pr.idx = get("POSITION", 3), get("NORMAL", 3), get("TEXCOORD_0", 2), g.Indices
					chk.Points, chk.Faces = g.Points, g.Faces
				} else {
					if pi, ok := p.Attributes["POSITION"]; ok {
						v, nc, err := f.floats(pi)
						if err != nil {
							return err
						}
						if nc != 3 {
							return errors.New("glb: POSITION is not VEC3")
						}
						pr.pos = v
					}
					if ni, ok := p.Attributes["NORMAL"]; ok {
						if v, nc, err := f.floats(ni); err == nil && nc == 3 {
							pr.nrm = v
						}
					}
					if ti, ok := p.Attributes["TEXCOORD_0"]; ok {
						if v, nc, err := f.floats(ti); err == nil && nc == 2 {
							pr.uv = v
						}
					}
					if p.Indices != nil {
						ix, err := f.indices(*p.Indices)
						if err != nil {
							return err
						}
						pr.idx = ix
					} else {
						pr.idx = make([]uint32, len(pr.pos)/3)
						for i := range pr.idx {
							pr.idx[i] = uint32(i)
						}
					}
					chk.Points, chk.Faces = len(pr.pos)/3, len(pr.idx)/3
				}
				if pr.pos == nil {
					return errors.New("glb: primitive without POSITION")
				}
				if len(pr.idx)%3 != 0 {
					return errors.New("glb: triangle list length is not a multiple of 3")
				}
				// An attribute that does not cover every point is dropped.
				if len(pr.nrm) != len(pr.pos) {
					pr.nrm = nil
				}
				if len(pr.uv) != len(pr.pos)/3*2 {
					pr.uv = nil
				}
				allN = allN && pr.nrm != nil
				allUV = allUV && pr.uv != nil
				chk.HasNormal, chk.HasUV = pr.nrm != nil, pr.uv != nil
				m.Check = append(m.Check, chk)
				prims = append(prims, pr)
				mats = append(mats, world)
			}
		}
		for _, c := range nd.Children {
			if err := walk(c, world, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	roots := []int{}
	if len(f.Doc.Scenes) > 0 {
		s := 0
		if f.Doc.Scene != nil && *f.Doc.Scene < len(f.Doc.Scenes) {
			s = *f.Doc.Scene
		}
		roots = f.Doc.Scenes[s].Nodes
	}
	for _, r := range roots {
		if err := walk(r, identity(), 0); err != nil {
			return nil, err
		}
	}
	for k, pr := range prims {
		base := uint32(len(m.Pos))
		w := mats[k]
		nm := normalMatrix(w)
		n := len(pr.pos) / 3
		for i := 0; i < n; i++ {
			m.Pos = append(m.Pos, xform(w, pr.pos[i*3:i*3+3], 1))
			if allN {
				v := xform(nm, pr.nrm[i*3:i*3+3], 0)
				m.Nrm = append(m.Nrm, normalize(v))
			}
			if allUV {
				m.UV = append(m.UV, [2]float32{pr.uv[i*2], pr.uv[i*2+1]})
			}
		}
		for _, ix := range pr.idx {
			if int(ix) >= n {
				return nil, errors.New("glb: index out of range")
			}
			m.Idx = append(m.Idx, base+ix)
		}
	}
	if !allN {
		m.Nrm = nil
	}
	if !allUV {
		m.UV = nil
	}
	return m, nil
}

func identity() [16]float64 { return [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }

// local is the node's column-major matrix.
func local(n Node) [16]float64 {
	if len(n.Matrix) == 16 {
		var m [16]float64
		copy(m[:], n.Matrix)
		return m
	}
	t := [3]float64{}
	if len(n.Translation) == 3 {
		copy(t[:], n.Translation)
	}
	q := [4]float64{0, 0, 0, 1}
	if len(n.Rotation) == 4 {
		copy(q[:], n.Rotation)
	}
	s := [3]float64{1, 1, 1}
	if len(n.Scale) == 3 {
		copy(s[:], n.Scale)
	}
	x, y, z, w := q[0], q[1], q[2], q[3]
	r := [9]float64{
		1 - 2*(y*y+z*z), 2 * (x*y + z*w), 2 * (x*z - y*w),
		2 * (x*y - z*w), 1 - 2*(x*x+z*z), 2 * (y*z + x*w),
		2 * (x*z + y*w), 2 * (y*z - x*w), 1 - 2*(x*x+y*y),
	}
	return [16]float64{
		r[0] * s[0], r[1] * s[0], r[2] * s[0], 0,
		r[3] * s[1], r[4] * s[1], r[5] * s[1], 0,
		r[6] * s[2], r[7] * s[2], r[8] * s[2], 0,
		t[0], t[1], t[2], 1,
	}
}

func mul(a, b [16]float64) [16]float64 {
	var c [16]float64
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			var s float64
			for k := 0; k < 4; k++ {
				s += a[k*4+row] * b[col*4+k]
			}
			c[col*4+row] = s
		}
	}
	return c
}

func xform(m [16]float64, v []float32, w float64) [3]float32 {
	x, y, z := float64(v[0]), float64(v[1]), float64(v[2])
	return [3]float32{
		float32(m[0]*x + m[4]*y + m[8]*z + m[12]*w),
		float32(m[1]*x + m[5]*y + m[9]*z + m[13]*w),
		float32(m[2]*x + m[6]*y + m[10]*z + m[14]*w),
	}
}

// normalMatrix is the inverse transpose of the upper 3x3 (as a 4x4 without
// translation).
func normalMatrix(m [16]float64) [16]float64 {
	a := [9]float64{m[0], m[1], m[2], m[4], m[5], m[6], m[8], m[9], m[10]} // columns
	// a is column-major 3x3: a[c*3+r]
	det := a[0]*(a[4]*a[8]-a[7]*a[5]) - a[3]*(a[1]*a[8]-a[7]*a[2]) + a[6]*(a[1]*a[5]-a[4]*a[2])
	if det == 0 {
		return identity()
	}
	// cofactor matrix = inverse transpose * det
	cof := func(r, c int) float64 {
		var rr, cc []int
		for i := 0; i < 3; i++ {
			if i != r {
				rr = append(rr, i)
			}
			if i != c {
				cc = append(cc, i)
			}
		}
		v := a[cc[0]*3+rr[0]]*a[cc[1]*3+rr[1]] - a[cc[1]*3+rr[0]]*a[cc[0]*3+rr[1]]
		if (r+c)%2 == 1 {
			v = -v
		}
		return v / det
	}
	var n [16]float64
	for c := 0; c < 3; c++ {
		for r := 0; r < 3; r++ {
			n[c*4+r] = cof(r, c)
		}
	}
	n[15] = 1
	return n
}

func normalize(v [3]float32) [3]float32 {
	l := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])))
	if l == 0 {
		return v
	}
	return [3]float32{v[0] / l, v[1] / l, v[2] / l}
}
