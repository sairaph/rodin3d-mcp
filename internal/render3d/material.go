package render3d

import "math"

// Material is a glTF metallic-roughness material, as the triangles of a
// model file carry it (Mesh.Mats): factors in linear units and texture
// maps, any of which may be nil. The textured modes draw it the way
// three.js's glTF loader sets up MeshStandardMaterial: base colour times
// its map, roughness and metalness times the map's G and B, the normal map
// scaled by NormalScale, the occlusion map's R darkening the environment
// light by OcclusionStrength, and the emissive colour times its map added.
// The base colour's alpha is not drawn (the model is opaque).
type Material struct {
	Name              string
	BaseColor         [4]float64
	Metallic          float64
	Roughness         float64
	Emissive          [3]float64 // KHR_materials_emissive_strength applied
	NormalScale       float64
	OcclusionStrength float64
	// Unlit is KHR_materials_unlit: the base colour without lighting (three.js
	// MeshBasicMaterial).
	Unlit bool

	Base, MetalRough, Normal, Occlusion, Emission *TexRef

	shaded *TexRef // a task's shaded map (Maps.Shaded), drawn by the shaded mode
}

// DefaultMaterial returns glTF's default material: white, metalness and
// roughness 1, no maps.
func DefaultMaterial() *Material {
	return &Material{BaseColor: [4]float64{1, 1, 1, 1}, Metallic: 1, Roughness: 1, NormalScale: 1, OcclusionStrength: 1}
}

// mapsMaterial is the material of a task's stage maps: the default
// material with the diffuse, pbr and normal maps, which draws what the
// web's PBR material does (its factors are 1).
func mapsMaterial(maps *Maps) *Material {
	m := DefaultMaterial()
	if maps == nil {
		return m
	}
	ref := func(t *Texture) *TexRef {
		if t == nil {
			return nil
		}
		return &TexRef{Tex: t}
	}
	m.Base, m.MetalRough, m.Normal, m.shaded = ref(maps.Diffuse), ref(maps.PBR), ref(maps.Normal), ref(maps.Shaded)
	return m
}

// matOf is the material of triangle tri: the mesh's own when it has
// materials, else def.
func (m *Mesh) matOf(tri int, def *Material) *Material {
	switch {
	case len(m.Mats) == 0:
		return def
	case m.TriMat == nil:
		return m.Mats[0]
	}
	return m.Mats[m.TriMat[tri]]
}

// baseAt is the base colour (linear RGB) at uv; maps are sampled only
// where the mesh has texture coordinates (hasUV).
func (m *Material) baseAt(uv [2]float64, hasUV bool) vec3 {
	c := vec3{m.BaseColor[0], m.BaseColor[1], m.BaseColor[2]}
	if m.Base != nil && hasUV {
		t := m.Base.sample(uv)
		c = c.hadamard(vec3{float64(t[0]), float64(t[1]), float64(t[2])})
	}
	return c
}

// metalRoughAt is the metalness and roughness at uv (the map's B and G).
func (m *Material) metalRoughAt(uv [2]float64, hasUV bool) (metal, rough float64) {
	metal, rough = m.Metallic, m.Roughness
	if m.MetalRough != nil && hasUV {
		t := m.MetalRough.sample(uv)
		metal *= float64(t[2])
		rough *= float64(t[1])
	}
	return metal, rough
}

// occlusionAt is the ambient occlusion at uv: 1 + strength (R - 1).
func (m *Material) occlusionAt(uv [2]float64, hasUV bool) float64 {
	if m.Occlusion == nil || !hasUV {
		return 1
	}
	return 1 + m.OcclusionStrength*(float64(m.Occlusion.sample(uv)[0])-1)
}

// emissiveAt is the emitted colour (linear RGB) at uv.
func (m *Material) emissiveAt(uv [2]float64, hasUV bool) vec3 {
	c := vec3(m.Emissive)
	if c == (vec3{}) || m.Emission == nil || !hasUV {
		return c
	}
	t := m.Emission.sample(uv)
	return c.hadamard(vec3{float64(t[0]), float64(t[1]), float64(t[2])})
}

// unlitAt is what the shaded mode draws: a task's shaded map; for a model
// file, the emissive colour when the material has an emissive map and no
// base colour map (the layout of Rodin's shaded pack: a black base and the
// picture as emission), else the base colour.
func (m *Material) unlitAt(uv [2]float64, hasUV bool) vec3 {
	if m.shaded != nil {
		t := m.shaded.sample(uv)
		return vec3{float64(t[0]), float64(t[1]), float64(t[2])}
	}
	if m.Base == nil && m.Emission != nil && !m.Unlit && hasUV {
		return m.emissiveAt(uv, hasUV)
	}
	return m.baseAt(uv, hasUV)
}

// Wrap is how a texture repeats past the unit square along one axis (glTF
// sampler wrapS and wrapT).
type Wrap uint8

// The wrap modes.
const (
	WrapRepeat Wrap = iota // REPEAT
	WrapClamp              // CLAMP_TO_EDGE
	WrapMirror             // MIRRORED_REPEAT
)

// UVTransform maps texture coordinates as KHR_texture_transform does:
// u' = T[0] u + T[1] v + T[2], v' = T[3] u + T[4] v + T[5].
type UVTransform [6]float64

// NewUVTransform returns KHR_texture_transform's matrix: scale, then
// rotate by rotation radians, then offset.
func NewUVTransform(offset [2]float64, rotation float64, scale [2]float64) *UVTransform {
	c, s := math.Cos(rotation), math.Sin(rotation)
	return &UVTransform{c * scale[0], s * scale[1], offset[0], -s * scale[0], c * scale[1], offset[1]}
}

// TexRef is a texture as a material samples it: the image, the sampler's
// wrap modes and a texture transform (nil: none).
type TexRef struct {
	Tex          *Texture
	WrapU, WrapV Wrap
	Transform    *UVTransform
}

// uv is texture coordinate p after the transform.
func (r *TexRef) uv(p [2]float64) [2]float64 {
	if t := r.Transform; t != nil {
		return [2]float64{t[0]*p[0] + t[1]*p[1] + t[2], t[3]*p[0] + t[4]*p[1] + t[5]}
	}
	return p
}

// duv is a texture coordinate step d after the transform (its linear part:
// screen derivatives and triangle edges).
func (r *TexRef) duv(d [2]float64) [2]float64 {
	if t := r.Transform; t != nil {
		return [2]float64{t[0]*d[0] + t[1]*d[1], t[3]*d[0] + t[4]*d[1]}
	}
	return d
}

func (r *TexRef) sample(p [2]float64) [4]float32 {
	q := r.uv(p)
	return r.Tex.SampleWrap(q[0], q[1], r.WrapU, r.WrapV)
}
