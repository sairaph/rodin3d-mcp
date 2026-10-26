package app

import (
	"bytes"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Loading runs on goroutines; each result is a closure posted back to the
// game loop, which drops it when the content or the pane changed meanwhile.

// thumbEdge is the longer edge of strip and input-card pictures.
const thumbEdge = 512

// post runs fn on the game loop.
func (a *App) post(fn func()) {
	select {
	case a.loaded <- fn:
	case <-a.ctx.Done():
	}
}

// startLoads begins loading everything new content shows.
func (a *App) startLoads(p *prepared) {
	for _, pn := range a.panes {
		a.loadPane(pn)
	}
	switch p.kind {
	case viewer.KindTask:
		vt := p.tasks[0]
		a.loadStrip(vt, p.stages[0])
		if st, ok := vt.Stage("material"); ok && len(st.Maps) > 0 {
			a.loadTaskMaps(vt, st, nil)
		}
	case viewer.KindTextures:
		a.loadTaskMaps(p.tasks[0], p.stages[0], p.maps)
	case viewer.KindInputSet, viewer.KindTaskInputs:
		for _, it := range a.inputs.items {
			a.loadThumb(it, it.file)
		}
	case viewer.KindImages:
		for _, f := range p.images {
			t := &thumb{kind: "image", name: filepath.Base(f), file: f}
			a.textures = append(a.textures, t)
			a.loadFullImage(t)
		}
	}
}

// loadPane loads the pane's mesh (and maps) in the background.
func (a *App) loadPane(p *pane) {
	gen, cgen := p.gen, a.contentGen
	p.status, p.err, p.what = "loading", "", "mesh"
	p.mapsWanted = p.kind == viewer.KindTask && p.stage.Name == "material" && len(p.stage.Maps) > 0
	a.dirty()
	current := func() bool { return a.contentGen == cgen && p.gen == gen }
	// A load ending changes no state the agent or the user set, so it does
	// not bump the revision (an agent's expected_revision stays good); the
	// pane's status and note in the state say what happened.
	failed := func(err error) {
		e := service.Classify(err)
		a.post(func() {
			if current() {
				p.status, p.err = "error", e.Message
				a.dirty()
			}
		})
	}
	// The goroutine reads only these copies; the game loop owns p.
	kind, file, stage := p.kind, p.file, p.stage
	var uuid string
	if p.vt != nil {
		uuid = p.vt.UUID
	}
	wantMaps := p.mapsWanted
	go func() {
		t0 := time.Now()
		if kind == viewer.KindModelFile {
			m, err := a.svc.ViewModel(a.ctx, file)
			if err != nil {
				failed(err)
				return
			}
			simple := render3d.Simplify(m.Mesh, simplifyTarget)
			modes := service.ModelModes(m)
			log.Printf("model %s: %d triangles (%d quick) in %v", filepath.Base(file), len(m.Mesh.Idx)/3, len(simple.Idx)/3, time.Since(t0).Round(time.Millisecond))
			a.post(func() {
				if !current() {
					return
				}
				p.mesh, p.simple, p.maps, p.frame, p.modes, p.status = m.Mesh, simple, m.Maps, render3d.FrameOf(m.Mesh), modes, "ready"
				if !slices.Contains(p.modes, a.mode) {
					// a.mode is set before the load only when a show asked for it.
					asked := a.mode
					a.mode = defaultMode(p.modes)
					if asked != "" {
						p.note = fmt.Sprintf("mode %s is not available for this model, so it shows %s", asked, a.mode)
					}
				}
				if m.Maps != nil && len(a.textures) == 0 {
					a.textures = mapThumbs(m.Maps, nil)
				}
				a.dirty()
			})
			return
		}
		mesh, err := a.svc.ViewMesh(a.ctx, uuid, stage)
		if err != nil {
			failed(err)
			return
		}
		simple := render3d.Simplify(mesh, simplifyTarget)
		frame := render3d.FrameOf(mesh)
		log.Printf("task %s %s: %d triangles (%d quick) in %v", uuid[:8], stage.Name, len(mesh.Idx)/3, len(simple.Idx)/3, time.Since(t0).Round(time.Millisecond))
		a.post(func() {
			if current() {
				p.mesh, p.simple, p.frame, p.draft, p.status = mesh, simple, frame, stage.Draft, "ready"
				if p.mapsWanted {
					p.what = "textures"
				}
				a.dirty()
			}
		})
		if !wantMaps {
			return
		}
		maps, err := a.svc.ViewMaps(a.ctx, uuid, stage)
		if err != nil {
			failed(err)
			return
		}
		a.post(func() {
			if current() {
				p.maps, p.mapsWanted = maps, false
				a.dirty()
			}
		})
	}()
}

// loadStrip fills the image strip with the stage's server images and the
// task's input images; on a task the inputs panel shows the same pictures,
// read-only.
func (a *App) loadStrip(vt *service.ViewTask, st service.ViewStage) {
	cgen := a.contentGen
	var strip []*thumb
	for _, s := range st.Server {
		t := &thumb{kind: s.Mode, name: s.Mode}
		strip = append(strip, t)
		go a.fetchThumb(cgen, t, func() (string, error) { return a.svc.ViewFile(a.ctx, vt.UUID, s) })
	}
	var inputs []*thumb
	for _, in := range vt.Inputs {
		t := &thumb{kind: "input", name: in.Name, view: in.View, weight: in.Weight}
		inputs = append(inputs, t)
		strip = append(strip, t)
		go a.fetchThumb(cgen, t, func() (string, error) { return a.svc.ViewInputFile(a.ctx, vt.UUID, in) })
	}
	a.strip = strip
	if len(inputs) > 0 && a.kind == viewer.KindTask {
		a.inputs = &inputModel{editable: false, fromTask: vt.UUID, items: inputs}
	}
	a.dirty()
}

// fetchThumb gets a file (download when needed) and makes its thumbnail.
func (a *App) fetchThumb(cgen uint64, t *thumb, get func() (string, error)) {
	file, err := get()
	if err != nil {
		e := service.Classify(err)
		a.post(func() {
			if a.contentGen == cgen {
				t.err = e.Message
				a.dirty()
			}
		})
		return
	}
	a.post(func() {
		if a.contentGen == cgen {
			t.file = file
		}
	})
	a.decodeThumb(cgen, t, file)
}

// loadThumb makes the thumbnail of a local file.
func (a *App) loadThumb(t *thumb, file string) {
	go a.decodeThumb(a.contentGen, t, file)
}

func (a *App) decodeThumb(cgen uint64, t *thumb, file string) {
	img, w, h, err := decodeFile(file, thumbEdge)
	a.post(func() {
		if a.contentGen != cgen {
			return
		}
		if err != nil {
			t.err = err.Error()
		} else {
			t.img, t.w, t.h = ebiten.NewImageFromImage(img), w, h
		}
		a.dirty()
	})
}

// loadFullImage loads an image file at full size (textures layout).
func (a *App) loadFullImage(t *thumb) {
	cgen := a.contentGen
	go func() {
		img, w, h, err := decodeFile(t.file, 0)
		a.post(func() {
			if a.contentGen != cgen {
				return
			}
			if err != nil {
				t.err = err.Error()
			} else {
				t.img, t.w, t.h = ebiten.NewImageFromImage(img), w, h
			}
			a.dirty()
		})
	}()
}

// decodeFile reads a png, jpeg or webp file, fitted to edge (0: as is).
func decodeFile(file string, edge int) (image.Image, int, int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, 0, err
	}
	img, err := imaging.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if edge > 0 {
		img = imaging.Fit(img, edge)
	}
	return img, w, h, nil
}

// loadTaskMaps loads a task's texture maps for the textures layout (only
// names, when given).
func (a *App) loadTaskMaps(vt *service.ViewTask, st service.ViewStage, names []string) {
	cgen := a.contentGen
	for _, m := range st.Maps {
		name := strings.TrimPrefix(m.Mode, "texture_")
		if names != nil && !slices.Contains(names, name) {
			continue
		}
		a.textures = append(a.textures, &thumb{kind: "map", name: name})
	}
	go func() {
		maps, err := a.svc.ViewMaps(a.ctx, vt.UUID, st)
		a.post(func() {
			if a.contentGen != cgen {
				return
			}
			loaded := mapThumbs(maps, nil)
			for _, t := range a.textures {
				if err != nil {
					t.err = service.Classify(err).Message
					continue
				}
				for _, l := range loaded {
					if l.name == t.name {
						t.img, t.w, t.h = l.img, l.w, l.h
					}
				}
				if t.img == nil && t.err == "" {
					t.err = "not in the task's files"
				}
			}
			a.dirty()
		})
	}()
}

// mapThumbs turns decoded render maps into pictures (game loop: it makes
// ebiten images).
func mapThumbs(m *render3d.Maps, _ []string) []*thumb {
	if m == nil {
		return nil
	}
	var out []*thumb
	add := func(name string, t *render3d.Texture) {
		if t == nil {
			return
		}
		img := &image.NRGBA{Pix: t.Pix, Stride: 4 * t.W, Rect: image.Rect(0, 0, t.W, t.H)}
		out = append(out, &thumb{kind: "map", name: name, img: ebiten.NewImageFromImage(img), w: t.W, h: t.H})
	}
	add("diffuse", m.Diffuse)
	add("pbr", m.PBR)
	add("normal", m.Normal)
	add("shaded", m.Shaded)
	return out
}

// pendingImages counts pictures still loading.
func (a *App) pendingImages() int {
	n := 0
	count := func(ts []*thumb) {
		for _, t := range ts {
			if t.img == nil && t.err == "" {
				n++
			}
		}
	}
	switch a.layout {
	case viewer.LayoutInputs:
		if a.inputs != nil {
			count(a.inputs.items)
		}
	case viewer.LayoutTextures:
		count(a.textures)
	default:
		count(a.strip)
	}
	return n
}
