package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// paidTool: changes a task on Hyper3D and may spend credits; never repeat
// blindly.
var paidTool = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(true)}

type imageArg struct {
	Path   string   `json:"path" jsonschema:"absolute path of a jpg, png or webp image (larger than 1024 px is scaled down like the web does)"`
	View   *string  `json:"view,omitempty" jsonschema:"the direction the image shows the object from (default ?, unknown)"`
	Weight *float64 `json:"weight,omitempty" jsonschema:"relative weight of this image (0-1); give every image one or none; normalised to sum 1"`
}

type presetsArg struct {
	Micro             *string `json:"micro,omitempty" jsonschema:"micro (fine surface detail) or clean; gen-2.5 extreme-high only"`
	Instruct          *string `json:"instruct,omitempty" jsonschema:"creative (corrects proportions) or faithful (keeps the input perspective); gen-2.5 medium and above"`
	Symmetric         *string `json:"symmetric,omitempty" jsonschema:"symmetric or asymmetric geometry"`
	SymmetricStrength *int    `json:"symmetric_strength,omitempty" jsonschema:"strength of symmetric, 1-3 (default 1)"`
	Detailed          *int    `json:"detailed,omitempty" jsonschema:"surface relief level, 1-3"`
	Soft              *bool   `json:"soft,omitempty" jsonschema:"smooth edges"`
}

type advancedArg struct {
	Seed           *int    `json:"seed,omitempty" jsonschema:"0-65535; default random"`
	NegativePrompt *string `json:"negative_prompt,omitempty" jsonschema:"what the geometry should not have"`
	Cfg            *int    `json:"cfg,omitempty" jsonschema:"guidance scale 1-15 (web default 10)"`
	Steps          *int    `json:"steps,omitempty" jsonschema:"inference steps 10-100 (web default 50)"`
	Detail         *int    `json:"detail,omitempty" jsonschema:"sharpness -5 to 5"`
}

type generateArgs struct {
	Images           []imageArg   `json:"images" jsonschema:"1-5 images of one object; several need views tagged for a good result"`
	Prompt           *string      `json:"prompt,omitempty" jsonschema:"caption override (at most 1024 characters); default the caption Hyper3D writes for the first image"`
	Model            *string      `json:"model,omitempty" jsonschema:"gen-2.5 (default) or gen-1.5"`
	Tier             *string      `json:"tier,omitempty" jsonschema:"gen-2.5 tier (required for gen-2.5 unless copy_settings_from gives it)"`
	Mode             *string      `json:"mode,omitempty" jsonschema:"gen-1.5 mode (default default)"`
	ImageMode        *string      `json:"image_mode,omitempty" jsonschema:"recon (default) or rest"`
	FaceEnhance      *bool        `json:"face_enhance,omitempty" jsonschema:"enhance faces; default what Hyper3D suggests for the image; refused where it is not offered"`
	Presets          *presetsArg  `json:"presets,omitempty" jsonschema:"the web's presets; default none (Hyper3D's recommendation)"`
	Advanced         *advancedArg `json:"advanced,omitempty" jsonschema:"the web's advanced options"`
	BBox             []float64    `json:"bbox,omitempty" jsonschema:"bounding box control [width, height, length], each 1-300"`
	Batch            *int         `json:"batch,omitempty" jsonschema:"drafts per submit; more than 1 needs the Business plan"`
	CopySettingsFrom *string      `json:"copy_settings_from,omitempty" jsonschema:"a task id whose model, tier, mode and presets fill the fields left out; the reply's recipe lists its later-stage settings"`
	Public           *bool        `json:"public,omitempty" jsonschema:"true keeps the new task public on Hyper3D; default false: the task is made private right after the submit"`
	ExpectedCredits  float64      `json:"expected_credits" jsonschema:"the credits quote_cost returned for generate_model with the same model, tier and batch; -1 accepts a price quote_cost reports as not known"`
	Wait             *bool        `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the draft (default true); after a time-out call wait_task"`
}

type redoGeometryArgs struct {
	TaskID          string       `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Prompt          *string      `json:"prompt,omitempty" jsonschema:"new caption (default the task's prompt)"`
	Advanced        *advancedArg `json:"advanced,omitempty" jsonschema:"the web's advanced options for this redo"`
	ExpectedCredits *float64     `json:"expected_credits,omitempty" jsonschema:"needed only when quote_cost prices the redo above 0 (extreme-high drafts)"`
	Wait            *bool        `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the new version (default true)"`
}

type confirmGeometryArgs struct {
	TaskID          string  `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Mesh            *string `json:"mesh,omitempty" jsonschema:"raw (Triangle, detailed) or quad; default raw when the plan allows it, else quad"`
	Faces           *int    `json:"faces,omitempty" jsonschema:"face count: raw simplifies to it (no faces: the full mesh); quad uses it instead of quality. Ranges: gen-2.5 high and extreme-high 20000-2000000, other gen-2.5 500-1000000, gen-1.5 500-200000"`
	Quality         *string `json:"quality,omitempty" jsonschema:"quad: extra-low 4000, low 8000, medium 18000 (default), high 50000 faces; smart low-poly: low, medium, high"`
	SmartLowPoly    *bool   `json:"smart_low_poly,omitempty" jsonschema:"confirm as a smart low-poly mesh (its own price; quality and mesh apply)"`
	BakeNormal      *bool   `json:"bake_normal,omitempty" jsonschema:"bake a normal map (raw: only with faces of 20000 or less)"`
	Symmetric       *bool   `json:"symmetric,omitempty" jsonschema:"symmetric mesh (default true)"`
	Hyper           *bool   `json:"hyper,omitempty" jsonschema:"use_hyper (default true, like the web)"`
	Smooth          *bool   `json:"smooth,omitempty" jsonschema:"smooth a quad mesh"`
	Version         *string `json:"version,omitempty" jsonschema:"the draft version to confirm (default the newest; get_task lists versions)"`
	ExpectedCredits float64 `json:"expected_credits" jsonschema:"the credits quote_cost returned for confirm_geometry on this task (with smart_low_poly when used); -1 accepts a price quote_cost reports as not known"`
	Wait            *bool   `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the confirmed geometry (default true)"`
}

type textureArgs struct {
	TaskID          string   `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	TextureMode     *string  `json:"texture_mode,omitempty" jsonschema:"texture tier of a gen-2.5 task (default high); gen-1.5 tasks use their legacy texture"`
	HDTexture       *bool    `json:"hd_texture,omitempty" jsonschema:"HD texture (superres); not at extreme-high"`
	Sharp           *bool    `json:"sharp,omitempty" jsonschema:"the web's 8K chip (sharp texture); not at extreme-high"`
	Delight         *bool    `json:"delight,omitempty" jsonschema:"remove baked-in lighting"`
	FaceEnhance     *bool    `json:"face_enhance,omitempty" jsonschema:"face restore; not at extreme-high"`
	Escore          *float64 `json:"escore,omitempty" jsonschema:"PBR temperature 0-10 in steps of 0.5 (default 7)"`
	ReferenceScale  *float64 `json:"reference_scale,omitempty" jsonschema:"reference strength 0-1, gen-1.5 only (default 1)"`
	ReferenceImage  *string  `json:"reference_image,omitempty" jsonschema:"the input image the texture follows: an input name from get_task, ALL (every input), or the absolute path of a new image; default ALL with several inputs, else the input"`
	Prompt          *string  `json:"prompt,omitempty" jsonschema:"texture prompt"`
	ExpectedCredits float64  `json:"expected_credits" jsonschema:"the credits quote_cost returned for generate_texture on this task with the same texture_mode; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the material draft (default true)"`
}

type redoMaterialArgs struct {
	TaskID          string   `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	TextureMode     *string  `json:"texture_mode,omitempty" jsonschema:"texture tier (default the draft's)"`
	HDTexture       *bool    `json:"hd_texture,omitempty" jsonschema:"HD texture (default the draft's)"`
	Sharp           *bool    `json:"sharp,omitempty" jsonschema:"sharp texture (default the draft's)"`
	Delight         *bool    `json:"delight,omitempty" jsonschema:"remove baked-in lighting (default the draft's)"`
	FaceEnhance     *bool    `json:"face_enhance,omitempty" jsonschema:"face restore (default the draft's)"`
	Escore          *float64 `json:"escore,omitempty" jsonschema:"PBR temperature 0-10 in steps of 0.5 (default the draft's)"`
	ReferenceScale  *float64 `json:"reference_scale,omitempty" jsonschema:"reference strength 0-1, gen-1.5 only"`
	Seed            *string  `json:"seed,omitempty" jsonschema:"keep the draft's seed or take a new one (default new)"`
	Prompt          *string  `json:"prompt,omitempty" jsonschema:"texture prompt (default the task's prompt)"`
	ExpectedCredits *float64 `json:"expected_credits,omitempty" jsonschema:"needed only when quote_cost prices the redo above 0 (extreme-high textures)"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the new version (default true)"`
}

type confirmMaterialArgs struct {
	TaskID          string  `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Version         *string `json:"version,omitempty" jsonschema:"the material version to confirm (default the newest; get_task lists versions)"`
	ExpectedCredits float64 `json:"expected_credits" jsonschema:"the credits quote_cost returned for confirm_material on this task; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool   `json:"wait,omitempty" jsonschema:"wait up to 45 seconds (default true)"`
}

type exportArgs struct {
	TaskID          string   `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Type            *string  `json:"type,omitempty" jsonschema:"mesh (confirmed geometry) or material (textured); default follows the stage"`
	Format          *string  `json:"format,omitempty" jsonschema:"file format (default obj)"`
	Resolution      *string  `json:"resolution,omitempty" jsonschema:"texture size of a material pack: 1k, 2k (default), 6k (Basic) or 4k, 12k (High, Business plan); 6k and 12k only for extreme-high textures"`
	Geometry        *string  `json:"geometry,omitempty" jsonschema:"base (default), lod, or high_poly (Business plan)"`
	Material        *string  `json:"material,omitempty" jsonschema:"material pack maps: all (default), pbr or shaded"`
	AlignGround     *bool    `json:"align_ground,omitempty" jsonschema:"stand the model on the ground plane (default true)"`
	Height          *float64 `json:"height,omitempty" jsonschema:"real-world height to scale the model to"`
	ExpectedCredits float64  `json:"expected_credits" jsonschema:"the credits quote_cost returned for export_model on this task with the same resolution and geometry; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the pack (default true)"`
}

type forkArgs struct {
	TaskID          string  `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Stage           string  `json:"stage" jsonschema:"geometry: a new task at the geometry draft stage; material: a new task at the material draft stage"`
	Public          *bool   `json:"public,omitempty" jsonschema:"true keeps the new task public on Hyper3D; default false: the new task is made private right after the fork"`
	ExpectedCredits float64 `json:"expected_credits" jsonschema:"the credits quote_cost returned for fork_task on this task with the same stage; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool   `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the new task (default true)"`
}

type selectArgs struct {
	TaskID    string `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Candidate string `json:"candidate" jsonschema:"the draft version to keep (v1, v2, ...; get_task lists versions)"`
}

type setPrivateArgs struct {
	TaskID  string `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Private *bool  `json:"private,omitempty" jsonschema:"true (default) makes the task private; false makes it public"`
}

// privacyTool: changes a task's privacy on Hyper3D, free; the same call
// twice leaves the same state.
var privacyTool = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)}

// nonNullDeep drops the null alternatives of s and of its nested objects.
func nonNullDeep(s *jsonschema.Schema) *jsonschema.Schema {
	for _, p := range s.Properties {
		dropNullAlt(p)
		if p.Type == "object" {
			nonNullDeep(p)
		}
		if p.Type == "array" && p.Items != nil && p.Items.Type == "object" {
			nonNullDeep(p.Items)
		}
	}
	return s
}

func viewTags() []string {
	out := make([]string, len(catalog.ViewTags))
	for i, t := range catalog.ViewTags {
		out[i] = t.Tag
	}
	return out
}

func textureMode(s *jsonschema.Schema) {
	withEnum(s, "texture_mode", "extreme-low", "low", "medium", "high", "extreme-high")
	withRange(s, 0, 10, "escore")
	withRange(s, 0, 1, "reference_scale")
}

func (s *Server) registerFlowTools() {
	gen := nonNullDeep(inputSchema[generateArgs](map[string]string{"model": `"gen-2.5"`, "wait": "true", "public": "false"}))
	images := property(gen, "images")
	images.MinItems, images.MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(service.MaxImages)
	withEnum(images.Items, "view", viewTags()...)
	withRange(images.Items, 0, 1, "weight")
	withEnum(gen, "model", "gen-2.5", "gen-1.5")
	withEnum(gen, "tier", "extreme-low", "low", "medium", "high", "extreme-high")
	withEnum(gen, "mode", "default", "focal", "zero", "speedy")
	withEnum(gen, "image_mode", "recon", "rest")
	presets := property(gen, "presets")
	withEnum(presets, "micro", "micro", "clean")
	withEnum(presets, "instruct", "creative", "faithful")
	withEnum(presets, "symmetric", "symmetric", "asymmetric")
	withRange(presets, 1, 3, "symmetric_strength", "detailed")
	adv := property(gen, "advanced")
	withRange(adv, 0, 65535, "seed")
	withRange(adv, 1, 15, "cfg")
	withRange(adv, 10, 100, "steps")
	withRange(adv, -5, 5, "detail")
	bbox := property(gen, "bbox")
	bbox.MinItems, bbox.MaxItems = jsonschema.Ptr(3), jsonschema.Ptr(3)
	withRange(gen, 1, 10, "batch")
	addTool(s, "generate_model", "Generate a 3D draft",
		"Start a Rodin image-to-3D task from 1-5 local images: each image is preprocessed by Hyper3D (free; it writes the caption), "+
			"then the geometry draft is submitted. Tag each image's view (F front, B back, L, R, FL, FR, BL, BR, U up, D down, ? unknown) "+
			"for multi-view input. Needs [permission] generate = \"allow\" in config.toml and expected_credits from quote_cost "+
			"(operation generate_model, same model, tier and batch). copy_settings_from reuses a task's recipe (get_generation_options lists "+
			"the account's recipes). The new task is made private unless public is true. Returns the new task (geometry_draft once ready, "+
			"its poster attached), the quote and the wallet before and after. Next: get_preview, redo_geometry or confirm_geometry. Never "+
			"resend after a time-out: list_tasks shows whether it started.",
		paidTool, gen, s.generateModel)

	redo := nonNullDeep(inputSchema[redoGeometryArgs](map[string]string{"wait": "true"}))
	radv := property(redo, "advanced")
	withRange(radv, 0, 65535, "seed")
	withRange(radv, 1, 15, "cfg")
	withRange(radv, 10, 100, "steps")
	withRange(radv, -5, 5, "detail")
	addTool(s, "redo_geometry", "Redo the geometry draft",
		"Generate a new version of a geometry draft (stage geometry_draft) with the draft's settings, optionally a new prompt and the "+
			"advanced options. Free except for extreme-high drafts (quote_cost operation redo_geometry); the plan limits the number of redos "+
			"(get_task redo_left). Needs [permission] redo = \"allow\". Returns the task with the new version and redos left; its poster is attached.",
		paidTool, redo, s.redoGeometry)

	conf := nonNullDeep(inputSchema[confirmGeometryArgs](map[string]string{"wait": "true", "symmetric": "true", "hyper": "true"}))
	withEnum(conf, "mesh", "raw", "quad")
	withEnum(conf, "quality", "extra-low", "low", "medium", "high")
	withRange(conf, 500, 2000000, "faces")
	addTool(s, "confirm_geometry", "Confirm the geometry",
		"Confirm a geometry draft version (default the newest) as the model's mesh: raw (Triangle) simplified to faces or full, quad with a "+
			"quality or face count, or smart low-poly. This is where the draft is paid for. Needs [permission] confirm_geometry = \"allow\" and "+
			"expected_credits from quote_cost (operation confirm_geometry, task_id, smart_low_poly when used). Returns the task at stage "+
			"geometry; next generate_texture or export_model.",
		paidTool, conf, s.confirmGeometry)

	tex := nonNullDeep(inputSchema[textureArgs](map[string]string{"wait": "true", "escore": "7"}))
	textureMode(tex)
	addTool(s, "generate_texture", "Texture the model",
		"Generate the material draft of a confirmed geometry (stage geometry): texture tier, HD, sharp (the web's 8K chip), de-light, face "+
			"restore, PBR temperature and the reference image. Needs [permission] generate_texture = \"allow\" and expected_credits from "+
			"quote_cost (operation generate_texture, task_id, texture_mode). Returns the task at stage material_draft; next redo_material or "+
			"confirm_material.",
		paidTool, tex, s.generateTexture)

	rmat := nonNullDeep(inputSchema[redoMaterialArgs](map[string]string{"wait": "true", "seed": `"new"`}))
	textureMode(rmat)
	withEnum(rmat, "seed", "keep", "new")
	addTool(s, "redo_material", "Redo the material draft",
		"Generate a new version of a material draft (stage material_draft) with its settings, changing only what is given. Free except at "+
			"texture_mode extreme-high (quote_cost operation redo_material); the plan limits the number of redos (get_task redo_left). Needs "+
			"[permission] redo = \"allow\".",
		paidTool, rmat, s.redoMaterial)

	addTool(s, "confirm_material", "Confirm the material",
		"Confirm a material draft version (default the newest). Needs [permission] confirm_material = \"allow\" and expected_credits from "+
			"quote_cost (operation confirm_material, task_id). Returns the task at stage material; next export_model.",
		paidTool, nonNullDeep(inputSchema[confirmMaterialArgs](map[string]string{"wait": "true"})), s.confirmMaterial)

	exp := nonNullDeep(inputSchema[exportArgs](map[string]string{"wait": "true", "format": `"obj"`, "geometry": `"base"`, "material": `"all"`, "align_ground": "true"}))
	withEnum(exp, "type", "mesh", "material")
	withEnum(exp, "format", "obj", "fbx", "glb", "usdz", "stl")
	withEnum(exp, "resolution", "1k", "2k", "4k", "6k", "12k")
	withEnum(exp, "geometry", "base", "lod", "high_poly")
	withEnum(exp, "material", "all", "pbr", "shaded")
	addTool(s, "export_model", "Pack the model files",
		"Pack a task's files for download: the confirmed mesh (stage geometry) or the textured model with its maps (stage material), in "+
			"one format, texture size, geometry level and material set. Needs [permission] export = \"allow\" and expected_credits from "+
			"quote_cost (operation export_model, task_id, resolution, geometry). Returns the task (packed once done) and the packed file "+
			"names; next download_model.",
		paidTool, exp, s.exportModel)

	fork := nonNullDeep(inputSchema[forkArgs](map[string]string{"wait": "true", "public": "false"}))
	withEnum(fork, "stage", "geometry", "material")
	addTool(s, "fork_task", "Fork a task",
		"Branch a new task from a task's geometry (back to a geometry draft) or material (back to a material draft); the original stays as it "+
			"is. The web offers it for tasks younger than 30 days. Needs [permission] fork = \"allow\" and expected_credits from quote_cost "+
			"(operation fork_task, task_id, settings.stage). The new task is made private unless public is true. Returns the new task.",
		paidTool, fork, s.forkTask)

	addTool(s, "select_candidate", "Pick a batch draft",
		"Keep one draft of a batch submit (stage geometry_draft): candidate is its version. Free. Needs [permission] redo = \"allow\".",
		paidTool, nonNullDeep(inputSchema[selectArgs](nil)), s.selectCandidate)

	addTool(s, "set_private", "Make a task private or public",
		"Make an existing task private (default) or, with private false, public on Hyper3D. Free and needs no [permission] key; a task "+
			"already in that state is left as it is. Make a task public only when the user asks for it. Returns the task with its private flag.",
		privacyTool, nonNull(inputSchema[setPrivateArgs](map[string]string{"private": "true"})), s.setPrivate)
}

// --- handlers ---

func expectedPtr(v float64) *float64 { return &v }

func (s *Server) generateModel(ctx context.Context, _ *mcp.CallToolRequest, in generateArgs) (*mcp.CallToolResult, any, error) {
	g := service.GenerateInput{
		Prompt: optString(in.Prompt), Model: optString(in.Model), Tier: optString(in.Tier), Mode: optString(in.Mode),
		ImageMode: optString(in.ImageMode), FaceEnhance: in.FaceEnhance, BBox: in.BBox, Batch: optInt(in.Batch, 0),
		CopySettingsFrom: optString(in.CopySettingsFrom), Public: optBool(in.Public, false), Expected: expectedPtr(in.ExpectedCredits),
		Wait: optBool(in.Wait, true),
	}
	for _, img := range in.Images {
		g.Images = append(g.Images, service.GenerateImage{Path: img.Path, View: optString(img.View), Weight: img.Weight})
	}
	if p := in.Presets; p != nil {
		g.Presets = &service.Presets{Micro: optString(p.Micro), Instruct: optString(p.Instruct), Symmetric: optString(p.Symmetric),
			SymmetricStrength: optInt(p.SymmetricStrength, 0), Detailed: optInt(p.Detailed, 0), Soft: optBool(p.Soft, false)}
	}
	g.Advanced = advanced(in.Advanced)
	return reply(s.svc.Generate(ctx, g)), nil, nil
}

func advanced(a *advancedArg) *service.Advanced {
	if a == nil {
		return nil
	}
	return &service.Advanced{Seed: a.Seed, NegativePrompt: optString(a.NegativePrompt), Cfg: a.Cfg, Steps: a.Steps, Detail: a.Detail}
}

func (s *Server) redoGeometry(ctx context.Context, _ *mcp.CallToolRequest, in redoGeometryArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.RedoGeometry(ctx, service.RedoGeometryInput{
		TaskID: in.TaskID, Prompt: optString(in.Prompt), Advanced: advanced(in.Advanced), Expected: in.ExpectedCredits, Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) confirmGeometry(ctx context.Context, _ *mcp.CallToolRequest, in confirmGeometryArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.ConfirmGeometry(ctx, service.ConfirmGeometryInput{
		TaskID: in.TaskID, Mesh: optString(in.Mesh), Quality: optString(in.Quality), Faces: in.Faces, SmartLowPoly: optBool(in.SmartLowPoly, false),
		BakeNormal: in.BakeNormal, Symmetric: in.Symmetric, Hyper: in.Hyper, Smooth: in.Smooth, Version: optString(in.Version),
		Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) generateTexture(ctx context.Context, _ *mcp.CallToolRequest, in textureArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.GenerateTexture(ctx, service.TextureInput{
		TaskID: in.TaskID, TextureMode: optString(in.TextureMode), HD: in.HDTexture, Sharp: in.Sharp, Delight: in.Delight, FaceEnhance: in.FaceEnhance,
		Escore: in.Escore, ReferenceScale: in.ReferenceScale, ReferenceImage: optString(in.ReferenceImage), Prompt: optString(in.Prompt),
		Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) redoMaterial(ctx context.Context, _ *mcp.CallToolRequest, in redoMaterialArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.RedoMaterial(ctx, service.TextureInput{
		TaskID: in.TaskID, TextureMode: optString(in.TextureMode), HD: in.HDTexture, Sharp: in.Sharp, Delight: in.Delight, FaceEnhance: in.FaceEnhance,
		Escore: in.Escore, ReferenceScale: in.ReferenceScale, Seed: optString(in.Seed), Prompt: optString(in.Prompt),
		Expected: in.ExpectedCredits, Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) confirmMaterial(ctx context.Context, _ *mcp.CallToolRequest, in confirmMaterialArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.ConfirmMaterial(ctx, service.ConfirmMaterialInput{
		TaskID: in.TaskID, Version: optString(in.Version), Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) exportModel(ctx context.Context, _ *mcp.CallToolRequest, in exportArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Export(ctx, service.ExportInput{
		TaskID: in.TaskID, Type: optString(in.Type), Format: optString(in.Format), Resolution: optString(in.Resolution), Geometry: optString(in.Geometry),
		Material: optString(in.Material), AlignGround: in.AlignGround, Height: in.Height, Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true),
	})), nil, nil
}

func (s *Server) forkTask(ctx context.Context, _ *mcp.CallToolRequest, in forkArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Fork(ctx, service.ForkInput{TaskID: in.TaskID, Stage: in.Stage, Public: optBool(in.Public, false),
		Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true)})), nil, nil
}

func (s *Server) setPrivate(ctx context.Context, _ *mcp.CallToolRequest, in setPrivateArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.SetPrivate(ctx, service.SetPrivateInput{TaskID: in.TaskID, Private: optBool(in.Private, true)})), nil, nil
}

func (s *Server) selectCandidate(ctx context.Context, _ *mcp.CallToolRequest, in selectArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.SelectCandidate(ctx, service.SelectInput{TaskID: in.TaskID, Candidate: in.Candidate})), nil, nil
}
