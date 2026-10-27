package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

type agenticArgs struct {
	Images          []string `json:"images" jsonschema:"1-5 absolute paths of jpg, png or webp images of one object (larger than 1024 px is scaled down like the web does)"`
	Prompt          *string  `json:"prompt,omitempty" jsonschema:"what to build (at most 1024 characters); default the caption Hyper3D writes for the first image"`
	Budget          string   `json:"budget" jsonschema:"time budget: basic (about 3 minutes), standard (about 5, the web's default) or pro (about 10); quote_cost gives each price"`
	NGons           *bool    `json:"ngons,omitempty" jsonschema:"the web's N-gons switch (default true): the server picks the producer; false asks for the high-poly producer, whose result is an ordinary Rodin task"`
	Public          *bool    `json:"public,omitempty" jsonschema:"true keeps the new task public on Hyper3D; default false: the task is made private right after the submit"`
	ExpectedCredits float64  `json:"expected_credits" jsonschema:"the credits quote_cost returned for generate_agentic with the same budget"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds (default true); the run takes minutes, so follow it with wait_task"`
}

type splitArgs struct {
	TaskID          string  `json:"task_id" jsonschema:"the task id of a geometry draft (an id prefix of 8 or more characters works too)"`
	Version         *string `json:"version,omitempty" jsonschema:"the draft version to split (default the newest; get_task lists versions)"`
	Strength        *int    `json:"strength,omitempty" jsonschema:"how many parts the plan aims for, 1-12 (the web's Strength; left out: 5 for a plan, the plan's own for step generate; above 5 needs the RodinBangGenerateAdvancedQuality privilege)"`
	Step            *string `json:"step,omitempty" jsonschema:"both (default): lay out the parts, then split; plan: only lay out the parts and return the plan picture; generate: split by the plan made before"`
	BangVersion     *string `json:"bang_version,omitempty" jsonschema:"split one part of this earlier Bang result again (with part)"`
	Part            *string `json:"part,omitempty" jsonschema:"the part name of bang_version to split again (get_task and the earlier reply list part names)"`
	ExpectedCredits float64 `json:"expected_credits" jsonschema:"the credits quote_cost returned for split_parts on this task with the same step, strength and part; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool   `json:"wait,omitempty" jsonschema:"wait up to 45 seconds in all (default true); false returns right after the request of step plan or generate (get_task follows the plan as bang_plan, the split as bang); step both needs its finished plan, so it waits as with true"`
}

type rigArgs struct {
	TaskID          string  `json:"task_id" jsonschema:"the task id of a task with confirmed geometry (an id prefix of 8 or more characters works too)"`
	Template        *string `json:"template,omitempty" jsonschema:"built-in skeleton: human (the Mixamo skeleton; the default when no skeleton, description or fbx_skeleton_path is given); quadruped, bird and kaiju share a four-legged skeleton with a tail, dragon adds wings"`
	Skeleton        *string `json:"skeleton,omitempty" jsonschema:"instead of template: your own bone hierarchy, one bone name per line, depth by leading - characters (Hips, -Spine, --Chest)"`
	Description     *string `json:"description,omitempty" jsonschema:"instead of template: the skeleton described in words; Hyper3D plans the bones"`
	FBXSkeleton     *string `json:"fbx_skeleton_path,omitempty" jsonschema:"instead of template: absolute path of an FBX file whose skeleton the rig takes, as the web's Import FBX: its bones (pose bones left out), skinned with shell, then turned to the FBX bones' frames by a second (reskin) call"`
	Reskin          *bool   `json:"reskin,omitempty" jsonschema:"with fbx_skeleton_path: send only the reskin call, for the task's rig made from the same FBX bones (when an earlier call's rig outlasted its wait or its reskin was not sent); refused before any paid request for another rig"`
	Skinning        *string `json:"skinning,omitempty" jsonschema:"core (human template only; its default) or shell (the default otherwise)"`
	ExpectedCredits float64 `json:"expected_credits" jsonschema:"the credits quote_cost returned for rig_model on this task with the same template (custom for skeleton, description and fbx_skeleton_path) and skinning; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool   `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the rig (default true); with fbx_skeleton_path it waits as with true, since the reskin call needs the finished rig"`
}

type animateArgs struct {
	TaskID          string   `json:"task_id" jsonschema:"the task id of a rigged task (an id prefix of 8 or more characters works too)"`
	Presets         []string `json:"presets,omitempty" jsonschema:"animation presets as <category>/<name> from list_animation_presets, e.g. human/Walking"`
	AnimationFiles  []string `json:"animation_files,omitempty" jsonschema:"absolute paths of your own FBX animations (one clip each, made for this skeleton, at most 25 MiB)"`
	Format          *string  `json:"format,omitempty" jsonschema:"file format of the export (default glb)"`
	Resolution      *string  `json:"resolution,omitempty" jsonschema:"texture size: 1k, 2k (default), 6k (Basic) or 4k, 12k (High, Business plan)"`
	Material        *string  `json:"material,omitempty" jsonschema:"maps of a textured task: all (default), pbr or shaded; an untextured task exports the mesh only"`
	Height          *float64 `json:"height,omitempty" jsonschema:"real-world height to scale the model to"`
	AlignGround     *bool    `json:"align_ground,omitempty" jsonschema:"stand the model on the ground plane (default true)"`
	ExpectedCredits float64  `json:"expected_credits" jsonschema:"the credits quote_cost returned for animate_model on this task; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the export (default true)"`
}

type presetsArgs struct {
	Category *string `json:"category,omitempty" jsonschema:"only this category"`
	Template *string `json:"template,omitempty" jsonschema:"only the category the web shows for this rig template"`
}

type textureModelArgs struct {
	ModelPath       string   `json:"model_path" jsonschema:"absolute path of the model file (glb, gltf, fbx, obj, dae, stl or ply; at most 150 MB)"`
	Images          []string `json:"images,omitempty" jsonschema:"1-5 absolute paths of pictures the texture follows, the first showing the model's front; default a render of the model's front (glb, gltf, obj and stl only)"`
	Prompt          *string  `json:"prompt,omitempty" jsonschema:"caption (at most 1024 characters); default the caption Hyper3D writes for the first image"`
	Model           *string  `json:"model,omitempty" jsonschema:"gen-2.5 (default) or gen-1.5"`
	Mode            *string  `json:"mode,omitempty" jsonschema:"gen-1.5 mode (default default)"`
	Public          *bool    `json:"public,omitempty" jsonschema:"true keeps the new task public on Hyper3D; default false: the task is made private right after the submit"`
	ExpectedCredits float64  `json:"expected_credits" jsonschema:"the credits quote_cost returned for texture_model with the same model and number of images; -1 accepts a price quote_cost reports as not known"`
	Wait            *bool    `json:"wait,omitempty" jsonschema:"wait up to 45 seconds for the draft (default true)"`
}

func (s *Server) registerP3Tools() {
	ag := nonNull(inputSchema[agenticArgs](map[string]string{"ngons": "true", "public": "false", "wait": "true"}))
	ai := property(ag, "images")
	ai.MinItems, ai.MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(service.MaxImages)
	withEnum(ag, "budget", "basic", "standard", "pro")
	addTool(s, "generate_agentic", "Agentic 3D build",
		"Start a Rodin Agentic task from 1-5 images of one object: Hyper3D plans and builds the model within a time budget (basic about 3 "+
			"minutes, standard 5, pro 10). The server picks the producer: high-poly makes an ordinary Rodin task that ends as a geometry "+
			"draft (confirm_geometry and the other stage tools continue it); n-gons makes a RodinAgentic task, which no paid or stage tool here acts on "+
			"(get_task, wait_task and get_preview read it; set_private changes its privacy). Needs [permission] agentic = \"allow\" and expected_credits from "+
			"quote_cost (operation generate_agentic, settings.budget). The new task is made private unless public is true. Returns the task "+
			"and the producer; follow it with wait_task. Never resend after a time-out: list_tasks shows whether it started.",
		paidTool, ag, s.generateAgentic)

	// No filled strength: step generate takes the plan's (SplitParts).
	sp := nonNull(inputSchema[splitArgs](map[string]string{"step": `"both"`, "wait": "true"}))
	withRange(sp, 1, 12, "strength")
	withEnum(sp, "step", "both", "plan", "generate")
	addTool(s, "split_parts", "Split a draft into parts",
		"Split a geometry draft (stage geometry_draft) into separate parts, the web's Bang: a plan lays out the parts (strength = how many), "+
			"then the split makes a Bang result of the draft version with one mesh per part. step plan stops after the plan and, when the "+
			"plan is made within the wait, attaches its picture so the user can approve it; get_task shows the newest plan split_parts sent "+
			"(bang_plan) with its picture, free, until a split of its version follows. step generate splits by the plan made before: it is "+
			"refused before any paid request while the newest plan split_parts sent on the version runs, when it failed or was canceled, or when it is of another part (bang_generate "+
			"names no part); it takes the plan's strength. A split of the same version still running is not sent again. When the split "+
			"finishes within the wait, its parts file is saved in the task's files folder and a render attached; otherwise get_task lists the "+
			"result and download_model saves it. Confirm the parts with confirm_geometry bang_version. Needs [permission] split_parts = \"allow\" and "+
			"expected_credits from quote_cost (operation split_parts, task_id, settings.step, strength and part).",
		paidTool, sp, s.splitParts)

	rg := nonNull(inputSchema[rigArgs](map[string]string{"wait": "true"}))
	tmpl := make([]string, len(catalog.RigTemplates))
	for i, t := range catalog.RigTemplates {
		tmpl[i] = t.Name
	}
	withEnum(rg, "template", tmpl...)
	withEnum(rg, "skinning", "shell", "core")
	addTool(s, "rig_model", "Rig the model",
		"Give a task with confirmed geometry (stages geometry, material_draft, material) a skeleton and skin: a built-in template, your own "+
			"bone hierarchy, a skeleton described in words, or the skeleton of an FBX file (two calls, as the web's Import FBX: the rig from "+
			"its bones, then the reskin to their frames; reskin true sends only the second). The rig is kept by Hyper3D with the task: get_task "+
			"shows it as rig, with the rig and reskin calls this tool sent while they run; a call while one of them still runs is refused before "+
			"any paid request. animate_model exports the rigged model. Needs [permission] rig = \"allow\" and expected_credits from quote_cost (operation "+
			"rig_model, task_id, settings.template and skinning).",
		paidTool, rg, s.rigModel)

	an := nonNull(inputSchema[animateArgs](map[string]string{"format": `"glb"`, "resolution": `"2k"`, "align_ground": "true", "wait": "true"}))
	an.Properties["presets"].MaxItems = jsonschema.Ptr(10)
	an.Properties["animation_files"].MaxItems = jsonschema.Ptr(10)
	withEnum(an, "format", "fbx", "glb", "usdz")
	withEnum(an, "resolution", "1k", "2k", "4k", "6k", "12k")
	withEnum(an, "material", "all", "pbr", "shaded")
	addTool(s, "animate_model", "Export the rigged model with animations",
		"Export a rigged task (rig_model) with up to 10 animations: presets from list_animation_presets and your own FBX animations. "+
			"Needs [permission] animate = \"allow\" and expected_credits from quote_cost (operation animate_model, task_id, settings.format, "+
			"resolution, material, animations and animation_files). When the export finishes within the wait, returns the exported file "+
			"names; otherwise get_task lists the files once it is done. download_model saves them.",
		paidTool, an, s.animateModel)

	pa := nonNull(inputSchema[presetsArgs](nil))
	cats := make([]string, len(catalog.AnimationPresets))
	for i, c := range catalog.AnimationPresets {
		cats[i] = c.Dir
	}
	withEnum(pa, "category", cats...)
	withEnum(pa, "template", tmpl...)
	addTool(s, "list_animation_presets", "Animation presets",
		"List the web's animation presets for animate_model: 161 presets in 9 categories, passed as <category>/<name>. template gives the "+
			"category the web shows first for a rig template. Free, no permission.",
		readTool, pa, s.listAnimationPresets)

	tm := nonNull(inputSchema[textureModelArgs](map[string]string{"model": `"gen-2.5"`, "public": "false", "wait": "true"}))
	ti := property(tm, "images")
	ti.MinItems, ti.MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(service.MaxImages)
	withEnum(tm, "model", "gen-2.5", "gen-1.5")
	withEnum(tm, "mode", "default", "focal", "zero", "speedy")
	addTool(s, "texture_model", "Texture your own model",
		"Start a Rodin 3D Editing task from your own model file and pictures of the object (the web's 3D Editing): the task runs the "+
			"ordinary stages, so confirm_geometry, generate_texture and export_model follow. Needs [permission] texture_model = \"allow\" and "+
			"expected_credits from quote_cost (operation texture_model, settings.model and images). The new task is made private unless public is true. "+
			"Never resend after a time-out: list_tasks shows whether it started.",
		paidTool, tm, s.textureModel)
}

func (s *Server) generateAgentic(ctx context.Context, _ *mcp.CallToolRequest, in agenticArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.GenerateAgentic(ctx, service.AgenticInput{Images: in.Images, Prompt: optString(in.Prompt), Budget: in.Budget,
		NGons: optBool(in.NGons, true), Public: optBool(in.Public, false), Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true)})), nil, nil
}

func (s *Server) splitParts(ctx context.Context, _ *mcp.CallToolRequest, in splitArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.SplitParts(ctx, service.SplitInput{TaskID: in.TaskID, Version: optString(in.Version), Strength: optInt(in.Strength, 0),
		Step: optString(in.Step), BangVersion: optString(in.BangVersion), Part: optString(in.Part), Expected: expectedPtr(in.ExpectedCredits),
		Wait: optBool(in.Wait, true)})), nil, nil
}

func (s *Server) rigModel(ctx context.Context, _ *mcp.CallToolRequest, in rigArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.RigModel(ctx, service.RigInput{TaskID: in.TaskID, Template: optString(in.Template), Skeleton: optString(in.Skeleton),
		Description: optString(in.Description), Skinning: optString(in.Skinning), FBXSkeleton: optString(in.FBXSkeleton), Reskin: optBool(in.Reskin, false), Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true)})), nil, nil
}

func (s *Server) animateModel(ctx context.Context, _ *mcp.CallToolRequest, in animateArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.AnimateModel(ctx, service.AnimateInput{TaskID: in.TaskID, Presets: in.Presets, Files: in.AnimationFiles,
		Format: optString(in.Format), Resolution: optString(in.Resolution), Material: optString(in.Material), Height: in.Height,
		AlignGround: in.AlignGround, Expected: expectedPtr(in.ExpectedCredits), Wait: optBool(in.Wait, true)})), nil, nil
}

func (s *Server) listAnimationPresets(_ context.Context, _ *mcp.CallToolRequest, in presetsArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.AnimationPresets(service.AnimationPresetsInput{Category: optString(in.Category), Template: optString(in.Template)})), nil, nil
}

func (s *Server) textureModel(ctx context.Context, _ *mcp.CallToolRequest, in textureModelArgs) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.TextureModel(ctx, service.TextureModelInput{ModelPath: in.ModelPath, Images: in.Images, Prompt: optString(in.Prompt),
		Model: optString(in.Model), Mode: optString(in.Mode), Public: optBool(in.Public, false), Expected: expectedPtr(in.ExpectedCredits),
		Wait: optBool(in.Wait, true)})), nil, nil
}
