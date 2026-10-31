package modelfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/basisu"
	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// The fixtures in testdata are a unit cube with 16 x 16 textures in every
// variant the reader decodes (testdata/gen.mjs writes them). The base
// colour picture has four quadrants: red top left, green top right, blue
// bottom left, yellow bottom right.
var quadrants = [4][3]int{{220, 40, 40}, {40, 200, 60}, {40, 60, 220}, {230, 210, 40}}

var (
	decOnce sync.Once
	decs    Decoders
)

// decoders compiles the Draco and KTX2 modules once for all tests.
func decoders(t *testing.T) Decoders {
	t.Helper()
	decOnce.Do(func() {
		ctx := context.Background()
		d, err1 := draco.New(ctx, "")
		k, err2 := basisu.New(ctx, "")
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		decs = Decoders{Draco: func() (*draco.Decoder, error) { return d, nil }, KTX2: func() (*basisu.Decoder, error) { return k, nil }}
	})
	return decs
}

func load(t *testing.T, name string) *Model {
	t.Helper()
	m, err := Load(context.Background(), filepath.Join("testdata", name), decoders(t))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

// TestCubeVariants: every encoding loads to the cube of the plain PNG file:
// the same corners (up to quantisation), one material with its factors and
// maps, and the base colour picture (up to the codec's loss).
func TestCubeVariants(t *testing.T) {
	ref := load(t, "cube_png.glb")
	for _, tc := range []struct {
		file          string
		posTol        float64
		texTol        int
		maps          int // distinct images
		occlusionMaps bool
	}{
		{"cube_png.glb", 0, 0, 3, true},
		{"cube_webp_lossless.glb", 0, 0, 3, true},
		{"cube_webp_lossy.glb", 0, 24, 3, true},
		{"cube_draco_webp.glb", 1e-3, 0, 3, true},
		{"cube_meshopt.glb", 1e-3, 0, 3, true},
		{"cube_etc1s.glb", 0, 24, 3, true},
		{"cube_uastc.glb", 0, 8, 3, true},
	} {
		m := load(t, tc.file)
		mesh := m.Mesh
		if len(mesh.Idx) != 36 || len(mesh.Mats) != 1 || mesh.TriMat != nil || mesh.UV == nil || mesh.Nrm == nil {
			t.Errorf("%s: %d indices, %d materials, TriMat %v, UV %v, normals %v", tc.file, len(mesh.Idx), len(mesh.Mats), mesh.TriMat != nil, mesh.UV != nil, mesh.Nrm != nil)
			continue
		}
		if m.Note() != "" {
			t.Errorf("%s: note %q", tc.file, m.Note())
		}
		// Corners as a set: compressors reorder vertices.
		for _, p := range mesh.Pos {
			found := false
			for _, q := range ref.Mesh.Pos {
				if math.Abs(float64(p[0]-q[0])) <= tc.posTol && math.Abs(float64(p[1]-q[1])) <= tc.posTol && math.Abs(float64(p[2]-q[2])) <= tc.posTol {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: corner %v is not the reference cube's", tc.file, p)
				break
			}
		}
		mt := mesh.Mats[0]
		if mt.Metallic != 0.5 || mt.Roughness != 0.8 || mt.BaseColor != [4]float64{1, 1, 1, 1} || mt.NormalScale != 1 || mt.OcclusionStrength != 1 {
			t.Errorf("%s: factors %+v", tc.file, mt)
		}
		if mt.Base == nil || mt.MetalRough == nil || mt.Normal == nil || mt.Occlusion == nil || mt.Emission != nil {
			t.Errorf("%s: maps base %v, metal-rough %v, normal %v, occlusion %v, emission %v", tc.file,
				mt.Base != nil, mt.MetalRough != nil, mt.Normal != nil, mt.Occlusion != nil, mt.Emission != nil)
			continue
		}
		if !mt.Base.Tex.SRGB || mt.MetalRough.Tex.SRGB || mt.Normal.Tex.SRGB {
			t.Errorf("%s: colour spaces base %v, metal-rough %v, normal %v", tc.file, mt.Base.Tex.SRGB, mt.MetalRough.Tex.SRGB, mt.Normal.Tex.SRGB)
		}
		// The occlusion map is the metal-rough picture (an ORM texture),
		// decoded once.
		if &mt.Occlusion.Tex.Pix[0] != &mt.MetalRough.Tex.Pix[0] {
			t.Errorf("%s: the shared ORM picture was decoded twice", tc.file)
		}
		b := mt.Base.Tex
		if b.W != 16 || b.H != 16 {
			t.Errorf("%s: base colour %dx%d", tc.file, b.W, b.H)
			continue
		}
		for q, want := range quadrants {
			o := ((4+8*(q/2))*16 + 4 + 8*(q%2)) * 4
			for c := 0; c < 3; c++ {
				if d := int(b.Pix[o+c]) - want[c]; d < -tc.texTol || d > tc.texTol {
					t.Errorf("%s quadrant %d: %v, want %v", tc.file, q, b.Pix[o:o+3], want)
					break
				}
			}
		}
		// pbr draws it.
		if _, _, err := render3d.Render(mesh, nil, render3d.Options{Mode: render3d.ModePBR, Size: 32}); err != nil {
			t.Errorf("%s: pbr: %v", tc.file, err)
		}
	}
}

// TestMultiMaterial: two materials split the triangles; the second's
// factors, emissive strength, transform and wrap modes are read, its base
// texture shares the first's picture, and its clearcoat is noted.
func TestMultiMaterial(t *testing.T) {
	m := load(t, "cube_multi.glb")
	mesh := m.Mesh
	if len(mesh.Mats) != 2 || len(mesh.TriMat) != 12 {
		t.Fatalf("%d materials, %d material indices", len(mesh.Mats), len(mesh.TriMat))
	}
	if !slices.Equal(mesh.TriMat, []uint16{0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1}) {
		t.Errorf("TriMat %v", mesh.TriMat)
	}
	a, b := mesh.Mats[0], mesh.Mats[1]
	if a.Name != "first" || b.Name != "second" {
		t.Errorf("names %q, %q", a.Name, b.Name)
	}
	if b.BaseColor != [4]float64{1, 0.5, 0.5, 1} || b.Metallic != 0 || b.Roughness != 0.3 || b.Emissive != [3]float64{0.5, 0.25, 0} {
		t.Errorf("second's factors: base %v, metal %v, rough %v, emissive %v", b.BaseColor, b.Metallic, b.Roughness, b.Emissive)
	}
	if b.Base == nil || a.Base == nil || &b.Base.Tex.Pix[0] != &a.Base.Tex.Pix[0] {
		t.Fatal("the second's base texture does not share the first's picture")
	}
	if b.Base.WrapU != render3d.WrapClamp || b.Base.WrapV != render3d.WrapMirror || a.Base.WrapU != render3d.WrapRepeat {
		t.Errorf("wraps: second %v %v, first %v", b.Base.WrapU, b.Base.WrapV, a.Base.WrapU)
	}
	if b.Base.Transform == nil || *b.Base.Transform != (render3d.UVTransform{0.5, 0, 0.5, 0, 1, 0}) || a.Base.Transform != nil {
		t.Errorf("transforms: second %v, first %v", b.Base.Transform, a.Base.Transform)
	}
	if !slices.Equal(m.Notes, []string{"KHR_materials_clearcoat"}) {
		t.Errorf("notes %q", m.Notes)
	}
}

// TestShadedPack: Rodin's shaded pack layout (a black base, the picture as
// emission) loads as such, and the shaded mode draws the emission.
func TestShadedPack(t *testing.T) {
	m := load(t, "cube_shaded.glb")
	mt := m.Mesh.Mats[0]
	if mt.Base != nil || mt.Emission == nil || mt.BaseColor != [4]float64{0, 0, 0, 1} || mt.Emissive != [3]float64{1, 1, 1} || !mt.Emission.Tex.SRGB {
		t.Fatalf("material %+v", mt)
	}
	img, _, err := render3d.Render(m.Mesh, nil, render3d.Options{Mode: render3d.ModeShaded, View: render3d.ViewFront, Size: 64})
	if err != nil {
		t.Fatal(err)
	}
	// The front face (+Z) fills the middle: its top left quadrant is red.
	o := img.PixOffset(26, 26)
	if p := img.Pix[o : o+3]; !(p[0] > 150 && p[1] < 90 && p[2] < 90) {
		t.Errorf("shaded front face, top left: %v, want red", p)
	}
}

// rewrite returns the GLB file name with its JSON changed by edit.
func rewrite(t *testing.T, name string, edit func(doc map[string]any, bin []byte)) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint32(b[12:]))
	var doc map[string]any
	if err := json.Unmarshal(b[20:20+n], &doc); err != nil {
		t.Fatal(err)
	}
	bin := append([]byte(nil), b[20+n+8:]...)
	edit(doc, bin)
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var out bytes.Buffer
	out.WriteString("glTF")
	binary.Write(&out, binary.LittleEndian, []uint32{2, uint32(12 + 8 + len(js) + 8 + len(bin)), uint32(len(js))})
	out.WriteString("JSON")
	out.Write(js)
	binary.Write(&out, binary.LittleEndian, uint32(len(bin)))
	out.WriteString("BIN\x00")
	out.Write(bin)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// view is the byte range of buffer view i in the BIN chunk.
func view(doc map[string]any, i int) (int, int) {
	v := doc["bufferViews"].([]any)[i].(map[string]any)
	off, _ := v["byteOffset"].(float64)
	return int(off), int(v["byteLength"].(float64))
}

// TestRefusedAndNoted: a required extension that changes geometry is
// refused; malformed meshopt data is an error; a texture that does not
// decode, an alpha mode and vertex colours are notes, and the file draws.
func TestRefusedAndNoted(t *testing.T) {
	ctx := context.Background()
	p := rewrite(t, "cube_png.glb", func(doc map[string]any, _ []byte) {
		doc["extensionsUsed"] = []any{"EXT_mesh_gpu_instancing"}
		doc["extensionsRequired"] = []any{"EXT_mesh_gpu_instancing"}
	})
	if _, err := Load(ctx, p, decoders(t)); err == nil || !strings.Contains(err.Error(), "EXT_mesh_gpu_instancing") {
		t.Errorf("unknown required extension: %v", err)
	}
	p = rewrite(t, "cube_png.glb", func(doc map[string]any, _ []byte) {
		doc["extensionsRequired"] = []any{"KHR_materials_clearcoat"}
	})
	if _, err := Load(ctx, p, decoders(t)); err != nil {
		t.Errorf("a required material extension: %v", err)
	}
	// Corrupt the compressed vertex stream of cube_meshopt.
	p = rewrite(t, "cube_meshopt.glb", func(doc map[string]any, bin []byte) {
		v := doc["bufferViews"].([]any)[4].(map[string]any)["extensions"].(map[string]any)["EXT_meshopt_compression"].(map[string]any)
		off, n := int(v["byteOffset"].(float64)), int(v["byteLength"].(float64))
		for i := off + 1; i < off+n; i++ {
			bin[i] = 0xff
		}
	})
	if _, err := Load(ctx, p, decoders(t)); err == nil {
		t.Error("corrupt meshopt data: no error")
	}
	// Corrupt the base colour KTX2 picture; mark the material blended and
	// give the primitive vertex colours.
	p = rewrite(t, "cube_etc1s.glb", func(doc map[string]any, bin []byte) {
		img := doc["textures"].([]any)[0].(map[string]any)["extensions"].(map[string]any)["KHR_texture_basisu"].(map[string]any)["source"].(float64)
		bv := doc["images"].([]any)[int(img)].(map[string]any)["bufferView"].(float64)
		off, n := view(doc, int(bv))
		for i := off + 16; i < off+n; i++ {
			bin[i] = 0xff
		}
		doc["materials"].([]any)[0].(map[string]any)["alphaMode"] = "BLEND"
		prim := doc["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any)
		prim["attributes"].(map[string]any)["COLOR_0"] = prim["attributes"].(map[string]any)["NORMAL"]
	})
	m, err := Load(ctx, p, decoders(t))
	if err != nil {
		t.Fatalf("corrupt KTX2 picture: %v", err)
	}
	if m.Mesh.Mats[0].Base != nil || m.Mesh.Mats[0].MetalRough == nil {
		t.Error("the corrupt picture was kept, or the others were dropped")
	}
	note := m.Note()
	for _, want := range []string{"texture 0 (image", "alpha mode BLEND", "COLOR_0"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	if _, _, err := render3d.Render(m.Mesh, nil, render3d.Options{Mode: render3d.ModePBR, Size: 32}); err != nil {
		t.Errorf("pbr: %v", err)
	}
	// Without a KTX2 transcoder the textures are notes, not an error.
	m, err = Load(ctx, filepath.Join("testdata", "cube_etc1s.glb"), Decoders{})
	if err != nil || m.Mesh.Mats[0].Base != nil || !strings.Contains(m.Note(), "no KTX2 transcoder") {
		t.Errorf("no transcoder: %v, note %q", err, m.Note())
	}
}

// TestNoMaterialsNoTextures: a file whose primitives name no material has
// no materials; texture coordinates missing drop the maps, not the
// factors.
func TestNoMaterialsNoTextures(t *testing.T) {
	ctx := context.Background()
	p := rewrite(t, "cube_png.glb", func(doc map[string]any, _ []byte) {
		delete(doc["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any), "material")
	})
	m, err := Load(ctx, p, decoders(t))
	if err != nil || len(m.Mesh.Mats) != 0 {
		t.Fatalf("no material: %v, %d materials", err, len(m.Mesh.Mats))
	}
	p = rewrite(t, "cube_png.glb", func(doc map[string]any, _ []byte) {
		delete(doc["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any)["attributes"].(map[string]any), "TEXCOORD_0")
	})
	m, err = Load(ctx, p, decoders(t))
	if err != nil || len(m.Mesh.Mats) != 1 || m.Mesh.Mats[0].Base != nil || m.Mesh.Mats[0].Metallic != 0.5 {
		t.Fatalf("no texture coordinates: %v, %+v", err, m.Mesh.Mats)
	}
	if !strings.Contains(m.Note(), "no texture coordinates") {
		t.Errorf("note %q", m.Note())
	}
}

// The glb package's texture source order: basisu, then webp, then source.
func TestTextureSources(t *testing.T) {
	var tx glb.Texture
	if err := json.Unmarshal([]byte(`{"source":2,"extensions":{"EXT_texture_webp":{"source":1},"KHR_texture_basisu":{"source":0},"EXT_texture_avif":{"source":3}}}`), &tx); err != nil {
		t.Fatal(err)
	}
	if got := tx.Sources(); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("sources %v", got)
	}
}
