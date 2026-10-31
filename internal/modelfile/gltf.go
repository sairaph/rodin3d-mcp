package modelfile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/basisu"
	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

func parseGLTF(data []byte, path string) (*glb.File, error) {
	if strings.EqualFold(filepath.Ext(path), ".gltf") {
		return glb.ParseGLTF(data, filepath.Dir(path))
	}
	return glb.Parse(data)
}

// reparse reads the glTF file at path again (for its textures).
func reparse(path string) (*glb.File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseGLTF(data, path)
}

func loadGLTF(ctx context.Context, data []byte, path string, dec Decoders, textures bool) (*Model, error) {
	f, err := parseGLTF(data, path)
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
	m := &Model{Mesh: &render3d.Mesh{Pos: gm.Pos, Nrm: gm.Nrm, UV: gm.UV, Idx: gm.Idx}, path: path, parts: gm.Parts}
	set, err := build(ctx, f, gm.Parts, m.Mesh.UV != nil, dec, textures)
	if err != nil {
		return nil, err
	}
	m.Mesh.Mats, m.Mesh.TriMat = set.mats, set.triMat(len(gm.Idx)/3)
	m.Notes, m.Textures, m.Pending = set.notes, set.textures, set.pending
	return m, nil
}

// WithTextures returns m with the pictures of its material maps decoded
// (each image once; the file is read again), or m itself when none wait.
// The geometry is shared.
func (m *Model) WithTextures(ctx context.Context, dec Decoders) (*Model, error) {
	if !m.Pending {
		return m, nil
	}
	f, err := reparse(m.path)
	if err != nil {
		return nil, err
	}
	set, err := build(ctx, f, m.parts, m.Mesh.UV != nil, dec, true)
	if err != nil {
		return nil, err
	}
	if len(set.mats) != len(m.Mesh.Mats) {
		return nil, fmt.Errorf("%s changed since it was read; show it again", filepath.Base(m.path))
	}
	return &Model{Mesh: withMats(m.Mesh, set.mats), Notes: set.notes, Textures: set.textures, path: m.path, parts: m.parts}, nil
}

// WithoutTextures returns m with its material maps released (their factors
// stay; WithTextures decodes the maps again), or m itself when it holds
// none.
func (m *Model) WithoutTextures() *Model {
	if m.Pending || len(m.Textures) == 0 {
		return m
	}
	bare := make([]*render3d.Material, len(m.Mesh.Mats))
	for i, mt := range m.Mesh.Mats {
		c := *mt
		c.Base, c.MetalRough, c.Normal, c.Occlusion, c.Emission = nil, nil, nil, nil, nil
		bare[i] = &c
	}
	return &Model{Mesh: withMats(m.Mesh, bare), Notes: m.Notes, Textures: m.Textures, Pending: true, path: m.path, parts: m.parts}
}

// withMats is a copy of mesh (sharing its geometry) with materials mats,
// indexed as mesh's.
func withMats(mesh *render3d.Mesh, mats []*render3d.Material) *render3d.Mesh {
	c := *mesh
	c.Mats = mats
	return &c
}

// Picture decodes the picture of glTF texture t of the file at path, for
// the textures panel (used as stored, no colour space).
func Picture(ctx context.Context, path string, dec Decoders, t int) (*render3d.Texture, error) {
	f, err := reparse(path)
	if err != nil {
		return nil, err
	}
	if t < 0 || t >= len(f.Doc.Textures) {
		return nil, fmt.Errorf("texture %d missing", t)
	}
	var errs []string
	for _, src := range f.Doc.Textures[t].Sources() {
		p := decodeImage(ctx, f, src, dec)
		if p.err == nil {
			return &render3d.Texture{W: p.w, H: p.h, Pix: p.pix}, nil
		}
		errs = append(errs, fmt.Sprintf("image %d: %v", src, p.err))
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("texture %d has no image this reader decodes", t)
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

// picture is a decoded image: RGBA8 pixels, or why it did not decode.
type picture struct {
	w, h int
	pix  []uint8
	err  error
}

// decodeImage decodes image i of f by its bytes: KTX2 with the
// transcoder, PNG, JPEG and WebP in Go.
func decodeImage(ctx context.Context, f *glb.File, i int, dec Decoders) *picture {
	p := &picture{}
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

// readMaterialExtensions are the material extensions the renderer draws.
var readMaterialExtensions = []string{"KHR_materials_emissive_strength", "KHR_materials_unlit"}

// materialSet is the materials of a file's primitives.
type materialSet struct {
	mats     []*render3d.Material
	byPart   []uint16 // material of each part
	parts    []glb.Part
	notes    []string
	textures []TextureInfo
	pending  bool // maps that were not decoded
}

// triMat is the material index of each of tris triangles (nil with one
// material).
func (s materialSet) triMat(tris int) []uint16 {
	if len(s.mats) <= 1 {
		return nil
	}
	out := make([]uint16, tris)
	for i, part := range s.parts {
		for t := part.First; t < part.First+part.Count; t++ {
			out[t] = s.byPart[i]
		}
	}
	return out
}

// build makes the materials of the parts (none when no part names one: the
// file draws in the untextured modes only) and notes what of them is not
// drawn. With decode the maps' pictures are decoded, each image once (the
// textures that sample it share its pixels); without, the materials have
// no maps and pending says whether any wait.
func build(ctx context.Context, f *glb.File, parts []glb.Part, hasUV bool, dec Decoders, decode bool) (materialSet, error) {
	set := materialSet{parts: parts}
	if !slices.ContainsFunc(parts, func(p glb.Part) bool { return p.Material >= 0 }) {
		return set, nil
	}
	note := func(format string, a ...any) {
		if s := fmt.Sprintf(format, a...); !slices.Contains(set.notes, s) {
			set.notes = append(set.notes, s)
		}
	}
	for _, gmesh := range f.Doc.Meshes {
		for _, p := range gmesh.Primitives {
			if _, ok := p.Attributes["COLOR_0"]; ok && (p.Mode == nil || *p.Mode == 4) {
				note("vertex colours (COLOR_0)")
			}
		}
	}
	pictures := map[int]*picture{}
	textures := map[[2]int]*render3d.Texture{} // image, sRGB
	type mapUse struct {
		mat     int
		name    string
		texture int
		image   int // its first image: the panel lists each once
	}
	var uses []mapUse
	wrap := func(mode int) render3d.Wrap {
		switch mode {
		case glb.WrapClampToEdge:
			return render3d.WrapClamp
		case glb.WrapMirroredRepeat:
			return render3d.WrapMirror
		}
		return render3d.WrapRepeat
	}
	ref := func(r *glb.TextureRef, srgb bool, mat int, name string) *render3d.TexRef {
		if r == nil {
			return nil
		}
		if r.Index < 0 || r.Index >= len(f.Doc.Textures) {
			note("texture %d (missing)", r.Index)
			return nil
		}
		if !hasUV {
			note("textures (the mesh has no texture coordinates)")
			return nil
		}
		if s := r.UVSet(); s != 0 {
			note("textures on TEXCOORD_%d", s)
			return nil
		}
		gt := f.Doc.Textures[r.Index]
		sources := gt.Sources()
		if len(sources) == 0 {
			if gt.Extensions.AVIF != nil {
				note("EXT_texture_avif textures")
			} else {
				note("texture %d (no image)", r.Index)
			}
			return nil
		}
		uses = append(uses, mapUse{mat: mat, name: name, texture: r.Index, image: sources[0]})
		if !decode {
			set.pending = true
			return nil
		}
		var tex *render3d.Texture
		var errs []string
		for _, src := range sources {
			p, ok := pictures[src]
			if !ok {
				p = decodeImage(ctx, f, src, dec)
				pictures[src] = p
			}
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
			note("texture %d (%s)", r.Index, strings.Join(errs, "; "))
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
	byIndex := map[int]int{} // glTF material (-1: the default) -> set.mats
	for _, part := range parts {
		if part.Material >= len(f.Doc.Materials) {
			return set, errors.New("the file changed since it was read; show it again")
		}
		k, ok := byIndex[part.Material]
		if !ok {
			if len(set.mats) > math.MaxUint16 {
				return set, fmt.Errorf("the file has more than %d materials", math.MaxUint16+1)
			}
			k = len(set.mats)
			byIndex[part.Material] = k
			mt := render3d.DefaultMaterial()
			if part.Material >= 0 {
				gmt := f.Doc.Materials[part.Material]
				mt.Name = gmt.Name
				mt.BaseColor, mt.Metallic, mt.Roughness, mt.Emissive = gmt.Factors()
				mt.Unlit = gmt.Unlit()
				mt.Base = ref(gmt.PBR.BaseColorTexture, true, k, "base_color")
				mt.MetalRough = ref(gmt.PBR.MetallicRoughnessTexture, false, k, "metallic_roughness")
				if r := gmt.NormalTexture; r != nil {
					mt.Normal = ref(r, false, k, "normal")
					if r.Scale != nil {
						mt.NormalScale = *r.Scale
					}
				}
				if r := gmt.OcclusionTexture; r != nil {
					mt.Occlusion = ref(r, false, k, "occlusion")
					if r.Strength != nil {
						mt.OcclusionStrength = *r.Strength
					}
				}
				mt.Emission = ref(gmt.EmissiveTexture, true, k, "emissive")
				if gmt.AlphaMode == "MASK" || gmt.AlphaMode == "BLEND" {
					note("alpha mode %s (drawn opaque)", gmt.AlphaMode)
				}
				for _, e := range slices.Sorted(maps.Keys(gmt.Extensions)) {
					if !slices.Contains(readMaterialExtensions, e) {
						note("%s", e)
					}
				}
			}
			set.mats = append(set.mats, mt)
		}
		set.byPart = append(set.byPart, uint16(k))
	}
	// The textures panel's names, each image once.
	seen := map[int]bool{}
	for _, u := range uses {
		if seen[u.image] {
			continue
		}
		seen[u.image] = true
		name := u.name
		if len(set.mats) > 1 {
			mn := set.mats[u.mat].Name
			if mn == "" || slices.ContainsFunc(set.mats, func(o *render3d.Material) bool { return o != set.mats[u.mat] && o.Name == mn }) {
				mn = fmt.Sprintf("material %d", u.mat)
			}
			name = mn + "/" + name
		}
		set.textures = append(set.textures, TextureInfo{Name: name, Texture: u.texture})
	}
	return set, nil
}
