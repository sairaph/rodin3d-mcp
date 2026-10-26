package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// maxActions is how many user actions the window remembers (plan 2.11).
const maxActions = 50

// camState is the orbit camera in the units the tools use.
type camState struct {
	Az   float64    `json:"azimuth_deg"`
	El   float64    `json:"elevation_deg"`
	Zoom float64    `json:"zoom"`
	Pan  [2]float64 `json:"pan"`
}

func defaultCam() camState { c, _ := namedCam("three_quarter"); return c }

// namedCam is a named view as an orbit camera (zoom 1, no pan).
func namedCam(view string) (camState, bool) {
	theta, phi, ok := render3d.View(view).Angles()
	if !ok || view == string(render3d.ViewGrid) {
		return camState{}, false
	}
	return camState{Az: theta * 180 / math.Pi, El: 90 - phi*180/math.Pi, Zoom: 1}, true
}

func (c camState) r3d() render3d.Camera {
	return render3d.Camera{Theta: c.Az * math.Pi / 180, Phi: (90 - c.El) * math.Pi / 180, Zoom: c.Zoom, Pan: c.Pan}
}

// wrapAz keeps an azimuth in (-180, 180].
func wrapAz(a float64) float64 {
	a = math.Mod(a, 360)
	if a > 180 {
		a -= 360
	}
	if a <= -180 {
		a += 360
	}
	return a
}

func (c camState) String() string {
	return fmt.Sprintf("azimuth %.0f, elevation %.0f, zoom %.2f, pan %.2f %.2f", c.Az, c.El, c.Zoom, c.Pan[0], c.Pan[1])
}

// pane is one 3D view: a task stage or a model file.
type pane struct {
	index  int
	kind   string // task | model_file
	vt     *service.ViewTask
	stage  service.ViewStage
	file   string
	modes  []string
	gen    uint64 // bumps when the pane's mesh source changes
	status string // loading | ready | error
	err    string
	what   string // what is loading, for the screen

	mesh, simple *render3d.Mesh
	maps         *render3d.Maps
	mapsWanted   bool // the stage has maps that are still loading
	frame        render3d.Frame
	draft        bool

	img       *ebiten.Image
	shown     renderKey
	hasShown  bool
	queued    renderKey
	hasQueued bool
	div       int // quick-frame resolution divisor (2..4)
	renderErr string
}

// thumb is a picture of the strip, the inputs panel or the textures panel.
type thumb struct {
	kind   string // poster | geometry_image | material_image | input | map | image
	name   string
	view   string
	file   string
	img    *ebiten.Image
	w, h   int // full picture size
	err    string
	weight *float64

	full        *ebiten.Image // full-size picture for the flat view
	fullLoading bool
}

// inputModel is the shown input set (or a task's inputs, read-only).
type inputModel struct {
	id       string
	editable bool
	fromTask string
	items    []*thumb
}

// prepared is content resolved outside the game loop (network, files).
type prepared struct {
	kind     string
	tasks    []*service.ViewTask
	stages   []service.ViewStage
	inputSet *service.InputSet
	maps     []string
	file     string
	images   []string
}

// prepare resolves show content: reads tasks, checks files, stores input
// sets. It runs on the socket goroutine and may take seconds.
func (a *App) prepare(ctx context.Context, c viewer.Content) (*prepared, error) {
	kind, err := c.Kind()
	if err != nil {
		return nil, fail(render.CodeInvalidInput, capital(err.Error())+".", "Pass exactly one kind of content.")
	}
	p := &prepared{kind: kind}
	switch kind {
	case viewer.KindTask:
		vt, st, err := a.resolveRef(ctx, *c.Task)
		if err != nil {
			return nil, err
		}
		p.tasks, p.stages = []*service.ViewTask{vt}, []service.ViewStage{st}
	case viewer.KindCompare:
		if len(c.Compare) != 2 {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("compare takes two task references; got %d.", len(c.Compare)), "Pass two {task_id, stage?} objects.")
		}
		for _, ref := range c.Compare {
			vt, st, err := a.resolveRef(ctx, ref)
			if err != nil {
				return nil, err
			}
			p.tasks, p.stages = append(p.tasks, vt), append(p.stages, st)
		}
	case viewer.KindInputSet:
		if c.InputSet.ID != "" && len(c.InputSet.Images) > 0 {
			return nil, fail(render.CodeInvalidInput, "input_set gives an id and images at once.", "Pass the id of a stored set, or images for a new one.")
		}
		if c.InputSet.ID != "" {
			set, err := a.svc.LoadInputSet(c.InputSet.ID)
			if err != nil {
				return nil, err
			}
			p.inputSet = &set
			break
		}
		if err := service.CheckInputSet(c.InputSet.Images); err != nil {
			return nil, err
		}
		set := service.InputSet{ID: service.NewInputSetID(), Images: normaliseViews(c.InputSet.Images)}
		if err := a.svc.SaveInputSet(set); err != nil {
			return nil, err
		}
		p.inputSet = &set
	case viewer.KindTaskInputs:
		vt, err := a.svc.ViewTask(ctx, c.TaskInputs)
		if err != nil {
			return nil, err
		}
		if len(vt.Inputs) == 0 {
			return nil, fail(render.CodeConflict, "This task has no input images (a text or model task).", "Show the task itself.")
		}
		set := service.InputSet{ID: service.NewInputSetID(), FromTask: vt.UUID}
		for _, in := range vt.Inputs {
			f, err := a.svc.ViewInputFile(ctx, vt.UUID, in)
			if err != nil {
				return nil, err
			}
			set.Images = append(set.Images, viewer.InputImage{Path: f, View: in.View, Weight: in.Weight})
		}
		if err := a.svc.SaveInputSet(set); err != nil {
			return nil, err
		}
		p.tasks, p.inputSet = []*service.ViewTask{vt}, &set
	case viewer.KindTextures:
		vt, err := a.svc.ViewTask(ctx, c.Textures.TaskID)
		if err != nil {
			return nil, err
		}
		st, ok := vt.Stage("material")
		if !ok || len(st.Maps) == 0 {
			return nil, fail(render.CodeConflict, "This task has no texture maps yet (they come with generate_texture).", "Show the task's geometry instead.")
		}
		var have []string
		for _, m := range st.Maps {
			have = append(have, strings.TrimPrefix(m.Mode, "texture_"))
		}
		for _, m := range c.Textures.Maps {
			if !slices.Contains(have, m) {
				return nil, failFields(render.CodeConflict, fmt.Sprintf("The task has no %q map.", m), "Use the maps it has: "+strings.Join(have, ", ")+".", map[string]any{"maps": have})
			}
		}
		p.tasks, p.stages, p.maps = []*service.ViewTask{vt}, []service.ViewStage{st}, c.Textures.Maps
		if len(p.maps) == 0 {
			p.maps = have
		}
	case viewer.KindModelFile:
		if err := service.CheckModelFile(c.ModelFile); err != nil {
			return nil, err
		}
		p.file = filepath.Clean(c.ModelFile)
	case viewer.KindImages:
		if err := service.CheckImageFiles(c.Images); err != nil {
			return nil, err
		}
		p.images = c.Images
	}
	return p, nil
}

// normaliseViews writes ? for an empty view tag.
func normaliseViews(in []viewer.InputImage) []viewer.InputImage {
	out := make([]viewer.InputImage, len(in))
	for i, im := range in {
		if im.View == "" {
			im.View = "?"
		}
		out[i] = im
	}
	return out
}

// resolveRef reads the task of ref and picks its stage and version.
func (a *App) resolveRef(ctx context.Context, ref viewer.TaskRef) (*service.ViewTask, service.ViewStage, error) {
	vt, err := a.svc.ViewTask(ctx, ref.TaskID)
	if err != nil {
		return nil, service.ViewStage{}, err
	}
	st, err := pickStage(vt, ref.Stage, ref.Version)
	return vt, st, err
}

// pickStage returns stage name ("" or current: the task's own) of vt at
// version (geometry_draft only).
func pickStage(vt *service.ViewTask, name, version string) (service.ViewStage, error) {
	if name == "" || name == "current" {
		name = vt.Current
	}
	var withMesh []string
	for _, s := range vt.Stages {
		if s.HasMesh() {
			withMesh = append(withMesh, s.Name)
		}
	}
	st, ok := vt.Stage(name)
	if !ok || !st.HasMesh() {
		if len(withMesh) == 0 {
			return st, fail(render.CodeConflict, "This task has no stage with a 3D model yet (it may still be running).", "Call wait_task, then show it again.")
		}
		return st, failFields(render.CodeConflict, fmt.Sprintf("Stage %s of this task has no 3D model to show.", name),
			"Show one of its stages: "+strings.Join(withMesh, ", ")+".", map[string]any{"stages": withMesh})
	}
	if version != "" {
		if name != "geometry_draft" {
			return st, fail(render.CodeInvalidInput, "version applies to the geometry_draft stage only.", "Drop version, or show stage geometry_draft.")
		}
		return vt.DraftVersion(version)
	}
	return st, nil
}

// defaultMode is the first mode the web viewer shows for a stage: Normal
// on a draft, PBR when textured, Classic otherwise.
func defaultMode(modes []string) string {
	for _, m := range []string{"pbr", "normal", "classic"} {
		if slices.Contains(modes, m) {
			return m
		}
	}
	if len(modes) > 0 {
		return modes[0]
	}
	return ""
}

// modesOf lists the modes every pane can show.
func (a *App) modesOf(panes []*pane) []string {
	if len(panes) == 0 {
		return nil
	}
	var out []string
	for _, m := range panes[0].modes {
		all := true
		for _, p := range panes[1:] {
			if !slices.Contains(p.modes, m) {
				all = false
			}
		}
		if all {
			out = append(out, m)
		}
	}
	return out
}

// layoutsOf lists the layouts the shown content offers.
func (a *App) layouts() []string {
	switch a.kind {
	case viewer.KindTask:
		out := []string{viewer.LayoutSingle}
		if a.inputs != nil && len(a.inputs.items) > 0 {
			out = append(out, viewer.LayoutInputs)
		}
		if len(a.textures) > 0 {
			out = append(out, viewer.LayoutTextures)
		}
		return out
	case viewer.KindCompare:
		return []string{viewer.LayoutCompare, viewer.LayoutSingle}
	case viewer.KindInputSet, viewer.KindTaskInputs:
		return []string{viewer.LayoutInputs}
	case viewer.KindTextures, viewer.KindImages:
		return []string{viewer.LayoutTextures}
	case viewer.KindModelFile:
		out := []string{viewer.LayoutSingle}
		if len(a.textures) > 0 {
			out = append(out, viewer.LayoutTextures)
		}
		return out
	}
	return nil
}

func defaultLayout(kind string) string {
	switch kind {
	case viewer.KindCompare:
		return viewer.LayoutCompare
	case viewer.KindInputSet, viewer.KindTaskInputs:
		return viewer.LayoutInputs
	case viewer.KindTextures, viewer.KindImages:
		return viewer.LayoutTextures
	}
	return viewer.LayoutSingle
}

func (a *App) conflict(msg, hint string, fields map[string]any) viewer.StateReply {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["revision"] = a.rev
	return a.refusal(&viewer.Error{Code: render.CodeConflict, Message: msg, Hint: hint, Fields: fields})
}

func (a *App) invalid(msg, hint string) viewer.StateReply {
	return a.refusal(&viewer.Error{Code: render.CodeInvalidInput, Message: msg, Hint: hint})
}

// staleRevision refuses a call made against an older state.
func (a *App) staleRevision(expected *int64) (viewer.StateReply, bool) {
	if expected == nil || *expected == a.rev {
		return viewer.StateReply{}, false
	}
	return a.conflict(fmt.Sprintf("The view changed since revision %d (now %d); the call was not applied.", *expected, a.rev),
		"Read the state in this reply (get_viewer_state shows the user's actions), then call again with expected_revision set to the current revision.",
		map[string]any{"user_actions": a.actionsSince(*expected)}), true
}

// applyShow puts prepared content on screen (game loop).
func (a *App) applyShow(p *prepared, params viewer.ShowParams) viewer.StateReply {
	if rep, stale := a.staleRevision(params.ExpectedRevision); stale {
		return rep
	}
	var panes []*pane
	for i, vt := range p.tasks {
		if p.kind == viewer.KindTaskInputs || p.kind == viewer.KindTextures {
			break
		}
		panes = append(panes, &pane{index: i, kind: viewer.KindTask, vt: vt, stage: p.stages[i], modes: p.stages[i].Modes, div: 2})
	}
	if p.kind == viewer.KindModelFile {
		panes = []*pane{{index: 0, kind: viewer.KindModelFile, file: p.file, div: 2}}
	}
	modes := a.modesOf(panes)
	mode := a.mode
	if params.Mode != "" {
		if p.kind == viewer.KindModelFile {
			if !render3d.Mode(params.Mode).Known() {
				return a.invalid(fmt.Sprintf("Unknown mode %q.", params.Mode), "Use a mode from get_generation_options preview_modes.")
			}
		} else if !slices.Contains(modes, params.Mode) {
			return a.conflict(fmt.Sprintf("Mode %s is not available for this content.", params.Mode), "Use one of: "+strings.Join(modes, ", ")+".",
				map[string]any{"available_modes": modes})
		}
		mode = params.Mode
	} else if !slices.Contains(modes, mode) {
		mode = defaultMode(modes)
	}
	var cam *camState
	if params.View != "" {
		c, ok := namedCam(params.View)
		if !ok {
			return a.invalid(fmt.Sprintf("Unknown view %q.", params.View), "Use one of: "+strings.Join(viewer.Views, ", ")+".")
		}
		cam = &c
	}

	a.rememberCamera()
	a.contentGen++
	a.rend.dropAll()
	a.kind = p.kind
	a.panes = panes
	a.mode = mode
	a.strip, a.textures, a.inputs = nil, nil, nil
	a.texSel, a.texZoom, a.texPan = 0, 1, [2]float64{}
	a.flat = -1
	a.selected = viewer.SelectedState{}
	a.highlight = nil
	a.popup = -1
	a.info = viewer.ContentInfo{Kind: p.kind}
	switch p.kind {
	case viewer.KindTask, viewer.KindTextures, viewer.KindTaskInputs:
		vt := p.tasks[0]
		a.info.TaskID, a.info.Prompt = vt.UUID, vt.Prompt
	case viewer.KindCompare:
		a.info.Tasks = []string{p.tasks[0].UUID, p.tasks[1].UUID}
	case viewer.KindModelFile:
		a.info.File = p.file
	case viewer.KindImages:
		a.info.Files = p.images
	}
	if p.inputSet != nil {
		a.info.InputSetID = p.inputSet.ID
		a.inputs = &inputModel{id: p.inputSet.ID, editable: true, fromTask: p.inputSet.FromTask}
		for _, im := range p.inputSet.Images {
			a.inputs.items = append(a.inputs.items, &thumb{kind: "input", name: filepath.Base(im.Path), view: im.View, file: im.Path, weight: im.Weight})
		}
	}
	a.layout = defaultLayout(p.kind)
	switch {
	case cam != nil:
		a.cam, a.viewName = *cam, params.View
	default:
		a.cam, a.viewName = a.cameraFor(), "three_quarter"
	}
	a.turntable = false
	a.setTitle()
	a.bump()
	a.startLoads(p)
	return viewer.StateReply{State: a.snapshot(), PID: os.Getpid()}
}

// cameraFor returns the remembered camera of the shown task, or the default.
func (a *App) cameraFor() camState {
	if a.kind == viewer.KindTask && len(a.panes) == 1 && a.panes[0].vt != nil {
		if c, ok := a.cameras[a.panes[0].vt.UUID]; ok {
			return c
		}
	}
	return defaultCam()
}

// rememberCamera keeps the camera of the shown task for the next time.
func (a *App) rememberCamera() {
	if a.kind == viewer.KindTask && len(a.panes) == 1 && a.panes[0].vt != nil {
		a.cameras[a.panes[0].vt.UUID] = a.cam
	}
}

func (a *App) setTitle() {
	title := "Rodin viewer"
	switch {
	case a.info.Prompt != "":
		title += " - " + clipText(a.info.Prompt, 60)
	case a.info.File != "":
		title += " - " + filepath.Base(a.info.File)
	case a.kind == viewer.KindCompare:
		title += " - compare"
	case a.kind == viewer.KindInputSet:
		title += " - input set " + a.info.InputSetID
	}
	ebiten.SetWindowTitle(title)
}

// applyControl changes the view (game loop). Every field is checked before
// any is applied.
func (a *App) applyControl(c viewer.Control) viewer.StateReply {
	if rep, stale := a.staleRevision(c.ExpectedRevision); stale {
		return rep
	}
	has3D := len(a.panes) > 0
	need3D := func(what string) (viewer.StateReply, bool) {
		if has3D {
			return viewer.StateReply{}, false
		}
		return a.conflict("The shown content has no 3D view, so "+what+" does not apply.", "Show a task or a model file first.", nil), true
	}
	// Plan the change on copies.
	layout, mode, cam, view := a.layout, a.mode, a.cam, a.viewName
	var newStage *service.ViewStage
	if c.Layout != nil {
		if !slices.Contains(a.layouts(), *c.Layout) {
			return a.conflict(fmt.Sprintf("Layout %s is not available for this content.", *c.Layout), "Use one of: "+strings.Join(a.layouts(), ", ")+".",
				map[string]any{"layouts": a.layouts()})
		}
		layout = *c.Layout
	}
	modes := a.modesOf(a.panes)
	if c.Stage != nil || c.Version != nil {
		if a.kind != viewer.KindTask || len(a.panes) != 1 {
			return a.conflict("Stage and version apply to a single task; compared panes keep the stages they were shown with.", "Call show_in_viewer with other stages to change them.", nil)
		}
		p := a.panes[0]
		name := p.stage.Name
		if c.Stage != nil {
			name = *c.Stage
		}
		version := ""
		if c.Version != nil {
			version = *c.Version
		} else if c.Stage == nil {
			version = p.stage.Version
		}
		st, err := pickStage(p.vt, name, version)
		if err != nil {
			e := errorOf(err)
			e.Fields = withRevision(e.Fields, a.rev)
			return a.refusal(e)
		}
		newStage = &st
		modes = st.Modes
		if !slices.Contains(modes, mode) {
			mode = defaultMode(modes)
		}
	}
	if c.Mode != nil {
		if r, no := need3D("mode"); no {
			return r
		}
		if !slices.Contains(modes, *c.Mode) {
			return a.conflict(fmt.Sprintf("Mode %s is not available here.", *c.Mode), "Use one of: "+strings.Join(modes, ", ")+".", map[string]any{"available_modes": modes})
		}
		mode = *c.Mode
	}
	if c.View != nil {
		if r, no := need3D("view"); no {
			return r
		}
		v, ok := namedCam(*c.View)
		if !ok {
			return a.invalid(fmt.Sprintf("Unknown view %q.", *c.View), "Use one of: "+strings.Join(viewer.Views, ", ")+".")
		}
		cam, view = v, *c.View
	}
	if c.ResetCamera {
		if r, no := need3D("reset_camera"); no {
			return r
		}
		cam, view = defaultCam(), "three_quarter"
	}
	if c.Orbit != nil {
		if r, no := need3D("orbit"); no {
			return r
		}
		if az := c.Orbit.AzimuthDeg; az != nil {
			if !finite(*az) {
				return a.invalid("orbit.azimuth_deg must be a number.", "Pass degrees, 0 = front, 90 = right.")
			}
			cam.Az = wrapAz(*az)
		}
		if el := c.Orbit.ElevationDeg; el != nil {
			if !finite(*el) || *el < -90 || *el > 90 {
				return a.invalid("orbit.elevation_deg must be from -90 to 90.", "Pass degrees above the horizon.")
			}
			cam.El = *el
		}
		view = "custom"
	}
	if c.Zoom != nil {
		if r, no := need3D("zoom"); no {
			return r
		}
		if !finite(*c.Zoom) || *c.Zoom < 0.1 || *c.Zoom > 10 {
			return a.invalid("zoom must be from 0.1 to 10 (1 fits the model).", "Pass a zoom in range.")
		}
		cam.Zoom = *c.Zoom
		view = "custom"
	}
	if c.Pan != nil {
		if r, no := need3D("pan"); no {
			return r
		}
		if !finite(c.Pan.X) || !finite(c.Pan.Y) || math.Abs(c.Pan.X) > 10 || math.Abs(c.Pan.Y) > 10 {
			return a.invalid("pan x and y must be from -10 to 10 (model sizes).", "Pass a smaller pan.")
		}
		cam.Pan = [2]float64{c.Pan.X, c.Pan.Y}
		view = "custom"
	}
	if c.Turntable != nil || c.WireframeOverlay != nil {
		if r, no := need3D("turntable and wireframe_overlay"); no {
			return r
		}
	}
	if c.Background != nil {
		if r, no := need3D("background"); no {
			return r
		}
		if !slices.Contains(viewer.Backgrounds, *c.Background) {
			return a.invalid(fmt.Sprintf("Unknown background %q.", *c.Background), "Use default, light or dark.")
		}
	}
	if s := c.Select; s != nil {
		if s.ImageIndex != nil && (*s.ImageIndex < -1 || *s.ImageIndex >= a.imageCount(layout)) {
			return a.conflict(fmt.Sprintf("There is no image %d in the %s layout (it has %d).", *s.ImageIndex, layout, a.imageCount(layout)), "Use an index from the state's strip or input_set (or -1 to clear).", nil)
		}
		if s.TextureMap != nil && a.textureIndex(*s.TextureMap) < 0 {
			return a.conflict(fmt.Sprintf("There is no texture %q.", *s.TextureMap), "Use one of: "+strings.Join(a.textureNames(), ", ")+".", nil)
		}
		if s.Pane != nil && (*s.Pane < 0 || *s.Pane >= len(a.panes)) {
			return a.conflict(fmt.Sprintf("There is no pane %d.", *s.Pane), fmt.Sprintf("Panes are 0 to %d.", len(a.panes)-1), nil)
		}
	}
	if c.SetTag != nil || c.Reorder != nil {
		if a.inputs == nil || !a.inputs.editable {
			return a.conflict("Only a shown input set can be retagged or reordered; a task's own inputs are read-only.",
				"Call show_in_viewer with task_inputs to make an editable copy, or with input_set.", nil)
		}
		n := len(a.inputs.items)
		if t := c.SetTag; t != nil {
			if t.ImageIndex < 0 || t.ImageIndex >= n {
				return a.conflict(fmt.Sprintf("There is no image %d (the set has %d).", t.ImageIndex, n), "Use an index from input_set.images.", nil)
			}
			if !slices.Contains(viewer.ViewTags, t.View) {
				return a.invalid(fmt.Sprintf("Unknown view tag %q.", t.View), "Use one of "+strings.Join(viewer.ViewTags, " ")+".")
			}
			if t.Weight != nil && (!finite(*t.Weight) || *t.Weight < 0 || *t.Weight > 1) {
				return a.invalid("weight must be from 0 to 1.", "Pass a weight in range.")
			}
		}
		if r := c.Reorder; r != nil && (r.From < 0 || r.From >= n || r.To < 0 || r.To >= n) {
			return a.conflict(fmt.Sprintf("reorder %d -> %d is outside the set's %d images.", r.From, r.To, n), "Use indexes from input_set.images.", nil)
		}
	}
	if h := c.Highlight; h != nil {
		if h.ImageIndex != nil && (*h.ImageIndex < 0 || *h.ImageIndex >= a.imageCount(layout)) {
			return a.conflict(fmt.Sprintf("There is no image %d to highlight.", *h.ImageIndex), "Use an index from the state's strip or input_set.", nil)
		}
		if h.View != nil && !slices.Contains(viewer.ViewTags, *h.View) {
			return a.invalid(fmt.Sprintf("Unknown view tag %q.", *h.View), "Use one of "+strings.Join(viewer.ViewTags, " ")+".")
		}
	}

	// Apply. A change of the 3D view leaves a strip picture shown full size.
	if a.flat >= 0 && (newStage != nil || c.Mode != nil || c.View != nil || c.ResetCamera || c.Orbit != nil || c.Zoom != nil || c.Pan != nil ||
		c.Turntable != nil || c.WireframeOverlay != nil || c.Background != nil) && (c.Select == nil || c.Select.ImageIndex == nil) {
		a.selectImage(-1)
	}
	if newStage != nil {
		a.setStage(*newStage)
	}
	a.layout, a.mode = layout, mode
	if cam != a.cam || view != a.viewName {
		a.cam, a.viewName = cam, view
		a.lastChange = time.Now()
	}
	if c.Turntable != nil {
		a.turntable = *c.Turntable
	}
	if c.WireframeOverlay != nil {
		a.overlay = *c.WireframeOverlay
	}
	if c.Background != nil {
		a.background = *c.Background
	}
	if s := c.Select; s != nil {
		if s.ImageIndex != nil {
			a.selectImage(*s.ImageIndex)
		}
		if s.TextureMap != nil {
			a.texSel = a.textureIndex(*s.TextureMap)
			a.selected.TextureMap = *s.TextureMap
		}
		if s.Pane != nil {
			a.selected.Pane = *s.Pane
		}
	}
	if t := c.SetTag; t != nil {
		it := a.inputs.items[t.ImageIndex]
		it.view = t.View
		if t.Weight != nil {
			w := *t.Weight
			it.weight = &w
		}
		a.saveInputs()
	}
	if r := c.Reorder; r != nil {
		a.reorder(r.From, r.To)
	}
	if c.ClearHighlight {
		a.highlight = nil
	}
	if c.Highlight != nil {
		h := *c.Highlight
		a.highlight = &h
		a.highlightAt = time.Now()
	}
	if c.Message != nil {
		a.message = *c.Message
	}
	a.bump()
	return viewer.StateReply{State: a.snapshot(), PID: os.Getpid()}
}

func withRevision(f map[string]any, rev int64) map[string]any {
	if f == nil {
		f = map[string]any{}
	}
	f["revision"] = rev
	return f
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// setStage switches the single task pane to another stage (or version) and
// loads its mesh.
func (a *App) setStage(st service.ViewStage) {
	p := a.panes[0]
	if p.stage.Name == st.Name && p.stage.Version == st.Version {
		return
	}
	p.stage, p.modes = st, st.Modes
	a.rend.drop(p)
	p.gen++
	p.mesh, p.simple, p.maps, p.img, p.hasShown, p.hasQueued, p.renderErr = nil, nil, nil, nil, false, false, ""
	a.flat = -1
	a.loadPane(p)
	a.loadStrip(p.vt, st)
	if !slices.Contains(p.modes, a.mode) {
		a.mode = defaultMode(p.modes)
	}
}

// imageCount is how many images select and highlight can address in a
// layout: the input set's in inputs, the strip's elsewhere.
func (a *App) imageCount(layout string) int {
	if layout == viewer.LayoutInputs {
		if a.inputs == nil {
			return 0
		}
		return len(a.inputs.items)
	}
	return len(a.strip)
}

func (a *App) selectImage(i int) {
	if i < 0 {
		a.selected.ImageIndex = nil
		a.flat = -1
		return
	}
	a.selected.ImageIndex = &i
	if a.layout != viewer.LayoutInputs && i < len(a.strip) {
		a.flat = i
		a.loadFlat(a.strip[i])
	}
}

func (a *App) textureNames() []string {
	out := make([]string, len(a.textures))
	for i, t := range a.textures {
		out[i] = t.name
	}
	return out
}

func (a *App) textureIndex(name string) int {
	for i, t := range a.textures {
		if t.name == name {
			return i
		}
	}
	return -1
}

// reorder moves input from to position to and saves the set.
func (a *App) reorder(from, to int) {
	items := a.inputs.items
	it := items[from]
	items = append(items[:from], items[from+1:]...)
	items = append(items[:to], append([]*thumb{it}, items[to:]...)...)
	a.inputs.items = items
	a.saveInputs()
}

// saveInputs stores the shown input set as it now stands.
func (a *App) saveInputs() {
	if a.inputs == nil || !a.inputs.editable {
		return
	}
	set := service.InputSet{ID: a.inputs.id, FromTask: a.inputs.fromTask}
	for _, it := range a.inputs.items {
		set.Images = append(set.Images, viewer.InputImage{Path: it.file, View: it.view, Weight: it.weight})
	}
	if err := a.svc.SaveInputSet(set); err != nil {
		a.message = "Could not save the input set: " + err.Error()
	}
}

// bump marks a change: a new revision and a redraw.
func (a *App) bump() {
	a.rev++
	a.dirty()
}

// userAction records a change the user made in the window.
func (a *App) userAction(action, detail string) {
	a.bump()
	a.actions = append(a.actions, viewer.UserAction{Revision: a.rev, At: time.Now().UTC().Format(time.RFC3339), Action: action, Detail: detail})
	if len(a.actions) > maxActions {
		a.actions = a.actions[len(a.actions)-maxActions:]
	}
}

func (a *App) actionsSince(rev int64) []viewer.UserAction {
	var out []viewer.UserAction
	for _, x := range a.actions {
		if x.Revision > rev {
			out = append(out, x)
		}
	}
	return out
}

// quality reports whether the screen shows full frames of loaded content.
func (a *App) quality() string {
	for _, p := range a.visiblePanes() {
		if p.status == "loading" || p.mapsWanted && modeNeedsMaps(a.mode) {
			return viewer.QualityInteractive
		}
		if p.status == "ready" && p.renderErr == "" && (!p.hasShown || p.shown.interactive || p.shown != a.fullKey(p)) {
			return viewer.QualityInteractive
		}
	}
	if a.turntable && len(a.visiblePanes()) > 0 {
		return viewer.QualityInteractive
	}
	if a.pendingImages() > 0 {
		return viewer.QualityInteractive
	}
	return viewer.QualityFull
}

// snapshot is the state as the tools see it.
func (a *App) snapshot() viewer.State {
	st := viewer.State{
		Revision: a.rev, Content: a.info, Layout: a.layout, Layouts: a.layouts(), Mode: a.mode, View: a.viewName,
		Overlay: a.overlay, Background: a.background, Selected: a.selected, Highlight: a.highlight, Message: a.message,
		Quality: a.quality(), Window: viewer.WindowInfo{W: a.sw, H: a.sh, Scale: a.scale}, Panes: []viewer.PaneState{},
	}
	if st.Content.Kind == "" {
		st.Content.Kind = viewer.KindNone
	}
	if st.Layouts == nil {
		st.Layouts = []string{}
	}
	if len(a.panes) > 0 {
		st.Modes = a.modesOf(a.panes)
		cam := viewer.CameraState{AzimuthDeg: round2(a.cam.Az), ElevationDeg: round2(a.cam.El), Zoom: round2(a.cam.Zoom), Pan: a.cam.Pan, FOV: 45, Turntable: a.turntable}
		if p := a.panes[0]; p.mesh != nil {
			aspect := 1.0
			if a.sh > 0 {
				aspect = float64(a.sw) / float64(a.sh)
			}
			eye, target, fov := p.frame.Pose(a.cam.r3d(), aspect)
			cam.Target = [3]float64{round4(target[0]), round4(target[1]), round4(target[2])}
			cam.Distance = round4(math.Sqrt((eye[0]-target[0])*(eye[0]-target[0]) + (eye[1]-target[1])*(eye[1]-target[1]) + (eye[2]-target[2])*(eye[2]-target[2])))
			cam.FOV = fov
		}
		st.Camera = cam
	} else {
		st.View, st.Mode = "", ""
	}
	if a.kind == viewer.KindTask && len(a.panes) == 1 && a.panes[0].vt != nil {
		p := a.panes[0]
		st.Stage, st.Stages = p.stage.Name, meshStages(p.vt)
		if p.stage.Name == "geometry_draft" {
			st.Version, st.Versions = p.stage.Version, p.vt.Versions
		}
	}
	for _, p := range a.panes {
		ps := viewer.PaneState{Index: p.index, Kind: p.kind, File: p.file, Modes: p.modes, Status: p.status, Error: p.err}
		if ps.Modes == nil {
			ps.Modes = []string{}
		}
		if p.renderErr != "" && ps.Error == "" {
			ps.Error = p.renderErr
		}
		if p.vt != nil {
			ps.TaskID, ps.Prompt, ps.Stage = p.vt.UUID, p.vt.Prompt, p.stage.Name
			if p.stage.Name == "geometry_draft" {
				ps.Version = p.stage.Version
			}
		}
		if p.mesh != nil {
			ps.Triangles = len(p.mesh.Idx) / 3
		}
		st.Panes = append(st.Panes, ps)
	}
	if a.inputs != nil {
		is := &viewer.InputSetState{ID: a.inputs.id, Editable: a.inputs.editable, FromTask: a.inputs.fromTask, Images: []viewer.InputSetImage{}}
		for i, it := range a.inputs.items {
			is.Images = append(is.Images, viewer.InputSetImage{Index: i, File: it.file, View: it.view, Weight: it.weight})
		}
		st.InputSet = is
	}
	for i, t := range a.strip {
		st.Strip = append(st.Strip, viewer.StripImage{Index: i, Kind: t.kind, Name: t.name, View: t.view})
	}
	st.Textures = a.textureNames()
	if len(st.Textures) == 0 {
		st.Textures = nil
	}
	return st
}

func meshStages(vt *service.ViewTask) []string {
	var out []string
	for _, s := range vt.Stages {
		if s.HasMesh() {
			out = append(out, s.Name)
		}
	}
	return out
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

// raise brings the window to the front.
func (a *App) raise() {
	if ebiten.IsWindowMinimized() {
		ebiten.RestoreWindow()
	}
	ebiten.SetWindowFloating(true)
	a.raiseStep = 2
	ebiten.RequestAttention()
}

// Cameras: the last camera per task, kept across windows.

func (a *App) loadCameras() {
	a.cameras = map[string]camState{}
	b, err := os.ReadFile(a.paths().ViewerCameras)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &a.cameras)
	if a.cameras == nil {
		a.cameras = map[string]camState{}
	}
}

func (a *App) saveCameras() {
	a.rememberCamera()
	b, err := json.Marshal(a.cameras)
	if err != nil {
		return
	}
	_ = domain.WriteFileAtomic(a.paths().ViewerCameras, b, 0o600)
}

// fail and failFields build service errors (classified by errorOf).
func fail(code, msg, hint string) error {
	return &service.Failure{E: render.Error{Code: code, Message: msg, Hint: hint}}
}

func failFields(code, msg, hint string, fields map[string]any) error {
	return &service.Failure{E: render.Error{Code: code, Message: msg, Hint: hint, Fields: fields}}
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
