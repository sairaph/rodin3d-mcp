package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/draco"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/store"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// What the viewer window reads through the service: a task's stages with
// their meshes, maps and server images, its input images, local model and
// image files, and input sets.

// ViewTask is a task as the viewer shows it: every stage that has a mesh or
// images, read once (card and version history).
type ViewTask struct {
	UUID           string
	Prompt         string
	TaskStage      string // the task's own stage (stage package)
	Current        string // the preview stage of the task's own stage
	Stages         []ViewStage
	Versions       []string // geometry versions, oldest first
	DefaultVersion string   // the version the draft stage shows by default
	Inputs         []ViewInput
	d              *taskDetail
}

// ViewStage is one stage of a ViewTask.
type ViewStage struct {
	Name    string   // assets.Stage*
	Version string   // geometry_draft: the geometry version shown
	Modes   []string // render modes; none when the stage has no mesh
	Draft   bool     // the mesh is the Draco draft
	Server  []ViewAsset
	Maps    []ViewAsset // material stage: texture_diffuse, _pbr, _normal, _shaded
	mesh    assets.Asset
	hasMesh bool
}

// HasMesh reports whether the stage can be drawn in 3D.
func (s ViewStage) HasMesh() bool { return s.hasMesh }

// ViewAsset is a server file of a stage.
type ViewAsset struct {
	Mode  string // poster, geometry_image, material_image, texture_*
	asset assets.Asset
}

// ViewInput is one input image of a task with its stored tag and weight.
type ViewInput struct {
	Name   string
	View   string
	Weight *float64
	asset  assets.Asset
}

// Stage returns the named stage of t.
func (t *ViewTask) Stage(name string) (ViewStage, bool) {
	for _, s := range t.Stages {
		if s.Name == name {
			return s, true
		}
	}
	return ViewStage{}, false
}

// StageNames lists the stages of t.
func (t *ViewTask) StageNames() []string {
	out := make([]string, len(t.Stages))
	for i, s := range t.Stages {
		out[i] = s.Name
	}
	return out
}

// DraftVersion returns the geometry draft stage at geometry version v.
func (t *ViewTask) DraftVersion(v string) (ViewStage, error) {
	base, ok := t.Stage(assets.StageGeometryDraft)
	if !ok {
		return ViewStage{}, fail(render.CodeConflict, "This task has no geometry draft to show.", "Show another stage of it.")
	}
	if v == "" || v == base.Version {
		return base, nil
	}
	if !slices.Contains(t.Versions, v) {
		return ViewStage{}, failFields(render.CodeConflict, fmt.Sprintf("The task has no geometry version %q.", v),
			"Use one of the versions listed: "+strings.Join(t.Versions, ", ")+".", map[string]any{"versions": t.Versions})
	}
	mesh, poster := assets.GeometryDraftAt(t.d.hist, v)
	if mesh.URL == "" {
		return ViewStage{}, fail(render.CodeConflict, fmt.Sprintf("Geometry version %s of this task has no draft mesh.", v), "Show the task's own version.")
	}
	st := base
	st.Version, st.mesh = v, mesh
	st.Server = nil
	if poster.URL != "" {
		st.Server = []ViewAsset{{Mode: assets.ModePoster, asset: poster}}
	}
	return st, nil
}

// ViewTask reads a task for the viewer: its card and version history, every
// stage with a mesh or server images, its versions and input images. No
// mesh or image is downloaded yet.
func (s *Service) ViewTask(ctx context.Context, id string) (*ViewTask, error) {
	uuid, err := s.resolveTask(id)
	if err != nil {
		return nil, err
	}
	d, err := s.loadDetail(ctx, uuid, true)
	if err != nil {
		return nil, err
	}
	t := &ViewTask{UUID: uuid, Prompt: d.card.Prompt, TaskStage: d.status.Stage, Current: assets.PreviewStage(d.status.Stage), d: d}
	t.Versions = assets.SortVersions(keysOf(d.hist.Geometry))
	_, textured := assets.Find(d.list, assets.StageMaterial, assets.ModeTextureDiffuse)
	for _, stg := range assets.Stages {
		st := ViewStage{Name: stg, Draft: stg == assets.StageGeometryDraft}
		if a, ok := assets.MeshAsset(d.card, d.hist, stg); ok {
			st.mesh, st.hasMesh = a, true
			for _, m := range render3d.ModesFor(st.Draft, stg == assets.StageMaterial && textured) {
				st.Modes = append(st.Modes, string(m))
			}
			if st.Draft {
				st.Version = a.Version
				t.DefaultVersion = a.Version
			}
		}
		for _, m := range []string{assets.ModePoster, assets.ModeGeometryImage, assets.ModeMaterialImage} {
			if a, ok := assets.Find(d.list, stg, m); ok {
				st.Server = append(st.Server, ViewAsset{Mode: m, asset: a})
			}
		}
		for _, k := range viewer.TextureMaps {
			if a, ok := assets.Find(d.list, stg, "texture_"+k); ok {
				st.Maps = append(st.Maps, ViewAsset{Mode: "texture_" + k, asset: a})
			}
		}
		if st.hasMesh || len(st.Server) > 0 {
			t.Stages = append(t.Stages, st)
		}
	}
	names, inputs := assets.Inputs(d.card, sortInputNames)
	labels, _ := d.card.Settings["image_label"].([]any)
	weights, _ := d.card.Settings["view_weights"].([]any)
	for i, n := range names {
		in := ViewInput{Name: n, View: "?", asset: inputs[i]}
		if i < len(labels) {
			if l, ok := labels[i].(string); ok && l != "" {
				in.View = l
			}
		}
		if i < len(weights) {
			if w, ok := weights[i].(float64); ok {
				in.Weight = &w
			}
		}
		t.Inputs = append(t.Inputs, in)
	}
	return t, nil
}

// ViewFile returns the cached file of a server image or input image of
// task uuid, downloading it when needed.
func (s *Service) ViewFile(ctx context.Context, uuid string, a ViewAsset) (string, error) {
	return s.cachedFile(ctx, uuid, a.asset)
}

// ViewInputFile returns the cached file of an input image.
func (s *Service) ViewInputFile(ctx context.Context, uuid string, in ViewInput) (string, error) {
	return s.cachedFile(ctx, uuid, in.asset)
}

func (s *Service) cachedFile(ctx context.Context, uuid string, a assets.Asset) (string, error) {
	cache, err := s.assetCache()
	if err != nil {
		return "", err
	}
	f, _, err := cache.Get(ctx, uuid, a)
	return f, err
}

// ViewMesh downloads (when needed) and decodes the stage's mesh; the decode
// is kept as a mesh file beside the download, as for get_preview.
func (s *Service) ViewMesh(ctx context.Context, uuid string, st ViewStage) (*render3d.Mesh, error) {
	if !st.hasMesh {
		return nil, fail(render.CodeConflict, "The "+st.Name+" stage of this task has no mesh.", "Show another stage.")
	}
	file, err := s.cachedFile(ctx, uuid, st.mesh)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if st.Draft && !draco.IsDraco(src) {
		return nil, fail(render.CodeConflict, "The geometry draft of this task is not a Draco mesh (an older preview format), so it cannot be drawn.",
			"Show its poster in the image strip, or a later stage.")
	}
	m, _, err := s.stageMesh(ctx, file, src, sum(src), st.Draft)
	return m, err
}

// ViewMaps downloads (when needed) and decodes the stage's texture maps for
// the textured modes; nil when the stage has none.
func (s *Service) ViewMaps(ctx context.Context, uuid string, st ViewStage) (*render3d.Maps, error) {
	var files []mapFile
	for _, a := range st.Maps {
		f, err := s.cachedFile(ctx, uuid, a.asset)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		files = append(files, mapFile{kind: strings.TrimPrefix(a.Mode, "texture_"), data: b, sum: sum(b)})
	}
	return s.stageMaps(files)
}

// ViewModel reads a local model file.
func (s *Service) ViewModel(ctx context.Context, path string) (*modelfile.Model, error) {
	return modelfile.Load(ctx, path, s.decoder)
}

// ModelModes lists the render modes of a local model: the confirmed-geometry
// set, without the modes that need texture coordinates when it has none,
// and with pbr, shaded and toon in front when it has a textured material.
func ModelModes(m *modelfile.Model) []string {
	var out []string
	for _, mode := range render3d.ModesFor(false, m.Maps != nil) {
		if m.Mesh.UV == nil && (mode == render3d.ModeCopper || mode == render3d.ModeUVChecker) {
			continue
		}
		out = append(out, string(mode))
	}
	return out
}

// CheckModelFile checks a model_file before the window loads it.
func CheckModelFile(path string) error {
	if !filepath.IsAbs(path) {
		return fail(render.CodeInvalidInput, "model_file must be an absolute path.", "Show it again with the file's absolute path.")
	}
	if !slices.Contains(modelfile.Extensions, strings.ToLower(filepath.Ext(path))) {
		return fail(render.CodeInvalidInput, fmt.Sprintf("%s is not a model file the viewer reads (%s).", filepath.Base(path), strings.Join(modelfile.Extensions, ", ")),
			"Convert it to one of those, or use images for pictures.")
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return fail(render.CodeNotFound, "No model file at "+path+".", "Check the path.")
	}
	return nil
}

// CheckImageFiles checks image paths: absolute, existing, png, jpeg or webp.
func CheckImageFiles(paths []string) error {
	if len(paths) == 0 {
		return fail(render.CodeInvalidInput, "images is empty.", "Give one or more absolute paths of png, jpeg or webp files.")
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return fail(render.CodeInvalidInput, "Image paths must be absolute; "+p+" is not.", "Pass absolute paths.")
		}
		f, err := os.Open(p)
		if err != nil {
			return fail(render.CodeNotFound, "No image at "+p+".", "Check the path.")
		}
		head := make([]byte, 16)
		n, _ := f.Read(head)
		f.Close()
		if imaging.Sniff(head[:n]) == "" {
			return fail(render.CodeInvalidInput, p+" is not a png, jpeg or webp image.", "Pass png, jpeg or webp files.")
		}
	}
	return nil
}

// InputSet is a set of input images with view tags and weights, as the
// viewer arranges it and generate_model's input_set reads it.
type InputSet struct {
	ID       string              `json:"id"`
	Images   []viewer.InputImage `json:"images"`
	FromTask string              `json:"from_task,omitempty"` // copied from this task's inputs
}

// NewInputSetID returns a fresh input set id.
func NewInputSetID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "is-" + hex.EncodeToString(b)
}

// CheckInputSet checks the images of an input set the way generate_model
// checks its images: 1-5 files of png, jpeg or webp, known view tags,
// weights above 0 and at most 1, on every image or on none.
func CheckInputSet(images []viewer.InputImage) error {
	if len(images) == 0 || len(images) > MaxImages {
		return fail(render.CodeInvalidInput, fmt.Sprintf("An input set holds 1 to %d images; got %d.", MaxImages, len(images)), "Pass 1 to 5 images.")
	}
	paths := make([]string, len(images))
	weighted := 0
	for i, im := range images {
		paths[i] = im.Path
		if im.View != "" && !slices.Contains(viewer.ViewTags, im.View) {
			return fail(render.CodeInvalidInput, fmt.Sprintf("Image %d has the view tag %q.", i, im.View), "Use one of "+strings.Join(viewer.ViewTags, " ")+".")
		}
		if im.Weight != nil {
			if w := *im.Weight; !(w > 0 && w <= 1) {
				return fail(render.CodeInvalidInput, fmt.Sprintf("Image %d has the weight %g.", i, w), "Weights are above 0 and at most 1; they are normalised to sum 1.")
			}
			weighted++
		}
	}
	if weighted != 0 && weighted != len(images) {
		return fail(render.CodeInvalidInput, fmt.Sprintf("%d of %d images have a weight.", weighted, len(images)), "Give every image a weight, or none for the default weights.")
	}
	return CheckImageFiles(paths)
}

// SaveInputSet stores an input set.
func (s *Service) SaveInputSet(set InputSet) error {
	st, err := s.Store()
	if err != nil {
		return err
	}
	data, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return st.SaveInputSet(set.ID, data)
}

// LoadInputSet reads a stored input set.
func (s *Service) LoadInputSet(id string) (InputSet, error) {
	st, err := s.Store()
	if err != nil {
		return InputSet{}, err
	}
	data, err := st.InputSet(strings.TrimSpace(id))
	if errors.Is(err, store.ErrNotFound) {
		return InputSet{}, fail(render.CodeNotFound, fmt.Sprintf("No input set %q.", id), "Show the images in the viewer (show_in_viewer with input_set images) to make one; its id is in the reply.")
	}
	if err != nil {
		return InputSet{}, err
	}
	var set InputSet
	if err := json.Unmarshal(data, &set); err != nil {
		return InputSet{}, wrapf(err, "read input set %s", id)
	}
	return set, nil
}

// Decoder returns the shared Draco decoder.
func (s *Service) Decoder() (*draco.Decoder, error) { return s.decoder() }
