package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

// 3D Editing (modelTo3d, notes 14 section 7; web builders main.fmt.js:87539
// workbench and chunk 3713:24795 home panel): the same task/rodin_mesh
// submit with the model file in "models" and reference images, which the
// web takes from its viewer (the user turns the model to its front) or from
// the user. The task then runs the ordinary stages: a geometry draft of the
// model, confirm_geometry, generate_texture, export_model.

// Model file rules of the web's 3D Editing upload.
var editFormats = []string{".glb", ".gltf", ".fbx", ".obj", ".dae", ".stl", ".ply"}

const maxModelFile = 150 << 20 // the web's 150 (MB) cap

// identityRotation is model_reference_rotation for a model seen from its own
// front (+Z), unturned: the web's 3x3 matrix, row-major.
var identityRotation = []float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

// TextureModelInput is the input of texture_model.
type TextureModelInput struct {
	ModelPath string
	Images    []string // reference images; empty: a render of the model's front
	Prompt    string
	Model     string // gen-2.5 | gen-1.5
	Mode      string // gen-1.5 mode
	Public    bool
	Expected  *float64
	Wait      bool
}

type textureModelDetail struct {
	ModelFile string        `yaml:"model_file"`
	Images    []sentInput   `yaml:"images"`
	Rendered  bool          `yaml:"front_rendered,omitempty"`
	Prompt    string        `yaml:"prompt"`
	Model     string        `yaml:"model"`
	Mode      string        `yaml:"mode,omitempty"`
	Privacy   *privacyFront `yaml:"privacy"`
}

// TextureModel is texture_model.
func (s *Service) TextureModel(ctx context.Context, in TextureModelInput) (Result, error) {
	if _, err := s.permit("texture_model"); err != nil {
		return Result{}, err
	}
	if _, err := s.signedIn(ctx); err != nil {
		return Result{}, err
	}
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	model := strings.ToLower(strings.TrimSpace(in.Model))
	if model == "" {
		model = "gen-2.5"
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch model {
	case "gen-2.5":
		if mode != "" {
			return Result{}, bad("mode is a gen-1.5 setting.", "Leave mode out for gen-2.5, or pass model gen-1.5.")
		}
	case "gen-1.5":
		if mode == "" {
			mode = "default"
		}
		if !oneOf(mode, "default", "focal", "zero", "speedy") {
			return Result{}, bad(fmt.Sprintf("mode %q is not one of default, focal, zero, speedy.", mode), "Pass one of the listed modes.")
		}
	default:
		return Result{}, bad(fmt.Sprintf("model %q is not gen-2.5 or gen-1.5.", model), "Pass gen-2.5 (default) or gen-1.5.")
	}
	mp := strings.TrimSpace(in.ModelPath)
	ext := strings.ToLower(filepath.Ext(mp))
	if !filepath.IsAbs(mp) || !oneOf(ext, editFormats...) {
		return Result{}, bad(fmt.Sprintf("model_path %q is not an absolute path of a %s file.", in.ModelPath, strings.Join(editFormats, ", ")), "Pass the absolute path of the model file.")
	}
	data, err := os.ReadFile(mp)
	if err != nil {
		return Result{}, bad(fmt.Sprintf("model_path cannot be read: %v.", err), "Pass the path of an existing model file.")
	}
	if len(data) == 0 || len(data) > maxModelFile {
		return Result{}, bad(fmt.Sprintf("The model file is %s; the web takes up to 150 MB.", sizeText(int64(len(data)))), "Pass a smaller model file.")
	}
	if len([]rune(in.Prompt)) > maxPrompt {
		return Result{}, bad(fmt.Sprintf("prompt is %d characters; at most %d.", len([]rune(in.Prompt)), maxPrompt), "Shorten the prompt.")
	}

	detail := textureModelDetail{ModelFile: mp, Model: model, Mode: mode}
	paths := in.Images
	if len(paths) == 0 {
		// The web's reference is a picture of the model from its viewer,
		// turned to its front; with no image given, a render of the
		// model's front stands in for it.
		if !oneOf(ext, modelfile.Extensions...) {
			return Result{}, bad("Without images, the model's front is rendered as the reference, which needs a glb, gltf, obj or stl file.", "Pass images: 1-5 pictures of the object.")
		}
		png, err := s.renderModelFile(ctx, mp, render3d.ModeClassic, render3d.ViewFront, uploadEdge)
		if err != nil {
			return Result{}, wrapf(err, "render the model's front")
		}
		front := filepath.Join(s.paths.Previews, "texture-model-front-"+sum(data)[:12]+".png")
		if err := os.MkdirAll(filepath.Dir(front), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(front, png, 0o600); err != nil {
			return Result{}, err
		}
		paths, detail.Rendered = []string{front}, true
	}
	list := make([]GenerateImage, len(paths))
	for i, p := range paths {
		list[i] = GenerateImage{Path: p}
	}
	images, err := prepareImages(list)
	if err != nil {
		return Result{}, err
	}

	sp, err := s.prepare(ctx, "texture_model", "", price.Input{Model: model}, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	if len(images) > 1 && !sp.info.HasPrivilege("RodinUseMultiImage") {
		return Result{}, &price.Gate{Reason: "2 or more images in one submit need the RodinUseMultiImage privilege (a paid plan)"}
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		pre, err := s.client.ImageProcess(ctx, images[0].upload.Name, images[0].upload.MIME, images[0].upload.Data, true)
		if err != nil {
			return Result{}, wrapf(err, "preprocess image 1 (%s)", images[0].file)
		}
		prompt = strings.TrimSpace(pre.Prompt)
	}
	if prompt == "" {
		prompt = defaultPrompt
	}
	detail.Prompt = prompt
	settings := editSettings(model, mode, len(images), prompt)
	req := rodin.MeshRequest{Prompt: prompt, Settings: settings, AlphaAll: true,
		Models: []rodin.UploadImage{{Name: uploadBase(mp), MIME: modelMIME(ext), Data: data}}}
	weights, _ := settings["view_weights"].([]float64)
	for i, img := range images {
		req.Images = append(req.Images, img.upload)
		w := 0.0
		if i < len(weights) {
			w = weights[i]
		}
		detail.Images = append(detail.Images, sentInput{File: img.file, View: "?", Weight: w, Sent: img.sent})
	}

	sp.open()
	reply, err := s.client.Mesh(ctx, req)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("texture_model", "", err)
	}
	sp.uuid = reply.UUID
	privacy, privacyNote := s.privacyOfNew(ctx, reply.UUID, in.Public)
	detail.Privacy = privacy
	set, err := s.settle(ctx, reply.UUID, stage.Status{}, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, wrapf(err, "the 3D Editing task %s was submitted; read its card", reply.UUID)
	}
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Submitted %s with %d reference image(s) as 3D Editing task %s (%s).",
		filepath.Base(mp), len(images), reply.UUID, strings.TrimSpace(model+" "+mode)), detail)
	res.Body += "\n\nThe task runs the ordinary stages: confirm_geometry takes the draft of the model, then generate_texture textures it and export_model packs it."
	if privacyNote != "" {
		res.Body += "\n\n" + privacyNote
	}
	return res, nil
}

// editSettings is the settings JSON of a 3D Editing submit (web pe,
// main.fmt.js:87590-87605): reference images are all "?", the default
// weights of the count, height 100, no face enhancement, the model unturned,
// and the generation mode; Gen-2.5 sends no brilliant_mode.
func editSettings(model, mode string, n int, prompt string) map[string]any {
	labels := make([]string, n)
	for i := range labels {
		labels[i] = "?"
	}
	var weights []float64
	for _, row := range catalog.DefaultWeights {
		if row.Images == n {
			weights = append([]float64(nil), row.Gen25...)
		}
	}
	condition := "concat"
	st := map[string]any{"allow_face_enhance": false, "default_face_enhance": false, "height": 100, "image_label": labels,
		"model_reference_rotation": identityRotation, "view_weights": weights}
	switch model {
	case "gen-1.5":
		st["generation_mode"] = map[string]string{"default": "Default", "focal": "Detail", "zero": "Smooth", "speedy": "Fast"}[mode]
		if n > 1 {
			condition = "fuse"
		}
		switch mode {
		case "speedy":
			st["fast_texture"] = true
		case "focal":
			st["sharp_bias"] = 2.5
		}
	default:
		st["generation_mode"] = "Brilliant"
	}
	st["condition_mode"] = condition
	if hasCharacterTag(prompt) {
		st["image_mode"] = "recon"
	}
	return st
}

// hasCharacterTag is the web's rt: the prompt after its first "." names a
// character.
func hasCharacterTag(prompt string) bool {
	i := strings.Index(prompt, ".")
	return strings.Contains(strings.ToLower(strings.TrimSpace(prompt[i+1:])), "character")
}

// modelMIME is the content type a browser gives a model file.
func modelMIME(ext string) string {
	switch ext {
	case ".glb":
		return "model/gltf-binary"
	case ".gltf":
		return "model/gltf+json"
	case ".stl":
		return "model/stl"
	case ".obj":
		return "model/obj"
	}
	return "application/octet-stream"
}
