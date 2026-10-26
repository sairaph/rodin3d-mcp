package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

// Wire bodies of this file follow the web app's builders (notes 14
// sections 2.4-2.7, 3.3 and 4.4; main.3620f51a.js API module and call
// sites).

type redoDetail struct {
	Version  string `yaml:"version,omitempty"`
	RedoLeft *int   `yaml:"redo_left,omitempty"`
}

// --- redo_geometry ---

// RedoGeometryInput is the input of redo_geometry.
type RedoGeometryInput struct {
	TaskID   string
	Prompt   string
	Advanced *Advanced
	Expected *float64
	Wait     bool
}

// redoCarry are the draft's own geometry settings a redo repeats (the web
// rebuilds them from the card and its panels; seeds are not repeated, or
// the redo would give the same draft).
var redoCarry = []string{
	"view_weights", "condition_mode", "image_label", "bbox_condition", "voxel_condition", "voxel_condition_cfg",
	"voxel_condition_weight", "pcd_condition", "pcd_condition_uncertainty", "spatial_condition", "sharp_bias",
	"detail_level", "image_mode", "rest_bias", "highpoly_bias", "symmetric_bias", "micro_bias", "geometry_instruct_mode",
}

// RedoGeometry is redo_geometry: a new version of the geometry draft
// (task/rodin_update, stage geometry).
func (s *Service) RedoGeometry(ctx context.Context, in RedoGeometryInput) (Result, error) {
	if _, err := s.permit("redo_geometry"); err != nil {
		return Result{}, err
	}
	if err := checkAdvanced(in.Advanced, nil, ""); err != nil {
		return Result{}, err
	}
	if len([]rune(in.Prompt)) > maxPrompt {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("prompt is longer than %d characters.", maxPrompt), "Shorten the prompt.")
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "redo_geometry")
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "redo_geometry", uuid, pin, in.Expected, false)
	if err != nil {
		return Result{}, err
	}
	hist, err := s.client.GeometryHistory(ctx, uuid)
	if err != nil {
		return Result{}, wrapf(err, "read the geometry versions")
	}
	geoLimit, _ := stage.RedoLimits(sp.info.Meta.Privileges)
	if left := stage.RedosLeft(geoLimit, len(hist)); left <= 0 {
		return Result{}, failFields(render.CodeConflict, fmt.Sprintf("No geometry redo is left: the plan allows %d and the draft has %d versions.", geoLimit, len(hist)),
			"Confirm one of the versions with confirm_geometry (version picks it), or fork the task.", map[string]any{"versions": len(hist), "limit": geoLimit})
	}

	settings := map[string]any{}
	for _, k := range redoCarry {
		if v, ok := card.Settings[k]; ok {
			settings[k] = v
		}
	}
	defaults := map[string]any{
		"condition_mode": "concat", "image_mode": "recon", "voxel_condition_cfg": true, "voxel_condition_weight": 1, "highpoly_bias": 0,
		"enable_advance_option": false, "lineart_enable": false, "face_enhance": false, "superres_texture": false, "sharp_texture": false,
		"texture_delight": false, "model_partial_reference_version": nil, "model_partial_reference_bbox": nil,
		"model_refine_reference_version": nil, "model_refine_reference_strength": nil, "model_reference_use_preprocess": nil,
		"texture_partial_reference_version": nil, "texture_partial_reference_bbox": nil, "texture_partial_reference_image_name": nil,
	}
	for k, v := range defaults {
		if _, ok := settings[k]; !ok {
			settings[k] = v
		}
	}
	if in.Advanced != nil && advancedSettings(in.Advanced, settings) {
		settings["enable_advance_option"] = true
		if in.Advanced.Detail != nil {
			settings["detail_level"] = nil // the web's advanced detail replaces the level preset
		}
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		prompt = card.Prompt
	}

	sp.open()
	reply, err := s.client.Update(ctx, uuid, prompt, "", settings)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("redo_geometry", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	detail := redoDetail{}
	if h, err := s.client.GeometryHistory(ctx, uuid); err == nil {
		detail.Version = assets.LatestVersion(keysOf(h))
		left := stage.RedosLeft(geoLimit, len(h))
		detail.RedoLeft = &left
	}
	res := s.finishFlow(ctx, sp, set, "Geometry redo sent.", detail)
	return res, nil
}

// --- confirm_geometry ---

// ConfirmGeometryInput is the input of confirm_geometry.
type ConfirmGeometryInput struct {
	TaskID       string
	Mesh         string // raw | quad
	Quality      string // quad: extra-low low medium high; smart: low medium high
	Faces        *int
	SmartLowPoly bool
	BakeNormal   *bool
	Symmetric    *bool
	Hyper        *bool
	Smooth       *bool
	Version      string
	Expected     *float64
	Wait         bool
}

var quadFaces = map[string]int{"extra-low": 4000, "low": 8000, "medium": 18000, "high": 50000}

// ConfirmGeometry is confirm_geometry: confirm a geometry draft version
// with its mesh options (task/rodin_confirm, or task/rodin_polygen for
// smart low-poly).
func (s *Service) ConfirmGeometry(ctx context.Context, in ConfirmGeometryInput) (Result, error) {
	if _, err := s.permit("confirm_geometry"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "confirm_geometry")
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{SmartLowPoly: in.SmartLowPoly}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "confirm_geometry", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	hist, err := s.client.GeometryHistory(ctx, uuid)
	if err != nil {
		return Result{}, wrapf(err, "read the geometry versions")
	}
	version, err := pickVersion(in.Version, keysOf(hist), "geometry")
	if err != nil {
		return Result{}, err
	}
	settings, err := confirmSettings(in, card, sp.info)
	if err != nil {
		return Result{}, err
	}
	body := map[string]any{"uuid": uuid, "settings": settings, "version": version}

	sp.open()
	reply, err := s.client.Confirm(ctx, body, in.SmartLowPoly)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("confirm_geometry", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	mesh := fmt.Sprintf("%v", settings["mesh_export_type"])
	if f, ok := settings["quality_override"].(int); ok {
		mesh += fmt.Sprintf(", %d faces", f)
	}
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Geometry %s confirmed (%s).", version, mesh), map[string]any{"version": version, "settings": settings})
	return res, nil
}

// pickVersion returns want when versions holds it, else the newest
// version when want is empty.
func pickVersion(want string, versions []string, what string) (string, error) {
	if len(versions) == 0 {
		if want != "" {
			return want, nil // no history to check against: the server decides
		}
		return "v1", nil
	}
	if want == "" {
		return assets.LatestVersion(versions), nil
	}
	for _, v := range versions {
		if v == want {
			return want, nil
		}
	}
	return "", fail(render.CodeInvalidInput, fmt.Sprintf("The task has no %s version %q.", what, want),
		"Pick one of: "+strings.Join(assets.SortVersions(versions), ", ")+" (get_task lists them).")
}

// confirmSettings builds the geometry confirm settings (web Fs builder,
// main@fmt:28340-28440).
func confirmSettings(in ConfirmGeometryInput, card *rodin.Card, info *rodin.UserInfo) (map[string]any, error) {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	mesh := strings.ToLower(strings.TrimSpace(in.Mesh))
	quality := strings.ToLower(strings.TrimSpace(in.Quality))
	symmetric, hyper := optTrue(in.Symmetric), optTrue(in.Hyper)
	bake, smooth := in.BakeNormal != nil && *in.BakeNormal, in.Smooth != nil && *in.Smooth

	if in.SmartLowPoly {
		if in.Faces != nil || smooth || bake {
			return nil, bad("faces, smooth and bake_normal do not apply to smart low-poly.", "Pass quality (low, medium, high) and mesh (raw or quad) only.")
		}
		if quality == "" {
			quality = "medium"
		}
		if !oneOf(quality, "low", "medium", "high") {
			return nil, bad(fmt.Sprintf("quality %q is not low, medium or high for smart low-poly.", quality), "Pass low, medium or high.")
		}
		if mesh == "" {
			mesh = "quad"
		}
		if !oneOf(mesh, "raw", "quad") {
			return nil, bad(fmt.Sprintf("mesh %q is not raw or quad.", mesh), "Pass raw or quad.")
		}
		return map[string]any{
			"quality": quality, "symmetric": symmetric, "use_hyper": true, "mesh_export_type": map[string]string{"raw": "Raw", "quad": "Quad"}[mesh],
			"mesh_smooth": false, "mesh_simplify": false, "quality_override": nil, "quad_normal": false, "model_multi_udim": []any{},
		}, nil
	}

	if mesh == "" {
		mesh = "quad"
		if info.HasPrivilege("RodinMeshAdditionalFeatures") {
			mesh = "raw" // every confirmed task in the owner's history is Raw (Triangle)
		}
	}
	gen := stage.Model(card.Setting("generation_mode"))
	tier := stage.Tier(card.Setting("brilliant_mode"))
	lo, hi := 500, 1000000
	switch {
	case gen == "gen-1.5":
		lo, hi = 500, 200000
	case tier == "high" || tier == "extreme-high":
		lo, hi = 20000, 2000000
	}
	st := map[string]any{"quality": "medium", "symmetric": symmetric, "use_hyper": hyper, "model_multi_udim": []any{}}
	switch mesh {
	case "raw":
		if !info.HasPrivilege("RodinMeshAdditionalFeatures") {
			return nil, &price.Gate{Reason: "the raw (Triangle) mesh needs the RodinMeshAdditionalFeatures privilege (Creator plan or higher); confirm with mesh quad"}
		}
		if quality != "" || smooth {
			return nil, bad("quality and smooth apply to the quad mesh.", "For a raw mesh pass faces (or nothing for the full mesh).")
		}
		st["mesh_export_type"], st["mesh_smooth"], st["mesh_simplify"], st["quality_override"], st["quad_normal"] = "Raw", false, false, nil, false
		if in.Faces != nil {
			if *in.Faces < lo || *in.Faces > hi {
				return nil, bad(fmt.Sprintf("faces %d is outside %d-%d for this task.", *in.Faces, lo, hi), fmt.Sprintf("Pass faces from %d to %d, or leave it out for the full mesh.", lo, hi))
			}
			st["mesh_simplify"], st["quality_override"] = true, *in.Faces
		}
		if bake {
			if in.Faces == nil || *in.Faces > 20000 {
				return nil, bad("bake_normal on a raw mesh needs faces of 20000 or less.", "Pass faces <= 20000, or leave bake_normal out.")
			}
			st["quad_normal"] = true
		}
	case "quad":
		faces := 0
		switch {
		case in.Faces != nil:
			if quality != "" {
				return nil, bad("Pass quality or faces for a quad mesh, not both.", "faces sets the count; quality picks 4000, 8000, 18000 or 50000.")
			}
			if *in.Faces < lo || *in.Faces > hi {
				return nil, bad(fmt.Sprintf("faces %d is outside %d-%d for this task.", *in.Faces, lo, hi), fmt.Sprintf("Pass faces from %d to %d.", lo, hi))
			}
			faces = *in.Faces
		default:
			if quality == "" {
				quality = "medium"
			}
			f, ok := quadFaces[quality]
			if !ok {
				return nil, bad(fmt.Sprintf("quality %q is not extra-low, low, medium or high.", quality), "Pass one of the listed qualities.")
			}
			faces = f
		}
		if (in.Faces != nil || quality != "medium") && !info.HasPrivilege("RodinMeshAdditionalFeatures") {
			return nil, &price.Gate{Reason: "choosing the quad face count needs the RodinMeshAdditionalFeatures privilege (Creator plan or higher)"}
		}
		st["mesh_export_type"], st["mesh_smooth"], st["mesh_simplify"], st["quality_override"], st["quad_normal"] = "Quad", smooth, false, faces, bake
	default:
		return nil, bad(fmt.Sprintf("mesh %q is not raw or quad.", mesh), "Pass raw (Triangle) or quad.")
	}
	return st, nil
}

func optTrue(p *bool) bool { return p == nil || *p }

// --- generate_texture ---

// TextureInput is the input of generate_texture and, without the
// reference, of redo_material.
type TextureInput struct {
	TaskID         string
	TextureMode    string
	HD             *bool
	Sharp          *bool
	Delight        *bool
	FaceEnhance    *bool
	Escore         *float64
	ReferenceScale *float64
	ReferenceImage string // generate_texture: input image name, "ALL" or a local path
	Prompt         string
	Seed           string // redo_material: keep | new
	Expected       *float64
	Wait           bool
}

// textureSettings builds the texture settings (web ct builder,
// main@5360100); defaults come from base (the card's settings for a redo,
// nil for a first texture).
func textureSettings(in TextureInput, card *rodin.Card, info *rodin.UserInfo, base map[string]any) (map[string]any, string, error) {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	boolOf := func(p *bool, key string) bool {
		if p != nil {
			return *p
		}
		v, _ := base[key].(bool)
		return v
	}
	gen := stage.Model(card.Setting("generation_mode"))
	mode := strings.ToLower(strings.TrimSpace(in.TextureMode))
	if mode == "" {
		if m, ok := base["texture_mode"].(string); ok && m != "" {
			mode = m
		}
	}
	if gen == "gen-1.5" {
		if mode != "" && mode != "legacy" {
			return nil, "", bad("texture_mode applies to gen-2.5 tasks; gen-1.5 textures use the legacy mode.", "Leave texture_mode out.")
		}
		mode = "legacy"
	} else {
		if mode == "" {
			mode = "high" // the web panel's default tier
		}
		if !oneOf(mode, "extreme-low", "low", "medium", "high", "extreme-high") {
			return nil, "", bad(fmt.Sprintf("texture_mode %q is not one of extreme-low, low, medium, high, extreme-high.", mode), "Pass one of the listed tiers.")
		}
	}
	escore := 7.0 // the web panel's default
	if v, ok := base["escore"].(float64); ok {
		escore = v
	}
	if in.Escore != nil {
		escore = *in.Escore
	}
	if escore < 0 || escore > 10 || math.Mod(escore*2, 1) != 0 {
		return nil, "", bad(fmt.Sprintf("escore %g is not 0-10 in steps of 0.5.", escore), "Pass escore from 0 to 10 in steps of 0.5 (web default 7).")
	}
	var refScale any // null for gen-2.5
	if gen == "gen-1.5" {
		rs := 1.0
		if v, ok := base["reference_scale"].(float64); ok {
			rs = v
		}
		if in.ReferenceScale != nil {
			rs = *in.ReferenceScale
		}
		if rs < 0 || rs > 1 {
			return nil, "", bad(fmt.Sprintf("reference_scale %g is outside 0-1.", rs), "Pass reference_scale from 0 to 1.")
		}
		refScale = rs
	} else if in.ReferenceScale != nil {
		return nil, "", bad("reference_scale applies to gen-1.5 tasks.", "Leave reference_scale out.")
	}
	hd, sharp, face := boolOf(in.HD, "superres_texture"), boolOf(in.Sharp, "sharp_texture"), boolOf(in.FaceEnhance, "face_enhance")
	if mode == "extreme-high" {
		if (in.HD != nil && *in.HD) || (in.Sharp != nil && *in.Sharp) || (in.FaceEnhance != nil && *in.FaceEnhance) {
			return nil, "", bad("hd_texture, sharp and face_enhance do not apply at texture_mode extreme-high.", "Leave them out at extreme-high.")
		}
		hd, sharp, face = false, false, false
	}
	if (hd || sharp) && !info.HasPrivilege("RodinTextureAdditionalFeatures") {
		return nil, "", &price.Gate{Reason: "hd_texture and sharp need the RodinTextureAdditionalFeatures privilege (Creator plan or higher)"}
	}
	st := map[string]any{
		"escore": escore, "reference_scale": refScale, "face_enhance": face, "superres_texture": hd, "sharp_texture": sharp,
		"texture_delight": boolOf(in.Delight, "texture_delight"), "texture_partial_reference_version": nil,
		"texture_partial_reference_bbox": nil, "texture_partial_reference_image_name": nil, "rodin_smart_uv_version": nil, "texture_mode": mode,
	}
	return st, mode, nil
}

// inputNames lists the image names a texture may use as reference, in
// upload order.
func inputNames(card *rodin.Card) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []rodin.StringMap{card.Info.MeshInputImages, card.Info.MaterialInputImages, card.Info.MeshReferenceImages} {
		for n := range m {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sortInputNames(out)
	return out
}

// GenerateTexture is generate_texture: the material draft of a confirmed
// geometry (task/rodin_texture).
func (s *Service) GenerateTexture(ctx context.Context, in TextureInput) (Result, error) {
	if _, err := s.permit("generate_texture"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "generate_texture")
	if err != nil {
		return Result{}, err
	}
	req := rodin.TextureRequest{UUID: uuid, Prompt: strings.TrimSpace(in.Prompt)}
	names := inputNames(card)
	ref := strings.TrimSpace(in.ReferenceImage)
	switch {
	case filepath.IsAbs(ref):
		imgs, err := prepareImages([]GenerateImage{{Path: ref}})
		if err != nil {
			return Result{}, err
		}
		up := imgs[0].upload
		req.NewReference = &up
	case ref == "":
		// The web's default: every input image ("ALL", multiple-reference
		// mode) when there are several, else the one input image.
		switch {
		case len(card.Info.MeshInputImages) > 1:
			ref = "ALL"
		case len(names) > 0:
			ref = names[0]
		default:
			return Result{}, fail(render.CodeConflict, "The task lists no input image to use as the texture reference.", "Pass reference_image: the absolute path of an image to texture from.")
		}
		req.ReferenceFilename = ref
	case ref == "ALL" || contains(names, ref):
		req.ReferenceFilename = ref
	default:
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("reference_image %q is not an input image of the task.", ref),
			"Pass ALL, one of "+orNone(names)+", or the absolute path of a new image.")
	}
	pin := price.Input{Operation: "generate_texture", TextureMode: in.TextureMode}
	pinFromCard(&pin, card)
	if pin.Model == "gen-1.5" {
		pin.TextureMode = "legacy"
	}
	sp, err := s.prepare(ctx, "generate_texture", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	settings, mode, err := textureSettings(in, card, sp.info, nil)
	if err != nil {
		return Result{}, err
	}
	req.Settings = settings

	sp.open()
	reply, err := s.client.Texture(ctx, req)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("generate_texture", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	refName := firstNonEmptyString(reply.ReferenceFilename, req.ReferenceFilename, "new image")
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Texture (%s) started from reference %s.", mode, refName), map[string]any{"reference": refName, "settings": settings})
	return res, nil
}

func firstNonEmptyString(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// materialRef reads the material history and the reference the task's
// material belongs to.
func (s *Service) materialRef(ctx context.Context, uuid string, card *rodin.Card) (string, []string, error) {
	mat, err := s.client.MaterialHistory(ctx, uuid)
	if err != nil {
		return "", nil, wrapf(err, "read the material versions")
	}
	ref := assets.MaterialRef(card, assets.History{Material: mat})
	if ref == "" {
		// Several references and none named on the card: the newest one.
		refs := keysOf(mat)
		sort.Strings(refs)
		if len(refs) == 0 {
			return "", nil, fail(render.CodeConflict, "The task has no material version yet.", "Call get_task to see its stage.")
		}
		ref = refs[len(refs)-1]
	}
	return ref, keysOf(mat[ref]), nil
}

// --- redo_material ---

// RedoMaterial is redo_material: a new version of the material draft
// (task/rodin_update, stage material). Unset texture fields repeat the
// draft's.
func (s *Service) RedoMaterial(ctx context.Context, in TextureInput) (Result, error) {
	if _, err := s.permit("redo_material"); err != nil {
		return Result{}, err
	}
	seed := 0 // "new": the web's default seed value
	switch strings.ToLower(strings.TrimSpace(in.Seed)) {
	case "", "new":
	case "keep":
		seed = -1
	default:
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("seed %q is not keep or new.", in.Seed), "Pass keep (same seed) or new.")
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "redo_material")
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{Operation: "redo_material", TextureMode: in.TextureMode}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "redo_material", uuid, pin, in.Expected, false)
	if err != nil {
		return Result{}, err
	}
	ref, versions, err := s.materialRef(ctx, uuid, card)
	if err != nil {
		return Result{}, err
	}
	_, matLimit := stage.RedoLimits(sp.info.Meta.Privileges)
	if left := stage.RedosLeft(matLimit, len(versions)); left <= 0 {
		return Result{}, failFields(render.CodeConflict, fmt.Sprintf("No material redo is left: the plan allows %d and the draft has %d versions.", matLimit, len(versions)),
			"Confirm a version with confirm_material, or fork the task.", map[string]any{"versions": len(versions), "limit": matLimit})
	}
	settings, mode, err := textureSettings(in, card, sp.info, card.Settings)
	if err != nil {
		return Result{}, err
	}
	settings["seed"] = seed
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		prompt = card.Prompt
	}

	sp.open()
	reply, err := s.client.Update(ctx, uuid, prompt, ref, settings)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("redo_material", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	detail := redoDetail{}
	if _, now, err := s.materialRef(ctx, uuid, set.card); err == nil {
		detail.Version = assets.LatestVersion(now)
		left := stage.RedosLeft(matLimit, len(now))
		detail.RedoLeft = &left
	}
	return s.finishFlow(ctx, sp, set, fmt.Sprintf("Material redo (%s) sent for reference %s.", mode, ref), detail), nil
}

// --- confirm_material ---

// ConfirmMaterialInput is the input of confirm_material.
type ConfirmMaterialInput struct {
	TaskID   string
	Version  string
	Expected *float64
	Wait     bool
}

// ConfirmMaterial is confirm_material: confirm a material draft version
// (task/rodin_confirm, stage material).
func (s *Service) ConfirmMaterial(ctx context.Context, in ConfirmMaterialInput) (Result, error) {
	if _, err := s.permit("confirm_material"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "confirm_material")
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "confirm_material", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	ref, versions, err := s.materialRef(ctx, uuid, card)
	if err != nil {
		return Result{}, err
	}
	version, err := pickVersion(in.Version, versions, "material")
	if err != nil {
		return Result{}, err
	}
	body := map[string]any{"uuid": uuid, "settings": map[string]any{"rodin_smart_uv_version": nil}, "version": version, "reference_image": ref}

	sp.open()
	reply, err := s.client.Confirm(ctx, body, false)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("confirm_material", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, reply.Job.UUIDs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	return s.finishFlow(ctx, sp, set, fmt.Sprintf("Material %s (reference %s) confirmed.", version, ref), map[string]any{"version": version, "reference": ref}), nil
}

// --- export_model ---

// ExportInput is the input of export_model.
type ExportInput struct {
	TaskID      string
	Type        string // mesh | material; default by stage
	Format      string
	Resolution  string
	Geometry    string
	Material    string
	AlignGround *bool
	Height      *float64
	Expected    *float64
	Wait        bool
}

type exportDetail struct {
	Pack  map[string]any `yaml:"pack"`
	Files []string       `yaml:"files,omitempty,flow"`
}

// Export is export_model: pack the task's files for download
// (task/rodin_unlock).
func (s *Service) Export(ctx context.Context, in ExportInput) (Result, error) {
	if _, err := s.permit("export_model"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "export_model")
	if err != nil {
		return Result{}, err
	}
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	want := "mesh"
	if st.Stage == stage.Material {
		want = "material"
	}
	if typ == "" {
		typ = want
	}
	if typ != want {
		return Result{}, bad(fmt.Sprintf("type %s does not fit a task at the %s: it packs %s.", typ, describeStage(st), want), "Leave type out; it follows the stage.")
	}
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "obj" // the web's default
	}
	if !oneOf(format, "obj", "fbx", "glb", "usdz", "stl") {
		return Result{}, bad(fmt.Sprintf("format %q is not one of obj, fbx, glb, usdz, stl.", format), "Pass one of the listed formats.")
	}
	geometry := strings.ToLower(strings.TrimSpace(in.Geometry))
	if geometry == "" {
		geometry = "base"
	}
	geoWire, ok := map[string]string{"base": "Base", "lod": "Lod", "high_poly": "HighPoly"}[geometry]
	if !ok {
		return Result{}, bad(fmt.Sprintf("geometry %q is not base, lod or high_poly.", geometry), "Pass one of the listed values.")
	}
	if in.Height != nil && *in.Height <= 0 {
		return Result{}, bad("height must be above 0.", "Pass the real-world height, or leave it out.")
	}
	body := map[string]any{"task_uuid": uuid, "type": typ, "geometry": geoWire, "geometry_file_format": format,
		"align_ground": optTrue(in.AlignGround), "rodin_smart_uv_version": nil}
	if in.Height != nil {
		body["height"] = *in.Height
	}
	resolution := strings.ToLower(strings.TrimSpace(in.Resolution))
	if typ == "material" {
		if resolution == "" {
			resolution = "2k"
			if card.Setting("generation_mode") == "Fast" {
				resolution = "1k"
			}
		}
		wire, ok := map[string]string{"1k": "Basic", "2k": "Basic", "6k": "Basic", "4k": "High", "12k": "High"}[resolution]
		if !ok {
			return Result{}, bad(fmt.Sprintf("resolution %q is not one of 1k, 2k, 4k, 6k, 12k.", resolution), "Pass 2k (Basic) unless the plan allows High (4k); 6k and 12k are for extreme-high textures.")
		}
		if (resolution == "6k" || resolution == "12k") && card.Setting("texture_mode") != "extreme-high" {
			return Result{}, bad(resolution+" packs exist only for texture_mode extreme-high.", "Pass 2k (or 4k with the Business plan).")
		}
		material := strings.ToLower(strings.TrimSpace(in.Material))
		if material == "" {
			material = "all"
		}
		matWire, ok := map[string]string{"all": "All", "pbr": "PBR", "shaded": "Shaded"}[material]
		if !ok {
			return Result{}, bad(fmt.Sprintf("material %q is not all, pbr or shaded.", material), "Pass one of the listed values.")
		}
		body["resolution"], body["material"] = wire, matWire
	} else if resolution != "" || in.Material != "" {
		return Result{}, bad("resolution and material apply to a textured task's pack.", "Leave them out for a geometry pack.")
	}
	pin := price.Input{Resolution: resolution, Geometry: geometry}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "export_model", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}

	sp.open()
	reply, err := s.client.Unlock(ctx, body)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("export_model", uuid, err)
	}
	var jobs []string
	if r, err := rodinJobs(reply); err == nil {
		jobs = r
	}
	set, err := s.settle(ctx, uuid, st, jobs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	detail := exportDetail{Pack: body}
	delete(detail.Pack, "task_uuid")
	if set.status.State == stage.Packed {
		if items, err := s.client.GetDownload(ctx, uuid); err == nil {
			for _, it := range items {
				detail.Files = append(detail.Files, it.Name)
			}
		}
	}
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Pack requested (%s, %s).", typ, format), detail)
	if len(detail.Files) > 0 {
		res.Body += "\n\nPacked files: " + strings.Join(detail.Files, ", ") + ". download_model saves them."
	}
	return res, nil
}

// rodinJobs reads the job ids of a raw reply.
func rodinJobs(raw []byte) ([]string, error) {
	var r rodin.JobReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return r.Job.UUIDs, nil
}

// --- fork_task ---

// ForkInput is the input of fork_task.
type ForkInput struct {
	TaskID   string
	Stage    string // geometry | material
	Expected *float64
	Wait     bool
}

// Fork is fork_task: a new task branched from the geometry or the material
// stage of a task (task/rodin_fork).
func (s *Service) Fork(ctx context.Context, in ForkInput) (Result, error) {
	if _, err := s.permit("fork_task"); err != nil {
		return Result{}, err
	}
	from := strings.ToLower(strings.TrimSpace(in.Stage))
	state, ok := map[string]string{"geometry": rodin.StepModelGenerate, "material": rodin.StepTextureGenerate}[from]
	if !ok {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("stage %q is not geometry or material.", in.Stage), "Pass geometry (reopen the geometry tools) or material (reopen the material tools).")
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "fork_task")
	if err != nil {
		return Result{}, err
	}
	if from == "material" && st.Stage != stage.MaterialDraft && st.Stage != stage.Material {
		return Result{}, fail(render.CodeConflict, "The task has no material to fork from yet.", "Fork stage geometry, or texture the task first.")
	}
	pin := price.Input{}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "fork_task", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}

	sp.open()
	newID, err := s.client.Fork(ctx, uuid, state)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("fork_task", uuid, err)
	}
	sp.uuid = newID
	set, err := s.settle(ctx, newID, stage.Status{}, nil, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, wrapf(err, "the fork %s was created; read its card", newID)
	}
	return s.finishFlow(ctx, sp, set, fmt.Sprintf("Forked %s at the %s stage as new task %s.", short(uuid), from, newID),
		map[string]any{"forked_from": uuid, "stage": from}), nil
}

// --- select_candidate ---

// SelectInput is the input of select_candidate.
type SelectInput struct {
	TaskID    string
	Candidate string
}

// SelectCandidate is select_candidate: pick one draft of a batch
// (task/select_candidate). The candidates are the draft's geometry versions.
func (s *Service) SelectCandidate(ctx context.Context, in SelectInput) (Result, error) {
	if _, err := s.permit("select_candidate"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "select_candidate")
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "select_candidate", uuid, pin, nil, false)
	if err != nil {
		return Result{}, err
	}
	hist, err := s.client.GeometryHistory(ctx, uuid)
	if err != nil {
		return Result{}, wrapf(err, "read the geometry versions")
	}
	cand := strings.TrimSpace(in.Candidate)
	if cand == "" || !contains(keysOf(hist), cand) {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("candidate %q is not a version of the draft.", in.Candidate),
			"Pass one of: "+orNone(assets.SortVersions(keysOf(hist)))+" (get_task lists them).")
	}

	sp.open()
	if _, err := s.client.SelectCandidate(ctx, uuid, cand); err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("select_candidate", uuid, err)
	}
	set, err := s.settle(ctx, uuid, st, nil, false)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	return s.finishFlow(ctx, sp, set, fmt.Sprintf("Candidate %s selected.", cand), map[string]any{"candidate": cand}), nil
}
