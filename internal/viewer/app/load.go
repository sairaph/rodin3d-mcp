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
			a.taskMaps(vt, st, nil)
		}
	case viewer.KindTextures:
		a.taskMaps(p.tasks[0], p.stages[0], p.maps)
	case viewer.KindInputSet, viewer.KindTaskInputs:
		for _, it := range a.inputs.items {
			a.loadThumb(it, it.file)
		}
	case viewer.KindImages:
		for _, f := range p.images {
			a.textures = append(a.textures, &thumb{kind: "image", name: filepath.Base(f), file: f, src: func() (image.Image, error) {
				img, _, _, err := decodeFile(f, 0)
				return img, err
			}})
		}
	}
}

// Memory (notes 41, fix rounds 3 and 4): the window holds the meshes and
// maps of the panes it shows and a GPU picture of what is on screen, nothing
// more. A task pane loads its texture maps only when its mode draws them and
// keeps only those of the mode shown (setMode); the textures panel decodes
// and uploads only the selected map; pictures of content the view left are
// freed at once and the freed memory goes back to the system (trimSoon).

// loadPane loads the pane's mesh in the background (a model file's maps
// come with it; a task's are loaded by loadPaneMaps when a mode needs them).
func (a *App) loadPane(p *pane) {
	gen, cgen := p.gen, a.contentGen
	p.status, p.err, p.what = "loading", "", "mesh"
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
					a.setMode(defaultMode(p.modes))
					if asked != "" {
						p.note = fmt.Sprintf("mode %s is not available for this model, so it shows %s", asked, a.mode)
					}
				}
				if m.Maps != nil && len(a.textures) == 0 {
					a.textures = modelMapThumbs(m.Maps)
				}
				a.dirty()
				a.trimSoon()
			})
			return
		}
		if stage.Turntable() {
			sheet, err := a.svc.ViewTurntable(a.ctx, uuid, stage)
			if err != nil {
				failed(err)
				return
			}
			log.Printf("task %s %s: turntable sheet in %v", uuid[:8], stage.Name, time.Since(t0).Round(time.Millisecond))
			a.post(func() {
				if current() {
					p.sheet, p.status = sheet, "ready"
					a.dirty()
				}
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
				a.dirty()
				a.trimSoon()
			}
		})
	}()
}

// sampledMaps lists the texture maps of a task pane's stage that mode
// samples (Shaded draws the diffuse map when the stage has no shaded one);
// nil for a model file, whose maps came with it.
func sampledMaps(p *pane, mode string) []string {
	if p.kind != viewer.KindTask {
		return nil
	}
	has := func(kind string) bool {
		return slices.ContainsFunc(p.stage.Maps, func(m service.ViewAsset) bool { return m.Mode == "texture_"+kind })
	}
	var out []string
	for _, k := range render3d.Mode(mode).MapsSampled() {
		if k == "shaded" && !has(k) {
			k = "diffuse"
		}
		if has(k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// missingMaps lists the texture maps mode samples that a task pane has not
// loaded yet.
func missingMaps(p *pane, mode string) []string {
	var out []string
	for _, k := range sampledMaps(p, mode) {
		if p.maps.Map(k) == nil {
			out = append(out, k)
		}
	}
	return out
}

// keepMaps is the set of maps of the kinds in keep, each from add when it
// has one, else from old; old itself when that is what it holds, nil when
// none. Maps are never changed in place: a render job keeps the set it was
// given.
func keepMaps(old, add *render3d.Maps, keep []string) *render3d.Maps {
	out := &render3d.Maps{}
	same, n := true, 0
	for _, k := range render3d.MapKinds {
		var t *render3d.Texture
		if slices.Contains(keep, k) {
			if t = add.Map(k); t == nil {
				t = old.Map(k)
			}
		}
		if t != nil {
			out.SetMap(k, t)
			n++
		}
		if t != old.Map(k) {
			same = false
		}
	}
	switch {
	case same:
		return old
	case n == 0:
		return nil
	}
	return out
}

// mapCount is how many maps m holds (part of a render key: the maps a pane
// holds change only with the mode, the stage or a load).
func mapCount(m *render3d.Maps) int {
	n := 0
	for _, k := range render3d.MapKinds {
		if m.Map(k) != nil {
			n++
		}
	}
	return n
}

// mapsError says why a ready task pane cannot draw mode: a map mode samples
// failed to load ("" when none did). Only that mode and the others that
// sample the map are affected; the mesh stays and every other mode draws.
func mapsError(p *pane, mode string) string {
	var why []string
	for _, k := range missingMaps(p, mode) {
		if msg, ok := p.mapsFailed[k]; ok {
			if !strings.HasSuffix(msg, ".") {
				msg += "."
			}
			why = append(why, "the "+k+" map did not load: "+msg)
		}
	}
	if len(why) == 0 {
		return ""
	}
	return "mode " + mode + ": " + strings.Join(why, " ") + " Choose the mode again to retry."
}

// setMode shows mode m. Each task pane keeps only the maps m samples (the
// maps of earlier modes are released; switching back reads them again), and
// map failures are forgotten, so a textured mode chosen again (the same one
// too) loads its missing maps again.
func (a *App) setMode(m string) {
	a.mapsTry++
	for _, p := range a.panes {
		p.mapsFailed = nil
		if p.kind != viewer.KindTask {
			continue
		}
		if kept := keepMaps(p.maps, nil, sampledMaps(p, m)); kept != p.maps {
			p.maps = kept
			a.trimSoon()
		}
	}
	a.mode = m
	a.dirty()
}

// loadPaneMaps loads the texture maps of kinds into a ready task pane whose
// mode samples them; mapsWanted marks the load. The pane keeps the maps the
// mode shown when the load ends samples; a map that fails is recorded in
// mapsFailed (mapsError), unless the mode was chosen again meanwhile.
func (a *App) loadPaneMaps(p *pane, kinds []string) {
	if p.mapsWanted || p.status != "ready" {
		return
	}
	gen, cgen, try := p.gen, a.contentGen, a.mapsTry
	p.mapsWanted = true
	a.dirty()
	uuid, stage := p.vt.UUID, p.stage
	go func() {
		loaded := &render3d.Maps{}
		failed := map[string]string{}
		for _, k := range kinds {
			t0 := time.Now()
			t, err := a.viewMap(a.ctx, uuid, stage, k)
			if err != nil {
				failed[k] = service.Classify(err).Message
				log.Printf("task %s %s: %s map: %s", uuid[:8], stage.Name, k, failed[k])
				continue
			}
			loaded.SetMap(k, t)
			log.Printf("task %s %s: %s map in %v", uuid[:8], stage.Name, k, time.Since(t0).Round(time.Millisecond))
		}
		a.post(func() {
			if a.contentGen != cgen || p.gen != gen {
				return
			}
			p.mapsWanted = false
			p.maps = keepMaps(p.maps, loaded, sampledMaps(p, a.mode))
			if len(failed) > 0 && a.mapsTry == try {
				p.mapsFailed = failed
			}
			a.dirty()
			a.trimSoon()
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
		if a.contentGen != cgen || !a.listed(t) {
			return
		}
		if err != nil {
			t.err = err.Error()
		} else {
			t.img, t.w, t.h = newImageFrom(img), w, h
		}
		a.dirty()
	})
}

// listed reports whether the view still lists t in its strip or its inputs
// panel. A stage change replaces the strip within the same content, and
// loads of the old strip may end after that: their pictures are not
// uploaded (nothing would free them).
func (a *App) listed(t *thumb) bool {
	return slices.Contains(a.strip, t) || a.inputs != nil && slices.Contains(a.inputs.items, t)
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

// taskMaps lists a task's texture maps in the textures panel (only names,
// when given); each is decoded when it is shown (loadTexture).
func (a *App) taskMaps(vt *service.ViewTask, st service.ViewStage, names []string) {
	for _, m := range st.Maps {
		name := strings.TrimPrefix(m.Mode, "texture_")
		if names != nil && !slices.Contains(names, name) {
			continue
		}
		a.textures = append(a.textures, &thumb{kind: "map", name: name, src: func() (image.Image, error) {
			t, err := a.viewMap(a.ctx, vt.UUID, st, name)
			if err != nil {
				return nil, err
			}
			return textureImage(t), nil
		}})
	}
}

// modelMapThumbs lists a model file's maps in the textures panel; their
// pixels are the pane's (no copy).
func modelMapThumbs(m *render3d.Maps) []*thumb {
	var out []*thumb
	add := func(name string, t *render3d.Texture) {
		if t != nil {
			out = append(out, &thumb{kind: "map", name: name, w: t.W, h: t.H, src: func() (image.Image, error) { return textureImage(t), nil }})
		}
	}
	add("diffuse", m.Diffuse)
	add("pbr", m.PBR)
	add("normal", m.Normal)
	add("shaded", m.Shaded)
	return out
}

// shownTexture is the picture the textures panel shows now, or nil.
func (a *App) shownTexture() *thumb {
	if a.layout != viewer.LayoutTextures || a.texSel < 0 || a.texSel >= len(a.textures) {
		return nil
	}
	return a.textures[a.texSel]
}

// syncImages keeps the large pictures only while they are on screen: the
// selected texture of the textures panel (loaded when missing) and the strip
// picture shown flat. Others are freed.
func (a *App) syncImages() {
	shown := a.shownTexture()
	for _, t := range a.textures {
		if t != shown && t.img != nil {
			release(&t.img)
			a.trimSoon()
		}
	}
	if shown != nil && shown.img == nil && shown.err == "" && !shown.loading {
		a.loadTexture(shown)
	}
	for i, t := range a.strip {
		if i != a.flat && t.full != nil {
			release(&t.full)
			a.trimSoon()
		}
	}
}

// loadTexture decodes a texture panel picture and uploads it if it is still
// the one shown.
func (a *App) loadTexture(t *thumb) {
	t.loading = true
	cgen := a.contentGen
	go func() {
		img, err := t.src()
		var msg string
		if err != nil {
			msg = service.Classify(err).Message
		}
		a.post(func() {
			if a.contentGen != cgen {
				return
			}
			t.loading = false
			switch {
			case err != nil:
				t.err = msg
			case a.shownTexture() == t:
				t.img = newImageFrom(img)
				t.w, t.h = img.Bounds().Dx(), img.Bounds().Dy()
			}
			a.dirty()
			a.trimSoon()
		})
	}()
}

// releaseImages frees the GPU pictures of the content the view leaves.
func (a *App) releaseImages() {
	for _, p := range a.panes {
		release(&p.img)
	}
	a.releaseThumbs(a.strip)
	a.releaseThumbs(a.textures)
	if a.inputs != nil {
		a.releaseThumbs(a.inputs.items)
	}
	a.trimSoon()
}

func (a *App) releaseThumbs(ts []*thumb) {
	for _, t := range ts {
		release(&t.img)
		release(&t.full)
	}
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
		if t := a.shownTexture(); t != nil {
			count([]*thumb{t})
		}
	default:
		count(a.strip)
	}
	return n
}
