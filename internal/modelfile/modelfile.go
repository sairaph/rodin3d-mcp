// Package modelfile reads local model files for the viewer: GLB and .gltf
// (with the textures of a single material), Wavefront OBJ and STL (binary
// and ASCII), into render3d meshes. Positions are used as stored: glTF and
// OBJ are Y up, so an STL drawn for a Z-up printer lies on its back.
package modelfile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// Extensions lists the file types Load reads.
var Extensions = []string{".glb", ".gltf", ".obj", ".stl"}

// Model is a loaded file: its mesh and, for glTF with one textured
// material, its maps.
type Model struct {
	Mesh *render3d.Mesh
	Maps *render3d.Maps // nil without textures
}

// Load reads the model file at path. dec decodes Draco primitives of glTF
// files (nil: such a file is an error).
func Load(ctx context.Context, path string, dec func() (*draco.Decoder, error)) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m *Model
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".glb", ".gltf":
		m, err = loadGLTF(ctx, data, path, ext == ".gltf", dec)
	case ".obj":
		var mesh *render3d.Mesh
		mesh, err = parseOBJ(data)
		m = &Model{Mesh: mesh}
	case ".stl":
		var mesh *render3d.Mesh
		mesh, err = parseSTL(data)
		m = &Model{Mesh: mesh}
	default:
		return nil, fmt.Errorf("%s is not a model file this viewer reads (%s)", filepath.Base(path), strings.Join(Extensions, ", "))
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := m.Mesh.Validate(); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return m, nil
}

func loadGLTF(ctx context.Context, data []byte, path string, text bool, dec func() (*draco.Decoder, error)) (*Model, error) {
	var f *glb.File
	var err error
	if text {
		f, err = glb.ParseGLTF(data, filepath.Dir(path))
	} else {
		f, err = glb.Parse(data)
	}
	if err != nil {
		return nil, err
	}
	var d *draco.Decoder
	for _, gm := range f.Doc.Meshes {
		for _, p := range gm.Primitives {
			if p.Extensions.Draco != nil && d == nil {
				if dec == nil {
					return nil, errors.New("the file is Draco compressed")
				}
				if d, err = dec(); err != nil {
					return nil, err
				}
			}
		}
	}
	gm, err := f.LoadMesh(ctx, d)
	if err != nil {
		return nil, err
	}
	m := &Model{Mesh: &render3d.Mesh{Pos: gm.Pos, Nrm: gm.Nrm, UV: gm.UV, Idx: gm.Idx}}
	if m.Mesh.UV == nil {
		return m, nil
	}
	base, mr, normal, ok := f.MaterialImages()
	if !ok || base < 0 {
		return m, nil
	}
	tex := func(i int, srgb bool) *render3d.Texture {
		if i < 0 {
			return nil
		}
		b, err := f.ImageData(i)
		if err != nil {
			return nil
		}
		t, err := render3d.DecodeTexture(b, srgb)
		if err != nil {
			return nil
		}
		return t
	}
	maps := &render3d.Maps{Diffuse: tex(base, true), PBR: tex(mr, false), Normal: tex(normal, false)}
	if maps.Diffuse != nil {
		m.Maps = maps
	}
	return m, nil
}

// parseOBJ reads the v, vt, vn and f records of an OBJ file; polygons are
// split into fans. A corner without a texture coordinate or normal drops
// that attribute for the whole mesh. OBJ texture coordinates have v up;
// render3d samples with v down (glTF), so v is flipped.
func parseOBJ(data []byte) (*render3d.Mesh, error) {
	var pos [][3]float32
	var uvs [][2]float32
	var nrms [][3]float32
	type corner struct{ v, t, n int }
	index := map[corner]uint32{}
	out := &render3d.Mesh{}
	var outUV [][2]float32
	var outN [][3]float32
	hasUV, hasN := true, true
	resolve := func(s string, n int) (int, error) {
		if s == "" {
			return -1, nil
		}
		i, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("bad index %q", s)
		}
		switch {
		case i > 0 && i <= n:
			return i - 1, nil
		case i < 0 && -i <= n:
			return n + i, nil
		}
		return 0, fmt.Errorf("index %d out of range", i)
	}
	floats := func(fields []string, n int) ([]float32, error) {
		if len(fields) < n {
			return nil, errors.New("too few numbers")
		}
		v := make([]float32, n)
		for i := 0; i < n; i++ {
			f, err := strconv.ParseFloat(fields[i], 32)
			if err != nil {
				return nil, err
			}
			v[i] = float32(f)
		}
		return v, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	line := 0
	for sc.Scan() {
		line++
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "v":
			v, err := floats(fields[1:], 3)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			pos = append(pos, [3]float32{v[0], v[1], v[2]})
		case "vt":
			v, err := floats(fields[1:], 2)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			uvs = append(uvs, [2]float32{v[0], 1 - v[1]})
		case "vn":
			v, err := floats(fields[1:], 3)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			nrms = append(nrms, [3]float32{v[0], v[1], v[2]})
		case "f":
			var face []uint32
			for _, f := range fields[1:] {
				parts := strings.Split(f, "/")
				var c corner
				var err error
				if c.v, err = resolve(parts[0], len(pos)); err != nil || c.v < 0 {
					return nil, fmt.Errorf("line %d: face corner %q: position missing or %v", line, f, err)
				}
				c.t, c.n = -1, -1
				if len(parts) > 1 {
					if c.t, err = resolve(parts[1], len(uvs)); err != nil {
						return nil, fmt.Errorf("line %d: %w", line, err)
					}
				}
				if len(parts) > 2 {
					if c.n, err = resolve(parts[2], len(nrms)); err != nil {
						return nil, fmt.Errorf("line %d: %w", line, err)
					}
				}
				if c.t < 0 {
					hasUV = false
				}
				if c.n < 0 {
					hasN = false
				}
				id, ok := index[c]
				if !ok {
					id = uint32(len(out.Pos))
					index[c] = id
					out.Pos = append(out.Pos, pos[c.v])
					uv := [2]float32{}
					if c.t >= 0 {
						uv = uvs[c.t]
					}
					outUV = append(outUV, uv)
					n := [3]float32{}
					if c.n >= 0 {
						n = nrms[c.n]
					}
					outN = append(outN, n)
				}
				face = append(face, id)
			}
			for k := 1; k+1 < len(face); k++ {
				out.Idx = append(out.Idx, face[0], face[k], face[k+1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if hasUV && len(outUV) > 0 {
		out.UV = outUV
	}
	if hasN && len(outN) > 0 {
		out.Nrm = outN
	}
	return out, nil
}

// parseSTL reads a binary STL (80-byte header, count, 50 bytes per
// triangle) or an ASCII one. Triangles keep their own corners (flat
// shading).
func parseSTL(data []byte) (*render3d.Mesh, error) {
	if len(data) >= 84 {
		n := int(binary.LittleEndian.Uint32(data[80:]))
		if 84+50*n == len(data) {
			m := &render3d.Mesh{Pos: make([][3]float32, 0, 3*n), Idx: make([]uint32, 0, 3*n)}
			for t := 0; t < n; t++ {
				o := 84 + 50*t + 12
				for c := 0; c < 3; c++ {
					var p [3]float32
					for a := 0; a < 3; a++ {
						p[a] = math.Float32frombits(binary.LittleEndian.Uint32(data[o+12*c+4*a:]))
					}
					m.Idx = append(m.Idx, uint32(len(m.Pos)))
					m.Pos = append(m.Pos, p)
				}
			}
			return m, nil
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("solid")) {
		return nil, errors.New("neither a binary STL (its size does not match its triangle count) nor an ASCII one")
	}
	m := &render3d.Mesh{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && fields[0] == "vertex" {
			var p [3]float32
			for a := 0; a < 3; a++ {
				f, err := strconv.ParseFloat(fields[1+a], 32)
				if err != nil {
					return nil, fmt.Errorf("bad vertex %q", sc.Text())
				}
				p[a] = float32(f)
			}
			m.Idx = append(m.Idx, uint32(len(m.Pos)))
			m.Pos = append(m.Pos, p)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(m.Idx)%3 != 0 {
		return nil, errors.New("an ASCII STL facet does not have three vertices")
	}
	return m, nil
}
