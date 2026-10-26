package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
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
	Mode   string // poster | geometry | material | diffuse | pbr | normal | shaded
	Size   int
	SaveTo string
}

type previewInfo struct {
	Stage  string `yaml:"stage"`
	Mode   string `yaml:"mode"`
	Source string `yaml:"source"`
	File   string `yaml:"file"`
	Width  int    `yaml:"width"`
	Height int    `yaml:"height"`
}

type previewResultFront struct {
	Preview previewInfo    `yaml:"preview"`
	Task    TaskFront      `yaml:"task"`
	Other   []previewFront `yaml:"available"`
}

// Preview is get_preview: one server image of a stage as a PNG, saved to a
// file and returned as image content.
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
		mode = "poster"
	}
	stg := in.Stage
	if stg == "" {
		stg = "current"
	}

	// The card alone lists the current stage's images; earlier stages need
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
	var avail []previewFront
	for _, st := range assets.Stages {
		if modes := assets.Available(d.list, st); len(modes) > 0 {
			avail = append(avail, previewFront{Stage: st, Modes: modes})
		}
	}
	a, ok := assets.Find(d.list, stg, mode)
	if !ok {
		var parts []string
		for _, p := range avail {
			parts = append(parts, p.Stage+": "+strings.Join(p.Modes, ", "))
		}
		msg := fmt.Sprintf("The task is at the %s; it has no %s image for stage %s.", describeStage(d.status), mode, stg)
		hint := "Call get_preview with one of the available stage and mode pairs: " + strings.Join(parts, "; ") + "."
		if len(parts) == 0 {
			hint = "The task has no server image yet; call wait_task while it is running, then get_preview."
		}
		return Result{}, failFields(render.CodeConflict, msg, hint, map[string]any{"stage": d.status.Stage, "available": avail})
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
	png, w, h, err := imaging.ToPNG(data, size)
	if err != nil {
		return Result{}, wrapf(err, "convert the %s %s image", stg, mode)
	}
	out := in.SaveTo
	if out == "" {
		out = filepath.Join(s.paths.Previews, fmt.Sprintf("%s-%s-%s-%d.png", short(uuid), stg, mode, size))
	} else if !filepath.IsAbs(out) {
		return Result{}, fail(render.CodeInvalidInput, "save_to must be an absolute path.", "Call get_preview with an absolute save_to path ending in .png, or without save_to.")
	}
	if err := domain.WriteFileAtomic(out, png, 0o644); err != nil {
		return Result{}, wrapf(err, "write %s", out)
	}
	front := previewResultFront{
		Preview: previewInfo{Stage: stg, Mode: mode, Source: "server", File: out, Width: w, Height: h},
		Task:    s.taskFront(d.card),
		Other:   avail,
	}
	body := fmt.Sprintf("The %s %s image of %q (%dx%d), saved to %s.", stg, mode, clip(d.card.Prompt, 60), w, h, out)
	body += "\n\nNext: get_preview with another stage or mode from available; get_task for the settings."
	return Result{Front: front, Body: body, Images: []Image{{PNG: png, What: stg + " " + mode}}}, nil
}
