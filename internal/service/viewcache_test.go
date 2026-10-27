package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// TestViewShare: two users of one map file share one decode while either
// holds it; once none does, the share forgets it (the window keeps nothing
// for content it left).
func TestViewShare(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	f := mapFile{kind: "diffuse", data: buf.Bytes(), sum: sum(buf.Bytes())}
	s := &Service{}
	a, err := s.viewTexture(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.viewTexture(f)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("a second user decoded the map again")
	}
	if !a.SRGB || a.W != 8 {
		t.Fatalf("decoded %dx%d srgb %v", a.W, a.H, a.SRGB)
	}
	other := mapFile{kind: "normal", data: f.data, sum: f.sum}
	c, err := s.viewTexture(other)
	if err != nil {
		t.Fatal(err)
	}
	if c == a || c.SRGB {
		t.Fatal("the same file as another kind shared the colour map")
	}
	runtime.KeepAlive(a)
	runtime.KeepAlive(c)
	a, b, c = nil, nil, nil
	runtime.GC()
	key := f.kind + " " + f.sum
	if shared(&s.view.mu, &s.view.maps, key) != nil {
		t.Fatal("the share kept a map nobody holds")
	}
}

// TestViewShareConcurrent: users asking for one file at the same moment
// get one decode (the second check under the load lock), for maps and for
// meshes.
func TestViewShareConcurrent(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	f := mapFile{kind: "diffuse", data: buf.Bytes(), sum: sum(buf.Bytes())}
	s := &Service{}
	src, key := meshFixture(t)
	for run := 0; run < 20; run++ {
		const users = 8
		texs := make([]*render3d.Texture, users)
		meshes := make([]*render3d.Mesh, users)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range users {
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				tx, err := s.viewTexture(f)
				if err != nil {
					t.Error(err)
				}
				texs[i] = tx
			}()
			go func() {
				defer wg.Done()
				<-start
				m, err := s.viewMesh(context.Background(), src, nil, key, false)
				if err != nil {
					t.Error(err)
				}
				meshes[i] = m
			}()
		}
		close(start)
		wg.Wait()
		for i := 1; i < users; i++ {
			if texs[i] != texs[0] || meshes[i] != meshes[0] {
				t.Fatalf("run %d: user %d got its own decode", run, i)
			}
		}
		runtime.KeepAlive(texs)
		runtime.KeepAlive(meshes)
		texs, meshes = nil, nil
		runtime.GC()
	}
}

// TestViewMeshShare: the viewer's meshes are shared as its maps are: two
// users of one stage file share one mesh while either holds it, another
// file's content is its own mesh, and once nobody holds a mesh the share
// forgets it and the next user reads it again.
func TestViewMeshShare(t *testing.T) {
	s := &Service{}
	src, key := meshFixture(t)
	a, err := s.viewMesh(context.Background(), src, nil, key, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.viewMesh(context.Background(), src, nil, key, false)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("a second user read the mesh again")
	}
	if len(a.Idx) != 3 {
		t.Fatalf("mesh has %d indices, want 3", len(a.Idx))
	}
	src2, key2 := meshFixture(t)
	c, err := s.viewMesh(context.Background(), src2, nil, key2, false)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("another file's content shared the mesh")
	}
	runtime.KeepAlive(a)
	runtime.KeepAlive(c)
	a, b, c = nil, nil, nil
	runtime.GC()
	if shared(&s.view.mu, &s.view.meshes, key) != nil || shared(&s.view.mu, &s.view.meshes, key2) != nil {
		t.Fatal("the share kept a mesh nobody holds")
	}
	d, err := s.viewMesh(context.Background(), src, nil, key, false)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || len(d.Idx) != 3 {
		t.Fatal("the mesh was not read again")
	}
	if n := len(s.view.meshes); n != 1 {
		t.Fatalf("the share has %d entries, want 1 (dead ones are dropped)", n)
	}
}

// meshFixture writes a stage file of new content with its mesh file beside
// it (as loadMesh keeps decodes) in a temporary folder, and returns the
// stage file and its content key.
func meshFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	content := []byte(dir) // unique per call
	key := sum(content)
	src := filepath.Join(dir, "model.glb")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	m := &render3d.Mesh{Pos: [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, Idx: []uint32{0, 1, 2}}
	mf := filepath.Join(dir, fmt.Sprintf("mesh-v%s-%s.r3dmesh", meshVersion, key[:16]))
	if err := os.WriteFile(mf, render3d.EncodeMesh(m, false), 0o600); err != nil {
		t.Fatal(err)
	}
	return src, key
}
