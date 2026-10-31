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
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
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
// its primitives have any) and what of the file is not drawn.
type Model struct {
	Mesh *render3d.Mesh
	// Notes name the features of the file the renderer draws without (an
	// alpha mode, a material extension, a texture that would not decode).
	Notes []string
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

// Load reads the model file at path.
func Load(ctx context.Context, path string, dec Decoders) (*Model, error) {
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

func loadGLTF(ctx context.Context, data []byte, path string, text bool, dec Decoders) (*Model, error) {
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
				if dec.Draco == nil {
					return nil, errors.New("the file is Draco compressed")
				}
				if d, err = dec.Draco(); err != nil {
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
	if err := m.materials(ctx, f, gm, dec); err != nil {
		return nil, err
	}
	return m, nil
}

// readMaterialExtensions are the material extensions the renderer draws.
var readMaterialExtensions = []string{"KHR_materials_emissive_strength", "KHR_materials_unlit"}

// materials gives m's mesh the materials of the file's primitives (none
// when no primitive has one: the file draws in the untextured modes only)
// and notes what of them is not drawn. Each image is decoded once; the
// textures that sample it share its pixels.
func (m *Model) materials(ctx context.Context, f *glb.File, gm *glb.Mesh, dec Decoders) error {
	if !slices.ContainsFunc(gm.Parts, func(p glb.Part) bool { return p.Material >= 0 }) {
		return nil
	}
	note := func(format string, a ...any) {
		if s := fmt.Sprintf(format, a...); !slices.Contains(m.Notes, s) {
			m.Notes = append(m.Notes, s)
		}
	}
	for _, gmesh := range f.Doc.Meshes {
		for _, p := range gmesh.Primitives {
			if _, ok := p.Attributes["COLOR_0"]; ok && (p.Mode == nil || *p.Mode == 4) {
				note("vertex colours (COLOR_0)")
			}
		}
	}
	type picture struct {
		w, h int
		pix  []uint8
		err  error
	}
	pictures := map[int]*picture{}
	decode := func(i int) *picture {
		if p, ok := pictures[i]; ok {
			return p
		}
		p := &picture{}
		pictures[i] = p
		b, err := f.ImageData(i)
		switch {
		case err != nil:
			p.err = err
		case basisu.IsKTX2(b):
			if dec.KTX2 == nil {
				p.err = errors.New("no KTX2 transcoder")
				break
			}
			var kd *basisu.Decoder
			if kd, p.err = dec.KTX2(); p.err != nil {
				break
			}
			var img *basisu.Image
			if img, p.err = kd.Decode(ctx, b); p.err == nil {
				if p.err = render3d.CheckTextureSize(img.W, img.H); p.err == nil {
					p.w, p.h, p.pix = img.W, img.H, img.Pix
				}
			}
		default:
			var t *render3d.Texture
			if t, p.err = render3d.DecodeTexture(b, false); p.err == nil {
				p.w, p.h, p.pix = t.W, t.H, t.Pix
			}
		}
		return p
	}
	textures := map[[2]int]*render3d.Texture{} // image, sRGB
	wrap := func(mode int) render3d.Wrap {
		switch mode {
		case glb.WrapClampToEdge:
			return render3d.WrapClamp
		case glb.WrapMirroredRepeat:
			return render3d.WrapMirror
		}
		return render3d.WrapRepeat
	}
	ref := func(r *glb.TextureRef, srgb bool) *render3d.TexRef {
		if r == nil {
			return nil
		}
		if r.Index < 0 || r.Index >= len(f.Doc.Textures) {
			note("texture %d (missing)", r.Index)
			return nil
		}
		if m.Mesh.UV == nil {
			note("textures (the mesh has no texture coordinates)")
			return nil
		}
		if set := r.UVSet(); set != 0 {
			note("textures on TEXCOORD_%d", set)
			return nil
		}
		gt := f.Doc.Textures[r.Index]
		var tex *render3d.Texture
		var errs []string
		for _, src := range gt.Sources() {
			p := decode(src)
			if p.err != nil {
				errs = append(errs, fmt.Sprintf("image %d: %v", src, p.err))
				continue
			}
			key := [2]int{src, 0}
			if srgb {
				key[1] = 1
			}
			if tex = textures[key]; tex == nil {
				tex = &render3d.Texture{W: p.w, H: p.h, Pix: p.pix, SRGB: srgb}
				textures[key] = tex
			}
			break
		}
		if tex == nil {
			switch {
			case len(errs) > 0:
				note("texture %d (%s)", r.Index, strings.Join(errs, "; "))
			case gt.Extensions.AVIF != nil:
				note("EXT_texture_avif textures")
			default:
				note("texture %d (no image)", r.Index)
			}
			return nil
		}
		out := &render3d.TexRef{Tex: tex}
		if gt.Sampler != nil && *gt.Sampler >= 0 && *gt.Sampler < len(f.Doc.Samplers) {
			s := f.Doc.Samplers[*gt.Sampler]
			out.WrapU, out.WrapV = wrap(s.WrapS), wrap(s.WrapT)
		}
		if t := r.Extensions.Transform; t != nil {
			off, scale := [2]float64{}, [2]float64{1, 1}
			if len(t.Offset) == 2 {
				copy(off[:], t.Offset)
			}
			if len(t.Scale) == 2 {
				copy(scale[:], t.Scale)
			}
			out.Transform = render3d.NewUVTransform(off, t.Rotation, scale)
		}
		return out
	}
	byIndex := map[int]int{} // glTF material (-1: the default) -> m.Mesh.Mats
	tri := make([]uint16, len(gm.Idx)/3)
	for _, part := range gm.Parts {
		k, ok := byIndex[part.Material]
		if !ok {
			if len(m.Mesh.Mats) > math.MaxUint16 {
				return fmt.Errorf("the file has more than %d materials", math.MaxUint16+1)
			}
			k = len(m.Mesh.Mats)
			byIndex[part.Material] = k
			mt := render3d.DefaultMaterial()
			if part.Material >= 0 {
				gmt := f.Doc.Materials[part.Material]
				mt.Name = gmt.Name
				mt.BaseColor, mt.Metallic, mt.Roughness, mt.Emissive = gmt.Factors()
				mt.Unlit = gmt.Unlit()
				mt.Base = ref(gmt.PBR.BaseColorTexture, true)
				mt.MetalRough = ref(gmt.PBR.MetallicRoughnessTexture, false)
				mt.Emission = ref(gmt.EmissiveTexture, true)
				if r := gmt.NormalTexture; r != nil {
					mt.Normal = ref(r, false)
					if r.Scale != nil {
						mt.NormalScale = *r.Scale
					}
				}
				if r := gmt.OcclusionTexture; r != nil {
					mt.Occlusion = ref(r, false)
					if r.Strength != nil {
						mt.OcclusionStrength = *r.Strength
					}
				}
				if gmt.AlphaMode == "MASK" || gmt.AlphaMode == "BLEND" {
					note("alpha mode %s (drawn opaque)", gmt.AlphaMode)
				}
				for _, e := range slices.Sorted(maps.Keys(gmt.Extensions)) {
					if !slices.Contains(readMaterialExtensions, e) {
						note("%s", e)
					}
				}
			}
			m.Mesh.Mats = append(m.Mesh.Mats, mt)
		}
		for t := part.First; t < part.First+part.Count; t++ {
			tri[t] = uint16(k)
		}
	}
	if len(m.Mesh.Mats) > 1 {
		m.Mesh.TriMat = tri
	}
	return nil
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
