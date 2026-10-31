// Package modelfile reads local model files for the viewer: GLB and .gltf
// (with their materials and textures: PNG, JPEG, WebP and KTX2 images, and
// Draco and meshopt compressed geometry), Wavefront OBJ and STL (binary and
// ASCII), into render3d meshes. Positions are used as stored: glTF and OBJ
// are Y up, so an STL drawn for a Z-up printer lies on its back.
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

	"github.com/sairaph/rodin3d-mcp/internal/basisu"
	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// Extensions lists the file types Load reads.
var Extensions = []string{".glb", ".gltf", ".obj", ".stl"}

// Model is a loaded file: its mesh (a glTF file's with its materials, when
// its primitives have any) and what of the file is not drawn. A Model is
// not changed after Load: WithTextures and WithoutTextures return new ones
// that share the geometry.
type Model struct {
	Mesh *render3d.Mesh
	// Notes name the features of the file the renderer draws without (an
	// alpha mode, a material extension, a texture that would not decode).
	Notes []string
	// Textures are the pictures the materials' maps sample, each once.
	Textures []TextureInfo
	// Pending is true when the materials have maps whose pictures are not
	// decoded (Load without textures, WithoutTextures): they draw with their
	// factors only until WithTextures.
	Pending bool

	path  string
	parts []glb.Part
}

// TextureInfo is a picture of a model file's materials: its name in the
// textures panel (base_color, metallic_roughness, normal, occlusion or
// emissive, as <material>/<map> when the file has several materials; a
// material without a name, or with another's, is "material <n>") and the
// glTF texture that holds it (Picture reads it).
type TextureInfo struct {
	Name    string
	Texture int
}

// Note is the notes as one sentence, or "" without any.
func (m *Model) Note() string {
	if len(m.Notes) == 0 {
		return ""
	}
	return "drawn without " + strings.Join(m.Notes, "; ")
}

// Decoders start the decoders a glTF file may need, on first use: Draco
// for compressed primitives, KTX2 for KHR_texture_basisu images. A nil one
// makes a Draco file an error and a KTX2 texture a note.
type Decoders struct {
	Draco func() (*draco.Decoder, error)
	KTX2  func() (*basisu.Decoder, error)
}

// Load reads the model file at path. textures decodes the pictures of a
// glTF file's material maps (once per image); without it the materials
// have their factors only and Pending says whether maps wait (the modes
// that sample no maps need none).
func Load(ctx context.Context, path string, dec Decoders, textures bool) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m *Model
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".glb", ".gltf":
		m, err = loadGLTF(ctx, data, path, dec, textures)
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
