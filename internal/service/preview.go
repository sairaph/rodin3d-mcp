package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// Preview sizes (longer edge, pixels).
const (
	PreviewMin     = 256
	PreviewMax     = 1024
	PreviewDefault = 512
)

// PreviewInput is the input of get_preview.
type PreviewInput struct {
	TaskID string
	Stage  string // current | geometry_draft | geometry | material
	Mode   string // a server image mode (assets.AllModes) or a render mode (render3d.AllModes)
	View   string // renders only: a render3d view; "" is three_quarter
	Size   int
	SaveTo string
}

// PreviewModes lists every get_preview mode: the server images, then the
// local renders.
func PreviewModes() []string {
	out := append([]string(nil), assets.AllModes...)
	for _, m := range render3d.AllModes {
		out = append(out, string(m))
	}
	return out
}

// RenderModes lists every local render mode.
func RenderModes() []string {
	out := make([]string, len(render3d.AllModes))
	for i, m := range render3d.AllModes {
		out[i] = string(m)
	}
	return out
}

// PreviewViews lists the views of a render, the default first.
func PreviewViews() []string {
	out := make([]string, len(render3d.Views))
	for i, v := range render3d.Views {
		out[i] = string(v)
	}
	return out
}

// serverShows says what each server image is.
var serverShows = map[string]string{
	assets.ModePoster:         "the server's poster of the stage (its render of the model)",
	assets.ModeGeometryImage:  "the server's render of the untextured geometry",
	assets.ModeMaterialImage:  "the server's render of the textured model",
	assets.ModeTextureDiffuse: "texture map: the base colour atlas, a flat UV layout, not a view of the model",
	assets.ModeTexturePBR:     "texture map: the PBR atlas (green roughness, blue metalness), a flat UV layout, not a view of the model",
	assets.ModeTextureNormal:  "texture map: the tangent-space normal atlas, a flat UV layout, not a view of the model",
	assets.ModeTextureShaded:  "texture map: the shaded colour atlas (lighting baked in), a flat UV layout, not a view of the model",
}

type previewInfo struct {
	Stage  string `yaml:"stage"`
	Mode   string `yaml:"mode"`
	View   string `yaml:"view,omitempty"`
	Source string `yaml:"source"`
	Shows  string `yaml:"shows"`
	File   string `yaml:"file"`
	Width  int    `yaml:"width"`
	Height int    `yaml:"height"`
	Cached bool   `yaml:"cached,omitempty"`
}

type previewResultFront struct {
	Preview previewInfo    `yaml:"preview"`
	Task    TaskFront      `yaml:"task"`
	Other   []previewFront `yaml:"available"`
}

// renderModes lists the render modes of stage stg of the task in d: none
// when the stage has no mesh; the draft set for a Draco draft; the GLB set,
// with the textured modes at the material stage when the card has the
// diffuse map.
func renderModes(d *taskDetail, stg string) []render3d.Mode {
	if _, ok := assets.MeshAsset(d.card, d.hist, stg); !ok {
		return nil
	}
	_, textured := assets.Find(d.list, assets.StageMaterial, assets.ModeTextureDiffuse)
	return render3d.ModesFor(stg == assets.StageGeometryDraft, stg == assets.StageMaterial && textured)
}

// previewModes lists, per stage, the get_preview modes the task in d has:
// its server images, then its local renders.
func previewModes(d *taskDetail) []previewFront {
	out := []previewFront{}
	for _, stg := range assets.Stages {
		modes := assets.Available(d.list, stg)
		for _, m := range renderModes(d, stg) {
			modes = append(modes, string(m))
		}
		if len(modes) > 0 {
			out = append(out, previewFront{Stage: stg, Modes: modes})
		}
	}
	return out
}

// previewModeCatalog lists every get_preview mode a stage can have.
func previewModeCatalog() []previewFront {
	var out []previewFront
	for _, stg := range assets.Stages {
		modes := append([]string(nil), assets.Modes[stg]...)
		for _, m := range render3d.ModesFor(stg == assets.StageGeometryDraft, stg == assets.StageMaterial) {
			modes = append(modes, string(m))
		}
		out = append(out, previewFront{Stage: stg, Modes: modes})
	}
	return out
}

// Preview is get_preview: a server image of a stage, or a local render of
// the stage's mesh in a web viewer mode from a view, as a PNG saved to a file
// and returned as image content.
func (s *Service) Preview(ctx context.Context, in PreviewInput) (Result, error) {
	uuid, err := s.resolveTask(in.TaskID)
	if err != nil {
		return Result{}, err
	}
	size := in.Size
	if size == 0 {
		size = PreviewDefault
	}
	if size < PreviewMin || size > PreviewMax {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("size %d is outside %d-%d.", size, PreviewMin, PreviewMax), "Call get_preview with size between 256 and 1024, or without size for 512.")
	}
	mode := in.Mode
	if mode == "" {
		mode = assets.ModePoster
	}
	server := slices.Contains(assets.AllModes, mode)
	if !server && !render3d.Mode(mode).Known() {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("Unknown mode %q.", mode),
			"Call get_preview with one of the modes: "+strings.Join(PreviewModes(), ", ")+".")
	}
	view := render3d.View(in.View)
	if view == "" {
		view = render3d.ViewThreeQuarter
	}
	if !slices.Contains(render3d.Views, view) {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("Unknown view %q.", in.View),
			"Call get_preview with one of the views: "+strings.Join(PreviewViews(), ", ")+".")
	}
	if in.SaveTo != "" && !filepath.IsAbs(in.SaveTo) {
		return Result{}, fail(render.CodeInvalidInput, "save_to must be an absolute path.", "Call get_preview with an absolute save_to path ending in .png, or without save_to.")
	}
	stg := in.Stage
	if stg == "" {
		stg = "current"
	}

	// The card alone lists the current stage's files; earlier stages need
	// the version history.
	d, err := s.loadDetail(ctx, uuid, false)
	if err != nil {
		return Result{}, err
	}
	if stg == "current" {
		stg = assets.PreviewStage(d.status.Stage)
	}
	if assets.NeedsHistory(d.status.Stage, stg) {
		if d, err = s.loadDetail(ctx, uuid, true); err != nil {
			return Result{}, err
		}
	}
	avail := previewModes(d)
	unavailable := func() error {
		var parts []string
		for _, p := range avail {
			parts = append(parts, p.Stage+": "+strings.Join(p.Modes, ", "))
		}
		msg := fmt.Sprintf("The task is at the %s; stage %s has no %s preview.", describeStage(d.status), stg, mode)
		hint := "Call get_preview with one of the available stage and mode pairs: " + strings.Join(parts, "; ") + "."
		if len(parts) == 0 {
			hint = "The task has no preview yet; call wait_task while it is running, then get_preview."
		}
		return failFields(render.CodeConflict, msg, hint, map[string]any{"stage": d.status.Stage, "available": avail})
	}

	info := previewInfo{Stage: stg, Mode: mode}
	var png []byte
	var lead, extra string
	if server {
		a, ok := assets.Find(d.list, stg, mode)
		if !ok {
			return Result{}, unavailable()
		}
		cache, err := s.assetCache()
		if err != nil {
			return Result{}, err
		}
		src, _, err := cache.Get(ctx, uuid, a)
		if err != nil {
			return Result{}, err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return Result{}, err
		}
		png, info.Width, info.Height, err = imaging.ToPNG(data, size)
		if err != nil {
			return Result{}, wrapf(err, "convert the %s %s image", stg, mode)
		}
		info.Source, info.Shows = "server", serverShows[mode]
		lead = fmt.Sprintf("The %s image of the %s stage of %q (%dx%d): %s.", mode, stg, clip(d.card.Prompt, 60), info.Width, info.Height, info.Shows)
		if assets.IsTextureMap(mode) {
			extra = " For a view of the textured model, use the render modes pbr, shaded or toon."
		}
	} else {
		rm := render3d.Mode(mode)
		if !slices.Contains(renderModes(d, stg), rm) {
			return Result{}, unavailable()
		}
		r, err := s.renderStage(ctx, uuid, d, stg, rm, view, size)
		if err != nil {
			return Result{}, err
		}
		png, info.Width, info.Height, info.Cached = r.PNG, r.Width, r.Height, r.Cached
		info.Source, info.Shows, info.View = "render", rm.Describe(), string(view)
		lead = fmt.Sprintf("A local render of the %s stage of %q in mode %s: %s; view %s, %dx%d.", stg, clip(d.card.Prompt, 60), mode, rm.Describe(), view, info.Width, info.Height)
		switch {
		case r.Cached:
			extra = " Served from the render cache."
		case r.Decoded:
			extra = fmt.Sprintf(" Rendered in %.1f s, after decoding the %d-triangle stage mesh in %.1f s (once per stage).", r.Took.Seconds(), r.MeshFaces, r.DecodeTook.Seconds())
		default:
			extra = fmt.Sprintf(" Rendered in %.1f s.", r.Took.Seconds())
		}
		if view == render3d.ViewGrid {
			extra += " The grid shows front (top left), right (top right), back (bottom left) and three_quarter (bottom right)."
		}
	}

	out := in.SaveTo
	if out == "" {
		name := fmt.Sprintf("%s-%s-%s-%d.png", short(uuid), stg, mode, size)
		if info.Source == "render" {
			name = fmt.Sprintf("%s-%s-%s-%s-%d.png", short(uuid), stg, mode, view, size)
		}
		out = filepath.Join(s.paths.Previews, name)
	}
	if err := domain.WriteFileAtomic(out, png, 0o644); err != nil {
		return Result{}, wrapf(err, "write %s", out)
	}
	info.File = out
	front := previewResultFront{Preview: info, Task: s.taskFront(d.card), Other: avail}
	body := lead + " Saved to " + out + "." + extra
	body += "\n\nNext: get_preview with another stage, mode or view from available (server images: poster, geometry_image, material_image, texture_*; the other modes are local renders of the stage mesh); get_task for the settings."
	return Result{Front: front, Body: body, Images: []Image{{PNG: png, What: stg + " " + mode}}}, nil
}
