package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

// Input image rules of the web app (notes 14 section 1).
const (
	MaxImages     = 5
	maxImageBytes = 20 << 20 // per file and for all files together, as uploaded
	uploadEdge    = 1024     // the browser scales the longest side to at most this
	maxPrompt     = 1024
	defaultPrompt = "A high-quality 3D model. sharp edges, complex geometry, game-ready"
)

// GenerateImage is one input image of generate_model.
type GenerateImage struct {
	Path   string
	View   string   // F FL FR B BL BR L R U D ?; empty is ?
	Weight *float64 // nil: the default table
}

// Presets are the web's preset panel (notes 14 section 2.2).
type Presets struct {
	Micro             string // micro | clean (extreme-high)
	Instruct          string // creative | faithful (medium and above)
	Symmetric         string // symmetric | asymmetric
	SymmetricStrength int    // 1-3 (symmetric only)
	Detailed          int    // 1-3
	Soft              bool
}

// Advanced are the web's advanced options (notes 14 section 2.3).
type Advanced struct {
	Seed           *int
	NegativePrompt string
	Cfg            *int
	Steps          *int
	Detail         *int
}

// GenerateInput is the input of generate_model (plan 2.6.3).
type GenerateInput struct {
	Images           []GenerateImage
	InputSet         string // an input set arranged in the viewer, instead of Images
	Prompt           string
	Model            string // gen-2.5 | gen-1.5
	Tier             string
	Mode             string // gen-1.5: default focal zero speedy
	ImageMode        string // recon | rest
	FaceEnhance      *bool
	Presets          *Presets
	Advanced         *Advanced
	BBox             []float64 // width, height, length
	Batch            int
	CopySettingsFrom string
	Public           bool // keep the new task public; default private
	Expected         *float64
	Wait             bool
}

type sentInput struct {
	File   string  `yaml:"file"`
	View   string  `yaml:"view"`
	Weight float64 `yaml:"weight"`
	Sent   string  `yaml:"sent"` // as is | scaled to 1024 px PNG
}

// copiedRecipe is what the copied task did after its draft, for the agent
// to pass to the later stage tools.
type copiedRecipe struct {
	From string `yaml:"from"`
	// PresetsNotCopied are presets of the copied task that the new task's
	// model or tier does not offer.
	PresetsNotCopied []string       `yaml:"presets_not_copied,omitempty,flow"`
	Confirm          map[string]any `yaml:"confirm_geometry,omitempty"`
	Texture          map[string]any `yaml:"generate_texture,omitempty"`
	Export           map[string]any `yaml:"export_model,omitempty"`
}

type generateDetail struct {
	Prompt    string        `yaml:"prompt"`
	Inputs    []sentInput   `yaml:"inputs"`
	Model     string        `yaml:"model"`
	Tier      string        `yaml:"tier,omitempty"`
	Mode      string        `yaml:"mode,omitempty"`
	Multiview string        `yaml:"multiview_warning,omitempty"`
	Privacy   *privacyFront `yaml:"privacy"`
	Recipe    *copiedRecipe `yaml:"recipe,omitempty"`
}

type preparedImage struct {
	upload rodin.UploadImage
	view   string
	weight *float64
	sent   string
	file   string
}

// Generate is generate_model: preprocess each image (free), submit the
// draft, wait for it.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (Result, error) {
	if _, err := s.permit("generate_model"); err != nil {
		return Result{}, err
	}
	if _, err := s.signedIn(ctx); err != nil {
		return Result{}, err
	}
	var recipe *copiedRecipe
	if id := strings.TrimSpace(in.CopySettingsFrom); id != "" {
		src, err := s.resolveTask(id)
		if err != nil {
			return Result{}, err
		}
		card, err := s.client.Card(ctx, src)
		if err != nil {
			return Result{}, wrapf(err, "read copy_settings_from %s", short(src))
		}
		recipe = copySettings(&in, card)
	}
	model := strings.ToLower(strings.TrimSpace(in.Model))
	if model == "" {
		model = "gen-2.5"
	}
	tier := strings.ToLower(strings.TrimSpace(in.Tier))
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if err := checkModel(model, tier, &mode); err != nil {
		return Result{}, err
	}
	if id := strings.TrimSpace(in.InputSet); id != "" {
		if len(in.Images) > 0 {
			return Result{}, fail(render.CodeInvalidInput, "Give images or input_set, not both.", "Pass the input set id alone to use the images, order, tags and weights arranged in the viewer.")
		}
		set, err := s.LoadInputSet(id)
		if err != nil {
			return Result{}, err
		}
		for _, im := range set.Images {
			in.Images = append(in.Images, GenerateImage{Path: im.Path, View: im.View, Weight: im.Weight})
		}
	}
	images, err := prepareImages(in.Images)
	if err != nil {
		return Result{}, err
	}
	weights, err := viewWeights(images, model)
	if err != nil {
		return Result{}, err
	}
	if len([]rune(in.Prompt)) > maxPrompt {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("prompt is %d characters; at most %d.", len([]rune(in.Prompt)), maxPrompt), "Shorten the prompt.")
	}
	if err := checkPresets(in.Presets, model, tier); err != nil {
		return Result{}, err
	}
	if err := checkAdvanced(in.Advanced, in.BBox, in.ImageMode); err != nil {
		return Result{}, err
	}

	sp, err := s.prepare(ctx, "generate_model", "", price.Input{Model: model, Tier: tier, Batch: in.Batch}, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	if len(images) > 1 && !sp.info.HasPrivilege("RodinUseMultiImage") {
		return Result{}, &price.Gate{Reason: "2 or more images in one submit need the RodinUseMultiImage privilege (a paid plan)"}
	}

	// Preprocess every image (free): the caption, height and face flags of
	// the first one go into the submit, like the web.
	var first *rodin.Preprocess
	multiview := ""
	for i, img := range images {
		pre, err := s.client.ImageProcess(ctx, img.upload.Name, img.upload.MIME, img.upload.Data, i == 0)
		if err != nil {
			return Result{}, wrapf(err, "preprocess image %d (%s)", i+1, img.file)
		}
		if i == 0 {
			first = pre
		}
		if pre.IsMultiview && multiview == "" {
			multiview = fmt.Sprintf("Image %d looks like a sheet of %s views in one picture; the web offers to crop it into separate images. It was sent as one image.", i+1, orUnknown(pre.NumMultiview.String()))
		}
	}

	prompt := composePrompt(in, first, model, mode)
	settings, err := generateSettings(in, first, images, weights, model, tier, mode)
	if err != nil {
		return Result{}, err
	}
	req := rodin.MeshRequest{Prompt: prompt, Settings: settings, BatchGen: in.Batch}
	detail := generateDetail{Prompt: prompt, Model: model, Tier: tier, Mode: mode, Multiview: multiview, Recipe: recipe}
	for i, img := range images {
		req.Images = append(req.Images, img.upload)
		req.UseOriginalAlpha = append(req.UseOriginalAlpha, false)
		detail.Inputs = append(detail.Inputs, sentInput{File: img.file, View: img.view, Weight: weights[i], Sent: img.sent})
	}

	sp.open()
	reply, err := s.client.Mesh(ctx, req)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("generate_model", "", err)
	}
	sp.uuid = reply.UUID
	// Private first, like the web right after its submit, so the draft is
	// never public longer than the two calls take.
	privacy, privacyNote := s.privacyOfNew(ctx, reply.UUID, in.Public)
	detail.Privacy = privacy
	set, err := s.settle(ctx, reply.UUID, stage.Status{}, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, wrapf(err, "the draft %s was submitted; read its card", reply.UUID)
	}
	lead := fmt.Sprintf("Submitted %d image(s) as task %s (%s).", len(images), reply.UUID, strings.TrimSpace(model+" "+tier+" "+mode))
	res := s.finishFlow(ctx, sp, set, lead, detail)
	if privacyNote != "" {
		res.Body += "\n\n" + privacyNote
	}
	if multiview != "" {
		res.Body += "\n\n" + multiview
	}
	if recipe != nil {
		res.Body += "\n\nrecipe lists what the copied task used for its later stages: pass those values to confirm_geometry, generate_texture and export_model."
		if len(recipe.PresetsNotCopied) > 0 {
			res.Body += " Presets not copied (the new model or tier does not offer them): " + strings.Join(recipe.PresetsNotCopied, ", ") + "."
		}
	}
	return res, nil
}

// checkModel validates model, tier and the Gen-1.5 mode.
func checkModel(model, tier string, mode *string) error {
	switch model {
	case "gen-2.5":
		if tier == "" {
			return fail(render.CodeInvalidInput, "tier is required for gen-2.5.", "Pass tier: extreme-low, low, medium, high or extreme-high (quote_cost shows the price of each).")
		}
		if !oneOf(tier, "extreme-low", "low", "medium", "high", "extreme-high") {
			return fail(render.CodeInvalidInput, fmt.Sprintf("tier %q is not one of extreme-low, low, medium, high, extreme-high.", tier), "Pass one of the listed tiers.")
		}
		if *mode != "" {
			return fail(render.CodeInvalidInput, "mode is a gen-1.5 setting.", "Leave mode out for gen-2.5, or pass model gen-1.5.")
		}
	case "gen-1.5":
		if tier != "" {
			return fail(render.CodeInvalidInput, "tier is a gen-2.5 setting.", "Leave tier out for gen-1.5; pass mode (default, focal, zero, speedy).")
		}
		if *mode == "" {
			*mode = "default"
		}
		if !oneOf(*mode, "default", "focal", "zero", "speedy") {
			return fail(render.CodeInvalidInput, fmt.Sprintf("mode %q is not one of default, focal, zero, speedy.", *mode), "Pass one of the listed modes.")
		}
	default:
		return fail(render.CodeInvalidInput, fmt.Sprintf("model %q is not gen-2.5 or gen-1.5.", model), "Pass model gen-2.5 (default) or gen-1.5.")
	}
	return nil
}

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// prepareImages reads, checks and (when larger than the web's upload size)
// scales the input images.
func prepareImages(list []GenerateImage) ([]preparedImage, error) {
	if len(list) == 0 || len(list) > MaxImages {
		return nil, fail(render.CodeInvalidInput, fmt.Sprintf("generate_model takes 1 to %d images; got %d.", MaxImages, len(list)), "Pass images as a list of {path, view, weight}, or input_set: the id of an input set arranged in the viewer.")
	}
	tags := map[string]bool{}
	for _, t := range catalog.ViewTags {
		tags[t.Tag] = true
	}
	var out []preparedImage
	total := 0
	for i, img := range list {
		p := strings.TrimSpace(img.Path)
		if !filepath.IsAbs(p) {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d].path %q is not an absolute path.", i, img.Path), "Pass absolute paths of jpg, png or webp files.")
		}
		view := strings.ToUpper(strings.TrimSpace(img.View))
		if view == "" {
			view = "?"
		}
		if !tags[view] {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d].view %q is not a view tag.", i, img.View), "Use one of F FL FR B BL BR L R U D ? (get_generation_options lists them).")
		}
		if img.Weight != nil && (*img.Weight <= 0 || *img.Weight > 1 || math.IsNaN(*img.Weight)) {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d].weight %g is outside (0, 1].", i, *img.Weight), "Pass weights above 0 and at most 1; they are normalised to sum 1.")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d] cannot be read: %v.", i, err), "Pass the path of an existing image file.")
		}
		mime := imaging.Sniff(data)
		if mime == "" {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d] (%s) is not a jpg, png or webp image.", i, filepath.Base(p)), "Convert it to png or jpg first.")
		}
		w, h, err := imaging.Size(data)
		if err != nil {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d] (%s) cannot be decoded: %v.", i, filepath.Base(p), err), "Pass a valid image file.")
		}
		name := uploadName(p, mime)
		sent := "as is"
		if max(w, h) > uploadEdge {
			img2, err := imaging.Decode(bytes.NewReader(data))
			if err != nil {
				return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d] (%s) cannot be decoded: %v.", i, filepath.Base(p), err), "Pass a valid image file.")
			}
			if data, err = imaging.PNG(imaging.Fit(img2, uploadEdge)); err != nil {
				return nil, wrapf(err, "scale images[%d]", i)
			}
			mime, name, sent = "image/png", strings.TrimSuffix(name, filepath.Ext(name))+".png", fmt.Sprintf("scaled from %dx%d to a %d px PNG", w, h, uploadEdge)
		}
		if len(data) > maxImageBytes {
			return nil, fail(render.CodeInvalidInput, fmt.Sprintf("images[%d] (%s) is %s; the limit is 20 MB.", i, filepath.Base(p), sizeText(int64(len(data)))), "Pass a smaller image.")
		}
		total += len(data)
		out = append(out, preparedImage{upload: rodin.UploadImage{Name: name, MIME: mime, Data: data}, view: view, weight: img.Weight, sent: sent, file: p})
	}
	if total > maxImageBytes {
		return nil, fail(render.CodeInvalidInput, fmt.Sprintf("The images are %s together; the limit is 20 MB.", sizeText(int64(total))), "Pass fewer or smaller images.")
	}
	return out, nil
}

// uploadName is the file name sent for an image: its own base name with
// the extension of its real type.
func uploadName(p, mime string) string {
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	base = strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, base)
	if base == "" {
		base = "image"
	}
	ext := map[string]string{"image/webp": ".webp", "image/jpeg": ".jpg", "image/png": ".png"}[mime]
	return base + ext
}

// viewWeights returns the weights sent: the default table of the model when
// none is given, else the given ones normalised to sum 1 the web's way
// (rounded to 2 decimals, the last takes the remainder).
func viewWeights(images []preparedImage, model string) ([]float64, error) {
	n := len(images)
	given := 0
	for _, img := range images {
		if img.weight != nil {
			given++
		}
	}
	if given == 0 {
		for _, row := range catalog.DefaultWeights {
			if row.Images == n {
				if model == "gen-1.5" {
					return append([]float64(nil), row.Gen15...), nil
				}
				return append([]float64(nil), row.Gen25...), nil
			}
		}
		return nil, fmt.Errorf("no default weights for %d images", n)
	}
	if given != n {
		return nil, fail(render.CodeInvalidInput, fmt.Sprintf("%d of %d images have a weight.", given, n), "Give every image a weight, or none for the default weights.")
	}
	sum := 0.0
	for _, img := range images {
		sum += *img.weight
	}
	out := make([]float64, n)
	acc := 0.0
	for i, img := range images {
		if i == n-1 {
			out[i] = round2(1 - acc)
			break
		}
		out[i] = round2(*img.weight / sum)
		acc += out[i]
	}
	return out, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// checkPresets validates the presets against model and tier (the web shows
// them only where they apply).
func checkPresets(p *Presets, model, tier string) error {
	if p == nil {
		return nil
	}
	tierIndex := map[string]int{"extreme-low": 0, "low": 1, "medium": 2, "high": 3, "extreme-high": 4}[tier]
	bad := func(msg string) error {
		return fail(render.CodeInvalidInput, msg, "get_generation_options lists each preset with the tiers it applies to.")
	}
	if p.Micro != "" {
		if model != "gen-2.5" || tier != "extreme-high" {
			return bad("presets.micro applies to gen-2.5 extreme-high only.")
		}
		if !oneOf(p.Micro, "micro", "clean") {
			return bad(fmt.Sprintf("presets.micro %q is not micro or clean.", p.Micro))
		}
	}
	if p.Instruct != "" {
		if model != "gen-2.5" || tierIndex < 2 {
			return bad("presets.instruct applies to gen-2.5 medium and above.")
		}
		if !oneOf(p.Instruct, "creative", "faithful") {
			return bad(fmt.Sprintf("presets.instruct %q is not creative or faithful.", p.Instruct))
		}
	}
	if p.Symmetric != "" && !oneOf(p.Symmetric, "symmetric", "asymmetric") {
		return bad(fmt.Sprintf("presets.symmetric %q is not symmetric or asymmetric.", p.Symmetric))
	}
	if p.SymmetricStrength != 0 && (p.Symmetric != "symmetric" || p.SymmetricStrength < 1 || p.SymmetricStrength > 3) {
		return bad("presets.symmetric_strength is 1-3 and needs symmetric \"symmetric\".")
	}
	if p.Detailed != 0 && (p.Detailed < 1 || p.Detailed > 3) {
		return bad("presets.detailed is a level from 1 to 3.")
	}
	return nil
}

func checkAdvanced(a *Advanced, bbox []float64, imageMode string) error {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	if imageMode != "" && !oneOf(imageMode, "recon", "rest") {
		return bad(fmt.Sprintf("image_mode %q is not recon or rest.", imageMode), "Pass recon (default) or rest.")
	}
	if len(bbox) != 0 {
		if len(bbox) != 3 {
			return bad("bbox has 3 numbers: width, height, length.", "Pass [width, height, length], each 1-300.")
		}
		for _, v := range bbox {
			if v < 1 || v > 300 {
				return bad(fmt.Sprintf("bbox value %g is outside 1-300.", v), "Pass [width, height, length], each 1-300.")
			}
		}
	}
	if a == nil {
		return nil
	}
	switch {
	case a.Seed != nil && (*a.Seed < 0 || *a.Seed > 65535):
		return bad("advanced.seed is 0-65535.", "Pass a seed from 0 to 65535, or leave it out for a random one.")
	case a.Cfg != nil && (*a.Cfg < 1 || *a.Cfg > 15):
		return bad("advanced.cfg is 1-15.", "Pass cfg from 1 to 15 (web default 10).")
	case a.Steps != nil && (*a.Steps < 10 || *a.Steps > 100):
		return bad("advanced.steps is 10-100.", "Pass steps from 10 to 100 (web default 50).")
	case a.Detail != nil && (*a.Detail < -5 || *a.Detail > 5):
		return bad("advanced.detail is -5 to 5.", "Pass detail from -5 to 5.")
	}
	return nil
}

// composePrompt is the prompt sent: the caller's, else the caption the
// preprocess step wrote, else the web's default; the preset tags follow
// like the web composes them (notes 14 section 2.2). Gen-1.5 speedy sends
// an empty prompt.
func composePrompt(in GenerateInput, pre *rodin.Preprocess, model, mode string) string {
	if model == "gen-1.5" && mode == "speedy" {
		return ""
	}
	caption := strings.TrimSpace(in.Prompt)
	if caption == "" && pre != nil {
		caption = strings.TrimSpace(pre.Prompt)
	}
	if caption == "" {
		caption = defaultPrompt
	}
	var tags []string
	if p := in.Presets; p != nil {
		switch p.Symmetric {
		case "symmetric":
			tags = append(tags, "symmetric geometry")
		case "asymmetric":
			tags = append(tags, "asymmetric geometry")
		}
		if p.Soft {
			tags = append(tags, "smooth edges")
		}
	}
	var fresh []string
	for _, t := range tags {
		if !strings.Contains(strings.ToLower(caption), t) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) == 0 {
		return caption
	}
	sep := ". "
	if strings.HasSuffix(caption, ".") {
		sep = " "
	}
	return caption + sep + strings.Join(fresh, ", ")
}

// generateSettings builds the settings JSON of rodin_mesh (notes 14
// section 2.1; the 9-field core verified live on 2026-10-07).
func generateSettings(in GenerateInput, pre *rodin.Preprocess, images []preparedImage, weights []float64, model, tier, mode string) (map[string]any, error) {
	labels := make([]string, len(images))
	for i, img := range images {
		labels[i] = img.view
	}
	height := 100.0
	if pre != nil {
		if h, err := strconv.ParseFloat(pre.Height.String(), 64); err == nil && h > 0 {
			height = h
		}
	}
	allow, face := false, false
	if pre != nil {
		allow, face = pre.AllowFaceEnhance, pre.DefaultFaceEnhance
	}
	// Speedy low tiers and Gen-1.5 speedy never enhance faces (web _e).
	noFace := (model == "gen-2.5" && (tier == "extreme-low" || tier == "low")) || (model == "gen-1.5" && mode == "speedy")
	if noFace {
		allow, face = false, false
	}
	if in.FaceEnhance != nil {
		if *in.FaceEnhance && !allow {
			why := "the preprocess step does not offer it for this image"
			if noFace {
				why = "it is off for this tier or mode"
			}
			return nil, fail(render.CodeInvalidInput, "face_enhance is not available: "+why+".", "Leave face_enhance out.")
		}
		face = *in.FaceEnhance
	}
	imageMode := in.ImageMode
	if imageMode == "" {
		imageMode = "recon"
	}
	condition := "concat"
	if model == "gen-1.5" && len(images) > 1 {
		condition = "fuse"
	}
	st := map[string]any{
		"view_weights": weights, "condition_mode": condition, "image_label": labels, "height": height,
		"allow_face_enhance": allow, "default_face_enhance": face, "image_mode": imageMode,
	}
	switch model {
	case "gen-2.5":
		st["generation_mode"] = "Brilliant"
		st["brilliant_mode"] = "gen-2.5-" + tier
	case "gen-1.5":
		st["generation_mode"] = map[string]string{"default": "Default", "focal": "Detail", "zero": "Smooth", "speedy": "Fast"}[mode]
		switch mode {
		case "focal":
			st["sharp_bias"] = 2.5
		case "speedy":
			st["fast_texture"] = true
		}
	}
	if p := in.Presets; p != nil && model == "gen-2.5" {
		if p.Symmetric == "symmetric" {
			st["symmetric_bias"] = []int{0, 2, 3}[max(p.SymmetricStrength, 1)-1]
		} else if p.Symmetric == "asymmetric" {
			st["symmetric_bias"] = 0
		}
		if p.Detailed > 0 {
			st["detail_level"] = p.Detailed
		}
		if p.Micro != "" {
			st["micro_bias"] = map[string]int{"micro": 5, "clean": 1}[p.Micro]
		}
		if p.Instruct != "" {
			st["geometry_instruct_mode"] = p.Instruct
		}
	}
	if a := in.Advanced; a != nil && advancedSettings(a, st) {
		st["enable_advance_option"] = true
	}
	if len(in.BBox) == 3 {
		// The editor's params: width, height, length and a fourth value the
		// web keeps at 1 (notes 14 section 2.5, open O12).
		st["bbox_condition"] = []float64{in.BBox[0], in.BBox[1], in.BBox[2], 1}
	}
	return st, nil
}

// advancedSettings writes the advanced options into settings (the web's
// keys) and reports whether any was set.
func advancedSettings(a *Advanced, st map[string]any) bool {
	set := false
	if a.Seed != nil {
		st["seed"], set = *a.Seed, true
	}
	if a.NegativePrompt != "" {
		st["negative_prompt"], set = a.NegativePrompt, true
	}
	if a.Cfg != nil {
		st["guidance_scale"], set = *a.Cfg, true
	}
	if a.Steps != nil {
		st["inference_steps"], set = *a.Steps, true
	}
	if a.Detail != nil {
		st["sharp_bias"], set = *a.Detail, true
	}
	return set
}

// copySettings fills the unset generate fields of in from a task's card
// and returns what that task did in its later stages. Seeds and advanced
// options are not copied: they belong to that task's own images, and the
// web never sends them with a first submit.
func copySettings(in *GenerateInput, card *rodin.Card) *copiedRecipe {
	if in.Model == "" {
		in.Model = stage.Model(card.Setting("generation_mode"))
	}
	if in.Tier == "" && in.Model == "gen-2.5" {
		in.Tier = stage.Tier(card.Setting("brilliant_mode"))
	}
	if in.Mode == "" && in.Model == "gen-1.5" {
		in.Mode = stage.Gen15Mode(card.Setting("generation_mode"))
	}
	if in.ImageMode == "" {
		if m := card.Setting("image_mode"); m == "recon" || m == "rest" {
			in.ImageMode = m
		}
	}
	// face_enhance is not copied: whether it is offered depends on the new
	// image (the preprocess step), whose default the submit then uses.
	var skipped []string
	if in.Presets == nil {
		p := &Presets{}
		if v, err := strconv.Atoi(card.Setting("symmetric_bias")); err == nil && v > 0 {
			p.Symmetric = "symmetric"
			p.SymmetricStrength = map[int]int{2: 2, 3: 3}[v]
			if p.SymmetricStrength == 0 {
				p.SymmetricStrength = 1
			}
		}
		if v, err := strconv.Atoi(card.Setting("detail_level")); err == nil && v >= 1 && v <= 3 {
			p.Detailed = v
		}
		// micro and instruct exist only on some Gen-2.5 tiers (checkPresets):
		// a copy onto a model or tier without them leaves them out, as the
		// web's panel would not show them.
		model, tier := strings.ToLower(strings.TrimSpace(in.Model)), strings.ToLower(strings.TrimSpace(in.Tier))
		if model == "" {
			model = "gen-2.5"
		}
		micro := map[string]string{"5": "micro", "1": "clean"}[card.Setting("micro_bias")]
		if micro != "" && model == "gen-2.5" && tier == "extreme-high" {
			p.Micro = micro
		} else if micro != "" {
			skipped = append(skipped, "micro")
		}
		if m := card.Setting("geometry_instruct_mode"); m == "creative" || m == "faithful" {
			if model == "gen-2.5" && oneOf(tier, "medium", "high", "extreme-high") {
				p.Instruct = m
			} else {
				skipped = append(skipped, "instruct")
			}
		}
		if *p != (Presets{}) {
			in.Presets = p
		}
	}
	if len(in.BBox) == 0 {
		if b, ok := card.Settings["bbox_condition"].([]any); ok && len(b) >= 3 {
			for _, v := range b[:3] {
				if f, ok := v.(float64); ok {
					in.BBox = append(in.BBox, f)
				}
			}
			if len(in.BBox) != 3 {
				in.BBox = nil
			}
		}
	}

	r := &copiedRecipe{From: card.TaskUUID, PresetsNotCopied: skipped}
	put := func(m *map[string]any, k string, v any) {
		if v == nil || v == "" {
			return
		}
		if *m == nil {
			*m = map[string]any{}
		}
		(*m)[k] = v
	}
	switch card.Setting("mesh_export_type") {
	case "Raw":
		put(&r.Confirm, "mesh", "raw")
	case "Quad":
		put(&r.Confirm, "mesh", "quad")
	}
	if v, err := strconv.Atoi(card.Setting("quality_override")); err == nil && v > 0 {
		put(&r.Confirm, "faces", v)
	}
	if v := card.Setting("quad_normal"); v == "true" {
		put(&r.Confirm, "bake_normal", true)
	}
	if v := card.Setting("symmetric"); v == "false" {
		put(&r.Confirm, "symmetric", false)
	}
	put(&r.Texture, "texture_mode", card.Setting("texture_mode"))
	if v, err := strconv.ParseFloat(card.Setting("escore"), 64); err == nil {
		put(&r.Texture, "escore", v)
	}
	for web, name := range map[string]string{"superres_texture": "hd_texture", "sharp_texture": "sharp", "face_enhance": "face_enhance", "texture_delight": "delight"} {
		if card.Setting(web) == "true" {
			put(&r.Texture, name, true)
		}
	}
	put(&r.Export, "format", card.Setting("geometry_file_format"))
	put(&r.Export, "geometry", map[string]string{"Base": "base", "Lod": "lod", "HighPoly": "high_poly"}[card.Setting("geometry")])
	put(&r.Export, "material", map[string]string{"All": "all", "PBR": "pbr", "Shaded": "shaded"}[card.Setting("material")])
	if card.Setting("resolution") == "Basic" {
		put(&r.Export, "resolution", "2k") // Basic is 1k, 2k or 6k; 2k is the web default
	}
	return r
}
