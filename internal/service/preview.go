package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/turntable"
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

// ViewerModes lists every mode a viewer pane can show: the render modes,
// and the turntable looks of a Gen-1.5 draft (a pane of its server frames).
func ViewerModes() []string {
	return append(RenderModes(), assets.TurntableModes...)
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
// when the stage has no mesh (a Gen-1.5 draft's turntable looks are server
// images); the draft set for a Draco draft; the GLB set,
// with the textured modes at the material stage when the card has the
// diffuse map.
func renderModes(d *taskDetail, stg string) []render3d.Mode {
	a, ok := assets.MeshAsset(d.card, d.hist, stg)
	if !ok {
		return nil // also a Gen-1.5 draft: its turntable looks are server images
	}
	_, textured := assets.Find(d.list, assets.StageMaterial, assets.ModeTextureDiffuse)
	return render3d.ModesFor(stg == assets.StageGeometryDraft && !assets.IsModelFile(a), stg == assets.StageMaterial && textured)
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

// turntablePNG draws look of a turntable sheet from view as a PNG of size:
// the frame nearest the view's direction round the model, or for the grid
// the frames of its four views.
func turntablePNG(data []byte, look string, view render3d.View, size int) ([]byte, int, int, error) {
	sheet, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, wrapf(err, "read the turntable sheet")
	}
	if err := turntable.Check(sheet); err != nil {
		return nil, 0, 0, fail(render.CodeConflict, "The geometry draft picture of this task cannot be read as the server's turntable: "+err.Error()+".",
			"Call get_preview with mode poster.")
	}
	frame := func(v render3d.View) (int, error) {
		theta, phi, ok := v.Angles()
		if !ok || phi == 0 {
			return 0, fail(render.CodeInvalidInput, fmt.Sprintf("View %s is not available for a turntable look: %s.", v, turntable.ErrNoView),
				"Call get_preview with view front, right, back, left, three_quarter, web or grid.")
		}
		return turntable.FrameAt(theta), nil
	}
	var img *image.NRGBA
	if view == render3d.ViewGrid {
		img = image.NewNRGBA(image.Rect(0, 0, size, size))
		cell := size / 2
		for i, v := range render3d.GridViews {
			k, err := frame(v)
			if err != nil {
				return nil, 0, 0, err
			}
			c, err := turntable.View(sheet, look, k, cell, cell, 1)
			if err != nil {
				return nil, 0, 0, err
			}
			at := image.Pt(i%2*cell, i/2*cell)
			draw.Draw(img, c.Bounds().Add(at), c, image.Point{}, draw.Src)
		}
	} else {
		k, err := frame(view)
		if err != nil {
			return nil, 0, 0, err
		}
		if img, err = turntable.View(sheet, look, k, size, size, 1); err != nil {
			return nil, 0, 0, err
		}
	}
	png, err := encodeRender(img)
	if err != nil {
		return nil, 0, 0, err
	}
	return png, size, size, nil
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
		if look, ok := assets.TurntableLook(mode); ok {
			png, info.Width, info.Height, err = turntablePNG(data, look, view, size)
			if err != nil {
				return Result{}, err
			}
			info.View, info.Shows = string(view), turntable.Describe(look)
			extra = " The Gen-1.5 draft has no mesh: the server's turntable sheet holds 16 frames round the model in four looks (turntable_*), " +
				"and the view picks the nearest frame (no top view)."
		} else {
			png, info.Width, info.Height, err = imaging.ToPNG(data, size)
			if err != nil {
				return Result{}, wrapf(err, "convert the %s %s image", stg, mode)
			}
			info.Shows = serverShows[mode]
		}
		info.Source = "server"
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
		if info.View != "" { // a render or a turntable frame: the view is part of the picture
			name = fmt.Sprintf("%s-%s-%s-%s-%d.png", short(uuid), stg, mode, view, size)
		}
		out = filepath.Join(s.paths.Previews, name)
	}
	if err := domain.WriteFileAtomic(out, png, 0o644); err != nil {
		return Result{}, wrapf(err, "write %s", out)
	}
	info.File = out
	front := previewResultFront{Preview: info, Task: s.liveTaskFront(ctx, d.card), Other: avail}
	body := lead + " Saved to " + out + "." + extra
	body += "\n\nNext: get_preview with another stage, mode or view from available (server images: poster, turntable_*, geometry_image, material_image, texture_*; the other modes are local renders of the stage mesh); get_task for the settings."
	return Result{Front: front, Body: body, Images: []Image{{PNG: png, What: stg + " " + mode}}}, nil
}
