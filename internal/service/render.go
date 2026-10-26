package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// renderVersion is part of every cached render's key: bump it when a change
// to render3d changes its pixels, so stale renders are not served.
const renderVersion = "5"

// meshVersion is part of every cached mesh file's name and render's key:
// bump it when a change to the Draco or GLB reader (or meshOfDraco) changes
// the mesh read from a file, so stale meshes and renders are not served.
const meshVersion = "1"

// renderState is what get_preview keeps between renders in one process: the
// Draco decoder (compiled once), the last stage mesh and texture set (so a
// second mode of the same stage reads nothing), and a one-slot semaphore, as
// a render already uses every core and its buffers are large.
type renderState struct {
	semOnce sync.Once
	sem     chan struct{}

	decOnce sync.Once
	dec     *draco.Decoder
	decErr  error

	mu      sync.Mutex
	meshKey string
	mesh    *render3d.Mesh
	mapsKey string
	maps    *render3d.Maps
}

func (r *renderState) acquire(ctx context.Context) (func(), error) {
	r.semOnce.Do(func() { r.sem = make(chan struct{}, 1) })
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// decoder compiles the embedded Draco decoder on first use, keeping wazero's
// compiled code under the cache folder for the next process.
func (s *Service) decoder() (*draco.Decoder, error) {
	r := &s.r3d
	r.decOnce.Do(func() {
		r.dec, r.decErr = draco.New(context.Background(), filepath.Join(s.paths.Cache, "wazero"))
	})
	return r.dec, r.decErr
}

// renderResult is one local render.
type renderResult struct {
	PNG          []byte
	Width        int
	Height       int
	Cached       bool          // served from the render cache
	Took         time.Duration // render time (0 when cached)
	Decoded      bool          // the stage mesh was decoded by this call
	DecodeTook   time.Duration
	MeshVertices int
	MeshFaces    int
}

// mapFile is one texture map of a material render.
type mapFile struct {
	kind string // diffuse | normal | pbr | shaded
	data []byte
	sum  string
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// renderStage renders stage stg of the task in d. The stage mesh and the
// texture maps come from the asset cache (fetched when missing); the mesh is
// decoded once per file content and kept as a mesh file beside it, and each
// rendered PNG is kept under renders/ there, keyed by the content of every
// input, so a repeated call reads one file.
func (s *Service) renderStage(ctx context.Context, uuid string, d *taskDetail, stg string, mode render3d.Mode, view render3d.View, size int) (renderResult, error) {
	var out renderResult
	cache, err := s.assetCache()
	if err != nil {
		return out, err
	}
	ma, ok := assets.MeshAsset(d.card, d.hist, stg)
	if !ok {
		return out, fmt.Errorf("the %s stage has no mesh", stg)
	}
	meshFile, _, err := cache.Get(ctx, uuid, ma)
	if err != nil {
		return out, err
	}
	src, err := os.ReadFile(meshFile)
	if err != nil {
		return out, err
	}
	draft := ma.Mode == assets.MeshDraft
	if draft && !draco.IsDraco(src) {
		return out, fail(render.CodeConflict, "The geometry draft of this task is not a Draco mesh (an older preview format), so it cannot be rendered here.",
			"Call get_preview with mode poster for the draft's server image; renders work from the confirmed geometry on.")
	}
	meshSum := sum(src)

	var maps []mapFile
	if mode.NeedsMaps() {
		for _, kind := range []string{"diffuse", "normal", "pbr", "shaded"} {
			a, ok := assets.Find(d.list, assets.StageMaterial, "texture_"+kind)
			if !ok {
				continue
			}
			f, _, err := cache.Get(ctx, uuid, a)
			if err != nil {
				return out, err
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return out, err
			}
			maps = append(maps, mapFile{kind: kind, data: b, sum: sum(b)})
		}
	}

	key := sha256.New()
	fmt.Fprintf(key, "%s\n%s\n%s\n%s\n%s\n%d\n", renderVersion, meshVersion, meshSum, mode, view, size)
	for _, m := range maps {
		fmt.Fprintf(key, "%s %s\n", m.kind, m.sum)
	}
	cached := filepath.Join(filepath.Dir(meshFile), "renders", fmt.Sprintf("%s-%s-%d-%s.png", mode, view, size, hex.EncodeToString(key.Sum(nil))[:16]))
	if res, ok := readRender(cached); ok {
		return res, nil
	}

	release, err := s.r3d.acquire(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	if res, ok := readRender(cached); ok { // rendered by a call this one waited for
		return res, nil
	}
	t0 := time.Now()
	mesh, decoded, err := s.stageMesh(ctx, meshFile, src, meshSum, draft)
	if err != nil {
		return out, err
	}
	out.Decoded, out.MeshVertices, out.MeshFaces = decoded, len(mesh.Pos), len(mesh.Idx)/3
	if decoded {
		out.DecodeTook = time.Since(t0)
	}
	tex, err := s.stageMaps(maps)
	if err != nil {
		return out, err
	}
	t1 := time.Now()
	img, _, err := render3d.Render(mesh, tex, render3d.Options{Mode: mode, View: view, Size: size, Draft: draft})
	var ie *render3d.InputError
	if errors.As(err, &ie) {
		return out, fail(render.CodeConflict, fmt.Sprintf("The %s mesh of this task cannot be drawn in mode %s: it %s.", stg, mode, ie.Reason),
			"Call get_preview with another mode from available.")
	}
	if err != nil {
		return out, wrapf(err, "render the %s stage in mode %s", stg, mode)
	}
	data, err := encodeRender(img)
	if err != nil {
		return out, err
	}
	out.Took = time.Since(t1)
	if err := domain.WriteFileAtomic(cached, data, 0o600); err != nil {
		return out, wrapf(err, "write %s", cached)
	}
	out.PNG, out.Width, out.Height = data, img.Bounds().Dx(), img.Bounds().Dy()
	// A render's buffers are hundreds of megabytes; give them back now
	// rather than holding them in a long-running server.
	debug.FreeOSMemory()
	return out, nil
}

// readRender returns a cached render when the file is a whole PNG.
func readRender(path string) (renderResult, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return renderResult{}, false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return renderResult{}, false
	}
	return renderResult{PNG: b, Width: cfg.Width, Height: cfg.Height, Cached: true}, true
}

func encodeRender(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode the render: %w", err)
	}
	return buf.Bytes(), nil
}

// stageMesh returns the mesh of a stage file whose content hashes to key:
// the one kept in memory, the mesh file beside the source, or a decode of
// the source (then written as that mesh file). decoded reports the decode.
func (s *Service) stageMesh(ctx context.Context, srcFile string, src []byte, key string, draft bool) (m *render3d.Mesh, decoded bool, err error) {
	r := &s.r3d
	r.mu.Lock()
	if r.meshKey == key && r.mesh != nil {
		m = r.mesh
	}
	r.mu.Unlock()
	if m != nil {
		return m, false, nil
	}
	keep := func(m *render3d.Mesh) {
		r.mu.Lock()
		r.meshKey, r.mesh = key, m
		r.mu.Unlock()
	}
	meshFile := filepath.Join(filepath.Dir(srcFile), fmt.Sprintf("mesh-v%s-%s.r3dmesh", meshVersion, key[:16]))
	if b, err := os.ReadFile(meshFile); err == nil {
		if m, _, err := render3d.DecodeMesh(b); err == nil {
			keep(m)
			return m, false, nil
		}
	}
	// Drop the previous mesh before decoding the next: both can be large.
	keep(nil)
	dec, err := s.decoder()
	if err != nil {
		return nil, false, wrapf(err, "start the Draco decoder")
	}
	if draft {
		g, err := dec.Decode(ctx, src)
		if err != nil {
			return nil, false, wrapf(err, "decode the geometry draft")
		}
		if m, err = meshOfDraco(g); err != nil {
			return nil, false, err
		}
	} else {
		f, err := glb.Parse(src)
		if err != nil {
			return nil, false, wrapf(err, "read the model GLB")
		}
		gm, err := f.LoadMesh(ctx, dec)
		if err != nil {
			return nil, false, wrapf(err, "read the model GLB")
		}
		m = &render3d.Mesh{Pos: gm.Pos, Nrm: gm.Nrm, UV: gm.UV, Idx: gm.Idx}
	}
	if err := m.Validate(); err != nil {
		return nil, false, err
	}
	// The mesh file only saves the next process a decode; a failed write
	// costs nothing else.
	_ = domain.WriteFileAtomic(meshFile, render3d.EncodeMesh(m, draft), 0o600)
	keep(m)
	return m, true, nil
}

// meshOfDraco turns a decoded Draco mesh into a render mesh: positions,
// and normals and texture coordinates when the file has them.
func meshOfDraco(g *draco.Geometry) (*render3d.Mesh, error) {
	pos := g.ByType(draco.Position)
	if !g.Mesh || pos == nil || pos.Components != 3 {
		return nil, errors.New("the geometry draft is not a triangle mesh with positions")
	}
	m := &render3d.Mesh{Idx: g.Indices, Pos: make([][3]float32, g.Points)}
	for i := range m.Pos {
		m.Pos[i] = [3]float32{pos.Data[i*3], pos.Data[i*3+1], pos.Data[i*3+2]}
	}
	if a := g.ByType(draco.Normal); a != nil && a.Components == 3 {
		m.Nrm = make([][3]float32, g.Points)
		for i := range m.Nrm {
			m.Nrm[i] = [3]float32{a.Data[i*3], a.Data[i*3+1], a.Data[i*3+2]}
		}
	}
	if a := g.ByType(draco.TexCoord); a != nil && a.Components == 2 {
		m.UV = make([][2]float32, g.Points)
		for i := range m.UV {
			m.UV[i] = [2]float32{a.Data[i*2], a.Data[i*2+1]}
		}
	}
	return m, nil
}

// stageMaps decodes the texture maps of a material render (nil for none),
// keeping the last set in memory.
func (s *Service) stageMaps(files []mapFile) (*render3d.Maps, error) {
	if len(files) == 0 {
		return nil, nil
	}
	var k string
	for _, f := range files {
		k += f.kind + f.sum
	}
	r := &s.r3d
	r.mu.Lock()
	if r.mapsKey == k && r.maps != nil {
		m := r.maps
		r.mu.Unlock()
		return m, nil
	}
	r.mapsKey, r.maps = "", nil
	r.mu.Unlock()
	m := &render3d.Maps{}
	for _, f := range files {
		t, err := render3d.DecodeTexture(f.data, f.kind == "diffuse" || f.kind == "shaded")
		if err != nil {
			return nil, wrapf(err, "decode the %s texture map", f.kind)
		}
		switch f.kind {
		case "diffuse":
			m.Diffuse = t
		case "normal":
			m.Normal = t
		case "pbr":
			m.PBR = t
		case "shaded":
			m.Shaded = t
		}
	}
	r.mu.Lock()
	r.mapsKey, r.maps = k, m
	r.mu.Unlock()
	return m, nil
}
