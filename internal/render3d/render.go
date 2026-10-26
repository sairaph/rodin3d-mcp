package render3d

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"time"
)

// Maps are the stage textures of a material-stage task (the card's
// texture_* images): diffuse and shaded are colour data, normal and pbr
// (G roughness, B metalness) are not. Any may be nil.
type Maps struct {
	Diffuse, Normal, PBR, Shaded *Texture
}

// View is a named camera. All views look at the centre of the model's
// bounding box from the web viewer's framing distance; they differ in
// direction only.
type View string

// The views. Directions are in the model's glTF frame: front is +Z, up +Y,
// and right is +X, the side on the right of the front view.
const (
	ViewThreeQuarter View = "three_quarter" // front-right, 45 degrees round, 20 degrees up
	ViewFront        View = "front"         // from +Z, level
	ViewBack         View = "back"          // from -Z, level
	ViewLeft         View = "left"          // from -X, level
	ViewRight        View = "right"         // from +X, level
	ViewTop          View = "top"           // from +Y, front at the bottom of the picture
	ViewWeb          View = "web"           // the web viewer's camera after a model loads
	ViewGrid         View = "grid"          // 2x2: front, right / back, three_quarter
)

// Views lists every view, the default first.
var Views = []View{ViewThreeQuarter, ViewFront, ViewBack, ViewLeft, ViewRight, ViewTop, ViewWeb, ViewGrid}

// GridViews are the cells of ViewGrid: top left, top right, bottom left,
// bottom right.
var GridViews = [4]View{ViewFront, ViewRight, ViewBack, ViewThreeQuarter}

// WebTheta and WebPhi are the web viewer's default camera after a model
// loads (gotoPOI theta 0, phi 1.37: on +Z, 11.5 degrees above the horizon).
const WebTheta, WebPhi = 0.0, 1.37

// Angles returns the view's camera direction as three.js spherical angles:
// theta about +Y from +Z, phi from +Y. ok is false for ViewGrid and unknown
// views.
func (v View) Angles() (theta, phi float64, ok bool) {
	level := math.Pi / 2
	switch v {
	case ViewThreeQuarter:
		return math.Pi / 4, level - 20*math.Pi/180, true
	case ViewFront:
		return 0, level, true
	case ViewBack:
		return math.Pi, level, true
	case ViewLeft:
		return -math.Pi / 2, level, true
	case ViewRight:
		return math.Pi / 2, level, true
	case ViewTop:
		return 0, 0, true
	case ViewWeb:
		return WebTheta, WebPhi, true
	}
	return 0, 0, false
}

// Options select the mode and camera.
type Options struct {
	Mode Mode
	View View // "" is ViewThreeQuarter
	Size int  // square output in pixels, 16-2048; a grid's cells are Size/2
	// Draft marks a mesh decoded from a .drc geometry draft (web ext "drc":
	// flat normals in NormalPreview, Raw and Metal).
	Draft bool
	// SS is the supersampling factor per axis; 0 means 2.
	SS int
}

// Stats report the timing of a render.
type Stats struct {
	Raster, Shade, Post, Total time.Duration
}

// InputError means the mesh or maps cannot show the mode: the mode needs
// texture coordinates or material textures that are not there.
type InputError struct {
	Mode   Mode
	Reason string
}

func (e *InputError) Error() string { return fmt.Sprintf("mode %s %s", e.Mode, e.Reason) }

// Render draws a mesh in a mode from a view (ViewGrid draws the four
// GridViews into one picture). A mode's environment is computed on first
// use.
func Render(m *Mesh, maps *Maps, o Options) (*image.NRGBA, Stats, error) {
	var st Stats
	def, ok := modeDefs[o.Mode]
	if !ok {
		return nil, st, fmt.Errorf("render3d: unknown mode %q", o.Mode)
	}
	if m == nil {
		return nil, st, errors.New("render3d: no mesh")
	}
	if err := m.Validate(); err != nil {
		return nil, st, err
	}
	if o.Size < 16 || o.Size > 2048 {
		return nil, st, fmt.Errorf("render3d: size %d out of range", o.Size)
	}
	if (def.useMaps || def.copper) && m.UV == nil {
		return nil, st, &InputError{Mode: o.Mode, Reason: "needs texture coordinates, and this mesh has none"}
	}
	if def.useMaps {
		need := maps != nil && maps.Diffuse != nil
		if o.Mode == ModeShaded {
			need = maps != nil && (maps.Shaded != nil || maps.Diffuse != nil)
		}
		if !need {
			return nil, st, &InputError{Mode: o.Mode, Reason: "needs the material textures"}
		}
	}
	if o.View == "" {
		o.View = ViewThreeQuarter
	}
	if o.View != ViewGrid {
		theta, phi, ok := o.View.Angles()
		if !ok {
			return nil, st, fmt.Errorf("render3d: unknown view %q", o.View)
		}
		return render(m, maps, def, o, theta, phi)
	}
	cell := o.Size / 2
	if cell < 16 {
		return nil, st, fmt.Errorf("render3d: size %d is too small for a grid", o.Size)
	}
	t0 := time.Now()
	out := image.NewNRGBA(image.Rect(0, 0, 2*cell, 2*cell))
	for i, v := range GridViews {
		theta, phi, _ := v.Angles()
		co := o
		co.Size = cell
		img, s, err := render(m, maps, def, co, theta, phi)
		if err != nil {
			return nil, st, err
		}
		st.Raster += s.Raster
		st.Shade += s.Shade
		st.Post += s.Post
		x, y := (i%2)*cell, (i/2)*cell
		draw.Draw(out, image.Rect(x, y, x+cell, y+cell), img, image.Point{}, draw.Src)
	}
	st.Total = time.Since(t0)
	return out, st, nil
}

// render draws one view.
func render(m *Mesh, maps *Maps, def modeDef, o Options, theta, phi float64) (*image.NRGBA, Stats, error) {
	var st Stats
	t0 := time.Now()
	ss := o.SS
	if ss <= 0 {
		ss = 2
	}
	W := o.Size * ss

	// Framing as HomeScene._addModel: centre on the bounding box, N = 2r/3 +
	// maxSize/3 (r = half the box diagonal), camera at 2.5 N, fov 45.
	bmin, bmax := bounds(m.Pos)
	center := bmin.add(bmax).mul(0.5)
	size := bmax.sub(bmin)
	r := size.length() / 2
	N := 2*r/3 + math.Max(size[0], math.Max(size[1], size[2]))/3
	if !(N > 0) || math.IsInf(N, 0) {
		return nil, st, errors.New("render3d: the mesh has no extent")
	}
	dist := 2.5 * N
	eye := center.add(vec3{dist * math.Sin(phi) * math.Sin(theta), dist * math.Cos(phi), dist * math.Sin(phi) * math.Cos(theta)})
	cam := newCamera(eye, center, 45, 1)

	s := &shadeCtx{m: m, maps: maps, def: def, cam: &cam, center: center, ss: ss, W: W, N: N,
		flat: def.forceFlat || (def.flatIfDraft && o.Draft) || m.Nrm == nil, nModel: len(m.Idx) / 3}
	s.depthNear, s.depthFar = dist-r, dist+r
	// Only MeshStandardMaterial (the model or the ground) reads the
	// environment (three r180); Glossy's Phong ignores the one it sets.
	if def.envIntensity > 0 && (def.mat == matStandard || def.ground) {
		s.env = environment(def.env)
	}
	if def.bg == bgEnv {
		s.bgEnv = environment(def.bgEnv)
	}
	for _, l := range def.lights {
		l.pos = l.pos.add(center)
		l.target = l.target.add(center)
		s.lights = append(s.lights, l)
	}

	// Geometry: the model, plus the ground plane when the mode shows it.
	pos, idx := m.Pos, m.Idx
	if def.ground {
		// A hair below the model's lowest point: a flat base exactly on the
		// plane would leave the depth test between coplanar triangles to
		// float noise (speckle under the base in Copper and AO).
		gy := float32(center[1] - size[1]/2 - 1e-3*N)
		pos = make([][3]float32, len(m.Pos), len(m.Pos)+len(groundMesh.Pos))
		copy(pos, m.Pos)
		base := uint32(len(pos))
		for _, p := range groundMesh.Pos {
			pos = append(pos, [3]float32{p[0] + float32(center[0]), p[1] + gy, p[2] + float32(center[2])})
		}
		idx = make([]uint32, len(m.Idx), len(m.Idx)+len(groundMesh.Idx))
		copy(idx, m.Idx)
		for _, i := range groundMesh.Idx {
			idx = append(idx, base+i)
		}
		s.groundBase = base
		s.groundY = float64(gy)
	}
	s.pos, s.idx = pos, idx
	s.g = newGbuf(W, W)
	s.p = project(&cam, pos, W, W)
	rasterize(s.g, s.p, idx, float32(cam.near))
	if def.mat == matToon {
		s.buildShadow()
	}
	if def.mat == matDepth {
		// Stretch to the visible depth range of this view.
		lo, hi := math.Inf(1), math.Inf(-1)
		for i, iz := range s.g.invZ {
			if s.g.tri[i] >= 0 && iz > 0 {
				d := 1 / float64(iz)
				lo, hi = math.Min(lo, d), math.Max(hi, d)
			}
		}
		if hi > lo {
			s.depthNear, s.depthFar = lo, hi
		}
	}
	st.Raster = time.Since(t0)

	t1 := time.Now()
	s.col = make([]float32, W*W*4)
	if def.wire {
		s.wire = make([]uint8, W*W)
	}
	s.shadeAll()
	st.Shade = time.Since(t1)

	t2 := time.Now()
	if def.sao > 0 {
		s.applySAO(def.sao, o.Size)
	}
	if def.outline {
		s.applyOutline(o.Size)
	}
	img := s.finish(o.Size)
	st.Post = time.Since(t2)
	st.Total = time.Since(t0)
	return img, st, nil
}

func bounds(p [][3]float32) (vec3, vec3) {
	mn := vec3{math.Inf(1), math.Inf(1), math.Inf(1)}
	mx := vec3{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, v := range p {
		for k := 0; k < 3; k++ {
			mn[k] = math.Min(mn[k], float64(v[k]))
			mx[k] = math.Max(mx[k], float64(v[k]))
		}
	}
	return mn, mx
}
