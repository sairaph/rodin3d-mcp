package render3d

import "math"

// Mode is a viewer mode. The names are the get_preview mode values; the web
// symbol each one reproduces is in modeDefs.
type Mode string

// The modes.
const (
	ModeNormal    Mode = "normal"     // web NormalPreview (draft)
	ModeRaw       Mode = "raw"        // web Raw
	ModeClassic   Mode = "classic"    // web Classic
	ModeGlossy    Mode = "glossy"     // web Glossy
	ModeCopper    Mode = "copper"     // web Copper
	ModeMetal     Mode = "metal"      // web Metal
	ModeAO        Mode = "ao"         // web AO
	ModeWireframe Mode = "wireframe"  // web Wireframe
	ModeGeometry  Mode = "geometry"   // web Geometry
	ModePBR       Mode = "pbr"        // web PBR
	ModeShaded    Mode = "shaded"     // web Shaded
	ModeToon      Mode = "toon"       // web Toon
	ModeDepth     Mode = "depth"      // local only: view depth, near white
	ModeUVChecker Mode = "uv_checker" // local only: a checker over the texture coordinates
)

// AllModes lists every mode.
var AllModes = []Mode{ModeNormal, ModeRaw, ModeClassic, ModeGlossy, ModeCopper, ModeMetal, ModeAO, ModeWireframe, ModeGeometry, ModePBR, ModeShaded, ModeToon, ModeDepth, ModeUVChecker}

// ModesFor lists the modes a stage mesh can be drawn in, in display order.
// A geometry draft (a .drc mesh: positions only, no normals or texture
// coordinates) gets the web's Gen-2.5 set without Copper, whose maps need
// texture coordinates, plus wireframe and depth. A GLB (confirmed geometry or
// material) adds copper and geometry; textured adds the material modes
// pbr, shaded and toon in front.
func ModesFor(draft, textured bool) []Mode {
	if draft {
		return []Mode{ModeNormal, ModeClassic, ModeGlossy, ModeMetal, ModeAO, ModeRaw, ModeWireframe, ModeDepth}
	}
	geo := []Mode{ModeNormal, ModeClassic, ModeGlossy, ModeCopper, ModeMetal, ModeAO, ModeRaw, ModeGeometry, ModeWireframe, ModeDepth, ModeUVChecker}
	if textured {
		return append([]Mode{ModePBR, ModeShaded, ModeToon}, geo...)
	}
	return geo
}

// NeedsMaps reports whether the mode draws the material textures.
func (m Mode) NeedsMaps() bool { return modeDefs[m].useMaps }

// Known reports whether m is a mode.
func (m Mode) Known() bool {
	_, ok := modeDefs[m]
	return ok
}

// Describe is a short description of the mode for replies.
func (m Mode) Describe() string {
	switch m {
	case ModeNormal:
		return "surface normals as colours (web NormalPreview)"
	case ModeRaw:
		return "raw normals without tone mapping (web Raw)"
	case ModeClassic:
		return "light grey clay under studio lights (web Classic)"
	case ModeGlossy:
		return "glossy white under a sunset sky (web Glossy)"
	case ModeCopper:
		return "dark green-grey patina with brown rust on sharp creases, on a ground plane (web Copper)"
	case ModeMetal:
		return "polished grey metal (web Metal)"
	case ModeAO:
		return "matte white with ambient occlusion on a ground plane (web AO)"
	case ModeWireframe:
		return "triangle edges over grey metal (web Wireframe)"
	case ModeGeometry:
		return "flat-shaded grey metal (web Geometry)"
	case ModePBR:
		return "the PBR textures: diffuse, normal, roughness and metalness maps (web PBR)"
	case ModeShaded:
		return "the shaded texture, unlit (web Shaded)"
	case ModeToon:
		return "toon shading with outlines (web Toon)"
	case ModeDepth:
		return "distance from the camera, near white (local)"
	case ModeUVChecker:
		return "a checker pattern over the texture coordinates, to show the UV layout and its seams (local)"
	}
	return string(m)
}

type matKind int

const (
	matNormal   matKind = iota // MeshNormalMaterial
	matStandard                // MeshStandardMaterial
	matPhong                   // MeshPhongMaterial
	matBasic                   // MeshBasicMaterial (unlit)
	matToon                    // MeshToonMaterial
	matDepth                   // local depth map
	matChecker                 // local UV checker
)

type lightKind int

const (
	lightDir lightKind = iota
	lightPoint
	lightSpot
	lightAmbient
)

type light struct {
	kind    lightKind
	pos     vec3
	target  vec3
	color   vec3 // linear, already times intensity
	shadow  bool
	coneCos float64
}

type bgKind int

const (
	bgCSS   bgKind = iota // transparent canvas over a CSS image (computed: procedural.go)
	bgColor               // scene.background colour (linear)
	bgEnv                 // scene.background equirect, blurred (computed: procedural.go)
)

// modeDef is everything the web viewer sets for a mode, read from the bundle
// (chunk 7859, HomeScene._applyRenderMode, _addModel, onInit, _addMaterials).
// Materials, lights, intensities and passes are the bundle's values; the
// environments, CSS backgrounds, copper maps and ground the bundle loads as
// files are computed (procedural.go).
type modeDef struct {
	web          string
	mat          matKind
	color        vec3 // linear
	metal, rough float64
	shininess    float64 // Phong
	specular     vec3    // Phong
	useMaps      bool    // PBR / Shaded / Toon textures
	copper       bool
	needUV       bool // reads texture coordinates (UV checker)
	newGray      bool // 0.8*sqrt(colour) shader patch (zt)
	forceFlat    bool // flat normals regardless of mesh normals (Wireframe, unbaked Geometry)
	flatIfDraft  bool // flat for Draco drafts (ext drc)
	env          envKind
	envIntensity float64
	lights       []light
	bg           bgKind
	bgImage      func(u, v float64) vec3 // bgCSS: display colour at a canvas position
	bgColor      vec3
	bgEnv        envKind
	bgBlur       float64
	sao          float64 // SAO intensity; 0 = pass disabled
	outputPass   bool    // OutputPass (Neutral tone mapping + sRGB) enabled
	direct       bool    // rendered without the composer (material sRGB encode)
	ground       bool
	outline      bool // Toon OutlineEffect
	wire         bool // triangle edge overlay
}

func hexLinear(h uint32) vec3 {
	return vec3{srgbToLinear(float64(h>>16&255) / 255), srgbToLinear(float64(h>>8&255) / 255), srgbToLinear(float64(h&255) / 255)}
}

func white(i float64) vec3 { return vec3{i, i, i} }

// defaultBackground is HomeScene._commitModelBackground without a RODIN glTF
// extension: [.129,.2,.282] + .1, linear.
var defaultBackground = vec3{0.12941176470588237 + 0.1, 0.2 + 0.1, 0.2823529411764706 + 0.1}

// defaultLights are _directionalLight (0,1,1) x3 with shadows and
// _ambientLight x0.5, shown only when the scene has no environment (Toon).
var defaultLights = []light{
	{kind: lightDir, pos: vec3{0, 1, 1}, color: white(3), shadow: true},
	{kind: lightAmbient, color: white(0.5)},
}

var modeDefs = map[Mode]modeDef{
	ModeNormal: {web: "NormalPreview", mat: matNormal, flatIfDraft: true, bg: bgCSS, bgImage: vignetteBackground},
	ModeRaw:    {web: "Raw", mat: matNormal, flatIfDraft: true, bg: bgCSS, bgImage: vignetteBackground},
	ModeMetal: {web: "Metal", mat: matStandard, color: vec3{0.5, 0.5, 0.5}, metal: 1, rough: 0.4, flatIfDraft: true,
		env: envRoom, envIntensity: 1, bg: bgCSS, bgImage: vignetteBackground},
	ModeClassic: {web: "Classic", mat: matStandard, color: hexLinear(15592948), metal: 0.2, rough: 0.5,
		env: envRoom, envIntensity: 0,
		lights: []light{
			{kind: lightDir, pos: vec3{0, 1, 1}, color: white(3)},
			{kind: lightAmbient, color: hexLinear(13491684).mul(1)},
			{kind: lightPoint, pos: vec3{0.5, 0.5, -1}, color: white(0.5)},
			{kind: lightPoint, pos: vec3{-0.5, 0.5, -1}, color: white(0.5)},
		},
		bg: bgCSS, bgImage: vignetteBackground, sao: 0.2, outputPass: true},
	ModeGlossy: {web: "Glossy", mat: matPhong, color: white(1), specular: white(1), shininess: 300,
		env: envRoom, envIntensity: 2,
		lights: []light{
			{kind: lightDir, pos: vec3{0.32, 0.39, 0.7}, color: white(2)},
			{kind: lightAmbient, color: hexLinear(10855845).mul(3)},
			{kind: lightSpot, pos: vec3{0, -0.3, -2}, color: white(4), coneCos: math.Cos(math.Pi / 2)},
		},
		bg: bgEnv, bgEnv: envSunset, bgBlur: 0.3, sao: 0.1, outputPass: true},
	ModeCopper: {web: "Copper", mat: matStandard, color: vec3{0.5, 0.5, 0.5}, metal: 0.92, copper: true,
		env: envSoftbox, envIntensity: 1.5,
		lights: []light{
			{kind: lightSpot, pos: vec3{1.4, 0.4, 1.8}, color: white(150), coneCos: math.Cos(math.Pi / 2)},
			{kind: lightSpot, pos: vec3{-1, 0.2, 0.3}, target: vec3{0, 0, -0.2}, color: white(15), coneCos: math.Cos(math.Pi / 2)},
			{kind: lightSpot, pos: vec3{0, 0.6, -2}, color: white(60), coneCos: math.Cos(math.Pi / 2)},
			{kind: lightAmbient, color: white(60)},
		},
		bg: bgCSS, bgImage: ringBackground, ground: true, outputPass: true},
	ModeAO: {web: "AO", mat: matStandard, color: hexLinear(14540253), metal: 0, rough: 0.5,
		env: envSoftboxDim, envIntensity: 1.5,
		lights: []light{
			{kind: lightSpot, pos: vec3{0, 4, 1}, color: white(40), coneCos: math.Cos(math.Pi / 2)},
			{kind: lightSpot, pos: vec3{0, -0.3, 4}, color: white(10), coneCos: math.Cos(math.Pi / 2)},
			{kind: lightSpot, pos: vec3{0, -0.3, -4}, color: white(3), coneCos: math.Cos(math.Pi / 2)},
		},
		bg: bgColor, bgColor: hexLinear(15790320), sao: 0.1, ground: true, outputPass: true},
	ModeWireframe: {web: "Wireframe", mat: matStandard, color: vec3{0.5, 0.5, 0.5}, metal: 0.8, rough: 0.5, newGray: true, forceFlat: true,
		// Single mesh: the CSS background-3 branch needs more than one mesh, so
		// scene.background is the default colour, cleared without tone mapping.
		env: envRoom, envIntensity: 2, bg: bgColor, bgColor: defaultBackground, direct: true, wire: true},
	ModeGeometry: {web: "Geometry", mat: matStandard, color: vec3{0.5, 0.5, 0.5}, metal: 0.8, rough: 0.5, newGray: true, forceFlat: true,
		env: envRoom, envIntensity: 2, bg: bgColor, bgColor: defaultBackground, outputPass: true},
	ModePBR: {web: "PBR", mat: matStandard, color: white(1), metal: 1, rough: 1, useMaps: true,
		env: envRoom, envIntensity: 2, bg: bgColor, bgColor: defaultBackground, outputPass: true},
	ModeShaded: {web: "Shaded", mat: matBasic, color: white(1), useMaps: true,
		bg: bgColor, bgColor: defaultBackground, outputPass: true},
	ModeToon: {web: "Toon", mat: matToon, color: white(1), useMaps: true, lights: defaultLights,
		bg: bgColor, bgColor: defaultBackground, outputPass: true, outline: true},
	ModeDepth:     {web: "-", mat: matDepth, bg: bgColor},
	ModeUVChecker: {web: "-", mat: matChecker, needUV: true, bg: bgColor, bgColor: defaultBackground, outputPass: true},
}
