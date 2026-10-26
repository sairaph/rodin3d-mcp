// Package catalog is the static description of what a Rodin generation can
// be told: models, tiers, Gen-1.5 modes, every setting with its range and
// default, view tags, default weights, and the privileges that gate them.
// Every value comes from the web app build read in notes 14 (sections 1-4)
// and the plan (2.6.3); the web field each setting is sent as is named.
package catalog

// Model is a generation model.
type Model struct {
	Name  string `yaml:"name"`
	Label string `yaml:"label"`
	Web   string `yaml:"web"`
	Tiers []Tier `yaml:"tiers,omitempty"`
	Modes []Mode `yaml:"modes,omitempty"`
	Notes string `yaml:"notes,omitempty"`
}

// Tier is a Gen-2.5 tier.
type Tier struct {
	Name     string `yaml:"name"`
	Web      string `yaml:"web"`
	Thinking string `yaml:"thinking,omitempty"`
	Submit   string `yaml:"submit"`
	Gate     string `yaml:"gate,omitempty"`
}

// Mode is a Gen-1.5 mode.
type Mode struct {
	Name string `yaml:"name"`
	UI   string `yaml:"ui"`
	Web  string `yaml:"web"`
}

// Setting is one input of a stage.
type Setting struct {
	Name    string `yaml:"name"`
	Stage   string `yaml:"stage"`
	Web     string `yaml:"web,omitempty"`
	Type    string `yaml:"type"`
	Values  string `yaml:"values,omitempty"`
	Default string `yaml:"default,omitempty"`
	Models  string `yaml:"models,omitempty"`
	Gate    string `yaml:"gate,omitempty"`
	Notes   string `yaml:"notes,omitempty"`
}

// ViewTag is one image direction tag.
type ViewTag struct {
	Tag   string `yaml:"tag"`
	Label string `yaml:"label"`
}

// Weights are the default view weights for a number of images.
type Weights struct {
	Images int       `yaml:"images"`
	Gen25  []float64 `yaml:"gen_2_5"`
	Gen15  []float64 `yaml:"gen_1_5"`
}

// Privilege is a plan privilege and what it unlocks.
type Privilege struct {
	Name    string `yaml:"name"`
	Unlocks string `yaml:"unlocks"`
}

// Models lists the image-to-3D models.
var Models = []Model{
	{
		Name: "gen-2.5", Label: "Gen-2.5 Agent-Ready", Web: "generation_mode Brilliant + brilliant_mode gen-2.5-<tier>",
		Tiers: []Tier{
			{Name: "extreme-low", Web: "gen-2.5-extreme-low", Thinking: "about 4 s", Submit: "Free Preview"},
			{Name: "low", Web: "gen-2.5-low", Thinking: "about 9 s", Submit: "Free Preview"},
			{Name: "medium", Web: "gen-2.5-medium", Thinking: "about 20 s", Submit: "Free Preview"},
			{Name: "high", Web: "gen-2.5-high", Thinking: "about 40 s", Submit: "0.5 credits"},
			{Name: "extreme-high", Web: "gen-2.5-extreme-high", Thinking: "about 80 s", Submit: "1 credit", Gate: "RodinUseAdditionalImage"},
		},
		Notes: "Multi-image submits use condition_mode concat and equal weights.",
	},
	{
		Name: "gen-1.5", Label: "Gen-1.5 V1.2", Web: "generation_mode Default | Detail | Smooth | Fast",
		Modes: []Mode{
			{Name: "default", UI: "Default", Web: "Default"},
			{Name: "focal", UI: "Focal", Web: "Detail (+ sharp_bias 2.5)"},
			{Name: "zero", UI: "Zero", Web: "Smooth"},
			{Name: "speedy", UI: "Speedy", Web: "Fast (+ fast_texture; empty prompt; no face enhance)"},
		},
		Notes: "Every mode submits as Free Preview. Two or more images use condition_mode fuse and the Gen-1.5 weight table.",
	},
}

// Settings lists every input per stage.
var Settings = []Setting{
	// generate_model
	{Name: "images", Stage: "generate_model", Web: "images[], image_label, view_weights", Type: "list of {path, view, weight}", Values: "1-5 images; jpg, jpeg, png, webp; at most 20 MB each and 20 MB together", Gate: "2 or more images: RodinUseMultiImage", Notes: "Each image is preprocessed first (free); the browser scales the longest side to at most 1024 px."},
	{Name: "images[].view", Stage: "generate_model", Web: "image_label", Type: "enum", Values: "F FL FR B BL BR L R U D ?", Default: "?", Notes: "One tag per image, in image order."},
	{Name: "images[].weight", Stage: "generate_model", Web: "view_weights", Type: "number", Values: "0-1, normalised to sum 1", Default: "equal (see default_weights)"},
	{Name: "prompt", Stage: "generate_model", Web: "prompt", Type: "text", Values: "at most 1024 characters", Default: "the caption the preprocess step writes for the first image, else \"A high-quality 3D model. sharp edges, complex geometry, game-ready\""},
	{Name: "model", Stage: "generate_model", Web: "generation_mode", Type: "enum", Values: "gen-2.5, gen-1.5", Default: "gen-2.5"},
	{Name: "tier", Stage: "generate_model", Web: "brilliant_mode", Type: "enum", Values: "extreme-low, low, medium, high, extreme-high", Models: "gen-2.5", Notes: "Required for gen-2.5."},
	{Name: "mode", Stage: "generate_model", Web: "generation_mode", Type: "enum", Values: "default, focal, zero, speedy", Default: "default", Models: "gen-1.5"},
	{Name: "image_mode", Stage: "generate_model", Web: "image_mode (+ rest_bias 0-5)", Type: "enum", Values: "recon, rest", Default: "recon"},
	{Name: "face_enhance", Stage: "generate_model", Web: "allow_face_enhance, default_face_enhance", Type: "bool", Default: "the preprocess step's default_face_enhance", Notes: "Allowed only when the preprocess step reports allow_face_enhance; off for Speedy low tiers and Gen-1.5 Fast."},
	{Name: "presets.micro", Stage: "generate_model", Web: "micro_bias", Type: "enum", Values: "micro (5), clean (1)", Models: "gen-2.5 extreme-high"},
	{Name: "presets.creative", Stage: "generate_model", Web: "geometry_instruct_mode", Type: "enum", Values: "creative (corrects proportions), faithful (keeps the input perspective)", Models: "gen-2.5 medium and above"},
	{Name: "presets.symmetric", Stage: "generate_model", Web: "symmetric_bias + prompt tag", Type: "enum + strength", Values: "symmetric or asymmetric; strength 1-3 -> symmetric_bias 0, 2, 3", Notes: "Prompt tag \"symmetric geometry\" or \"asymmetric geometry\"."},
	{Name: "presets.detailed", Stage: "generate_model", Web: "detail_level", Type: "integer", Values: "1-3", Notes: "Improves surface relief and geometric bump."},
	{Name: "presets.soft", Stage: "generate_model", Web: "prompt tag \"smooth edges\"", Type: "bool"},
	{Name: "advanced.seed", Stage: "generate_model", Web: "seed", Type: "integer", Values: "0-65535", Default: "random", Notes: "The web sends the advanced options only with a redo (open question O2)."},
	{Name: "advanced.negative_prompt", Stage: "generate_model", Web: "negative_prompt", Type: "text"},
	{Name: "advanced.cfg", Stage: "generate_model", Web: "guidance_scale", Type: "integer", Values: "1-15", Default: "10"},
	{Name: "advanced.steps", Stage: "generate_model", Web: "inference_steps", Type: "integer", Values: "10-100", Default: "50"},
	{Name: "advanced.detail", Stage: "generate_model", Web: "sharp_bias", Type: "integer", Values: "-5 to 5"},
	{Name: "controls.bbox", Stage: "generate_model", Web: "bbox_condition", Type: "[width, height, length]", Values: "each 1-300", Default: "100, 100, 100"},
	{Name: "controls.voxel", Stage: "generate_model", Web: "voxel_condition, voxel_condition_cfg, voxel_condition_weight", Type: "model file", Values: "grid 16, 32, 64 or 128 (Gen-1.5: 16); strict or rough; weight 0-1", Default: "grid 64, strict, weight 1", Gate: "Creator plan or higher", Notes: "Source model: obj fbx glb gltf dae stl ply, voxelized locally."},
	{Name: "controls.point_cloud", Stage: "generate_model", Web: "pcd_condition, pcd_condition_uncertainty, spatial_condition", Type: "model file", Values: "512, 1024 or 2048 points", Default: "2048 points, uncertainty 0.01", Gate: "Creator plan or higher"},
	{Name: "batch", Stage: "generate_model", Web: "batch_gen", Type: "integer", Values: "high: 1, 3, 5; extreme-high: 1, 3; other tiers: 1, 5, 10", Default: "1", Gate: "more than 1: Business plan"},
	{Name: "private", Stage: "generate_model", Web: "is_private", Type: "bool", Default: "true"},
	// confirm_geometry
	{Name: "mesh", Stage: "confirm_geometry", Web: "mesh_export_type", Type: "enum", Values: "raw (Triangle), quad", Gate: "raw: RodinMeshAdditionalFeatures"},
	{Name: "quality", Stage: "confirm_geometry", Web: "quality_override (quality stays medium, as the web sends it)", Type: "enum", Values: "extra-low 4000, low 8000, medium 18000, high 50000 faces", Default: "medium", Notes: "Quad mesh."},
	{Name: "faces", Stage: "confirm_geometry", Web: "quality_override", Type: "integer", Values: "gen-2.5 high and extreme-high: 20000-2000000; other gen-2.5 tiers: 500-1000000; gen-1.5: 500-200000", Notes: "Raw mesh with Simplify; under 20000 the web warns of a low-poly model."},
	{Name: "smart_low_poly", Stage: "confirm_geometry", Web: "task/rodin_polygen", Type: "bool + density", Values: "density low, medium, high; type raw or quad", Gate: "RodinRemeshAdditionalFeatures", Notes: "The web labels Confirm 2 Credits in this mode (not measured)."},
	{Name: "bake_normal", Stage: "confirm_geometry", Web: "quad_normal", Type: "bool", Notes: "Raw mesh: only with Simplify and at most 20000 faces."},
	{Name: "symmetric", Stage: "confirm_geometry", Web: "symmetric", Type: "bool", Default: "true"},
	{Name: "smooth", Stage: "confirm_geometry", Web: "mesh_smooth", Type: "bool", Notes: "Quad mesh."},
	{Name: "simplify", Stage: "confirm_geometry", Web: "mesh_simplify", Type: "bool", Notes: "Raw mesh."},
	// generate_texture
	{Name: "texture_mode", Stage: "generate_texture", Web: "texture_mode", Type: "enum", Values: "extreme-low, low, medium, high, extreme-high (gen-2.5); legacy (gen-1.5)", Default: "high"},
	{Name: "escore", Stage: "generate_texture", Web: "escore", Type: "number", Values: "0-10, step 0.5 (PBR temperature)", Default: "7"},
	{Name: "reference_scale", Stage: "generate_texture", Web: "reference_scale", Type: "number", Values: "0-1, step 0.05", Default: "1", Models: "gen-1.5"},
	{Name: "hd_texture", Stage: "generate_texture", Web: "superres_texture", Type: "bool", Gate: "RodinTextureAdditionalFeatures", Notes: "Ignored at texture_mode extreme-high."},
	{Name: "sharp", Stage: "generate_texture", Web: "sharp_texture", Type: "bool", Gate: "RodinTextureAdditionalFeatures", Notes: "The web's 8K chip; ignored at texture_mode extreme-high."},
	{Name: "face_enhance", Stage: "generate_texture", Web: "face_enhance", Type: "bool", Notes: "Face Restore; ignored at texture_mode extreme-high."},
	{Name: "delight", Stage: "generate_texture", Web: "texture_delight", Type: "bool"},
	{Name: "reference_image", Stage: "generate_texture", Web: "texture_reference_filename or a new image", Type: "input image name or local path", Default: "the first input image"},
	{Name: "smart_uv", Stage: "generate_texture", Web: "rodin_smart_uv_version", Type: "bool", Default: "native UV"},
	// export_model
	{Name: "type", Stage: "export_model", Web: "type", Type: "enum", Values: "mesh, material", Default: "by stage"},
	{Name: "format", Stage: "export_model", Web: "geometry_file_format", Type: "enum", Values: "obj, fbx, glb, usdz, stl", Default: "obj"},
	{Name: "resolution", Stage: "export_model", Web: "resolution Basic | High", Type: "enum", Values: "1k, 2k, 6k -> Basic; 4k, 12k -> High (6k and 12k for extreme-high textures)", Default: "2k", Gate: "High: RodinPackHighResolution", Notes: "Material packs only; a mesh pack sends no resolution, as the web does."},
	{Name: "geometry", Stage: "export_model", Web: "geometry Base | Lod | HighPoly", Type: "enum", Values: "base, lod, high_poly", Default: "base", Gate: "high_poly: RodinPackHighResolution"},
	{Name: "material", Stage: "export_model", Web: "material", Type: "enum", Values: "pbr, shaded, all", Default: "all", Notes: "Material packs only; a mesh pack sends no material, as the web does."},
	{Name: "align_ground", Stage: "export_model", Web: "align_ground", Type: "bool", Default: "true (model on the ground)"},
	{Name: "height", Stage: "export_model", Web: "height", Type: "number", Notes: "Custom real-world size."},
}

// ViewTags in the order of the web's direction bar.
var ViewTags = []ViewTag{
	{"?", "Unknown"}, {"F", "Front"}, {"FL", "Front Left"}, {"L", "Left"}, {"BL", "Back Left"},
	{"B", "Back"}, {"BR", "Back Right"}, {"R", "Right"}, {"FR", "Front Right"}, {"U", "Up"}, {"D", "Down"},
}

// DefaultWeights by image count (web table qX).
var DefaultWeights = []Weights{
	{1, []float64{1}, []float64{1}},
	{2, []float64{0.5, 0.5}, []float64{0.6, 0.4}},
	{3, []float64{0.33, 0.33, 0.34}, []float64{0.43, 0.33, 0.24}},
	{4, []float64{0.25, 0.25, 0.25, 0.25}, []float64{0.4, 0.3, 0.2, 0.1}},
	{5, []float64{0.2, 0.2, 0.2, 0.2, 0.2}, []float64{0.3, 0.25, 0.2, 0.15, 0.1}},
}

// Privileges that gate settings (notes 14, section 4.3).
var Privileges = []Privilege{
	{"RodinUseMultiImage", "2 or more images in one submit"},
	{"RodinUseAdditionalImage", "tier extreme-high; extra material reference images"},
	{"RodinMeshAdditionalFeatures", "raw (Triangle) mesh with a face count; quad quality choice"},
	{"RodinRemeshAdditionalFeatures", "smart low-poly"},
	{"RodinTextureAdditionalFeatures", "HD and 8K texture options"},
	{"RodinPackHighResolution", "4K and 12K packs; high-poly pack geometry"},
	{"RodinDownloadModel", "downloads of Gen-2.5 models above Low"},
	{"RodinBangGenerateAdvancedQuality", "part split strength above 5"},
	{"RodinHighRedoCount", "50 geometry and 15 material redos"},
	{"RodinMediumRedoCount", "20 geometry and 6 material redos"},
	{"UnlimitedPrivateModel", "private tasks without a quota"},
}
