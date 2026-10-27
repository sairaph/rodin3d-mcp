package service

import (
	"context"
	"sync"
	"weak"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// viewShare lets the viewer window's users of one file (two panes, a pane
// and the textures panel) share one decode of it. It holds weak pointers
// only: an entry lives as long as the window holds the mesh or map, and the
// window drops them when the view moves on, so nothing stays in memory for
// content no longer shown (notes 41, fix round 3). Decodes run one at a
// time: an 8192 x 8192 map takes about half a gigabyte while it decodes.
type viewShare struct {
	mu     sync.Mutex
	meshes map[string]weak.Pointer[render3d.Mesh]
	maps   map[string]weak.Pointer[render3d.Texture]

	meshLoad sync.Mutex // one mesh read or decode at a time
	mapLoad  sync.Mutex // one map decode at a time
}

// shared returns the live entry of key in *m. It takes the map by pointer
// and reads it under mu: share replaces the map itself, so reading the
// field outside the lock would race with it.
func shared[T any](mu *sync.Mutex, m *map[string]weak.Pointer[T], key string) *T {
	mu.Lock()
	defer mu.Unlock()
	return (*m)[key].Value()
}

// share records v as the entry of key in *m and forgets entries that died.
func share[T any](mu *sync.Mutex, m *map[string]weak.Pointer[T], key string, v *T) {
	mu.Lock()
	defer mu.Unlock()
	if *m == nil {
		*m = map[string]weak.Pointer[T]{}
	}
	for k, p := range *m {
		if p.Value() == nil {
			delete(*m, k)
		}
	}
	(*m)[key] = weak.Make(v)
}

// viewMesh is the mesh of a stage file whose content hashes to key, shared
// with any other user of it; else read from the mesh file or decoded.
func (s *Service) viewMesh(ctx context.Context, srcFile string, src []byte, key string, draft bool) (*render3d.Mesh, error) {
	v := &s.view
	if m := shared(&v.mu, &v.meshes, key); m != nil {
		return m, nil
	}
	v.meshLoad.Lock()
	defer v.meshLoad.Unlock()
	if m := shared(&v.mu, &v.meshes, key); m != nil {
		return m, nil
	}
	m, _, err := s.loadMesh(ctx, srcFile, src, key, draft)
	if err != nil {
		return nil, err
	}
	share(&v.mu, &v.meshes, key, m)
	return m, nil
}

// viewTexture is the decoded texture map f, shared with any other user of
// it.
func (s *Service) viewTexture(f mapFile) (*render3d.Texture, error) {
	v := &s.view
	key := f.kind + " " + f.sum
	if t := shared(&v.mu, &v.maps, key); t != nil {
		return t, nil
	}
	v.mapLoad.Lock()
	defer v.mapLoad.Unlock()
	if t := shared(&v.mu, &v.maps, key); t != nil {
		return t, nil
	}
	t, err := render3d.DecodeTexture(f.data, colourMap(f.kind))
	if err != nil {
		return nil, wrapf(err, "decode the %s texture map", f.kind)
	}
	share(&v.mu, &v.maps, key, t)
	return t, nil
}

// colourMap reports whether a map kind holds colour (sRGB) data.
func colourMap(kind string) bool { return kind == "diffuse" || kind == "shaded" }
