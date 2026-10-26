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

// Camera is an orbit camera round the framed model: the direction as
// three.js spherical angles (Theta about +Y from +Z, Phi from +Y, radians),
// a zoom (1 is the web viewer's framing distance, 2 half that distance) and
// a pan that moves the look-at point from the model centre along the view's
// right and up, in units of the framing size (Frame.Size).
type Camera struct {
	Theta, Phi float64
	Zoom       float64 // 0 means 1
	Pan        [2]float64
}

// Camera returns the view's direction as an orbit camera with zoom 1 and no
// pan; ok is false for ViewGrid and unknown views.
func (v View) Camera() (Camera, bool) {
	theta, phi, ok := v.Angles()
	return Camera{Theta: theta, Phi: phi, Zoom: 1}, ok
}

// Frame is the box the camera frames: a mesh's bounding box.
type Frame struct {
	Min, Max [3]float64
}

// FrameOf returns the bounding box of m.
func FrameOf(m *Mesh) Frame {
	mn, mx := bounds(m.Pos)
	return Frame{Min: mn, Max: mx}
}

// Size is the framing size N of the web viewer (HomeScene._addModel): two
// thirds of the box's half diagonal plus a third of its largest side. The
// camera of zoom 1 stands 2.5 N from the look-at point.
func (f Frame) Size() float64 {
	size := vec3(f.Max).sub(vec3(f.Min))
	return 2*size.length()/2/3 + math.Max(size[0], math.Max(size[1], size[2]))/3
}

// Pose returns where camera c stands and what it looks at, in model units,
// and its vertical field of view in degrees.
func (f Frame) Pose(c Camera, aspect float64) (eye, target [3]float64, fovDeg float64) {
	cam, ok := f.camera(c, aspect)
	if !ok {
		return eye, target, 45
	}
	return cam.eye, cam.target, 45
}

// camera builds the render camera of c for frame f.
func (f Frame) camera(c Camera, aspect float64) (camera, bool) {
	center := vec3(f.Min).add(vec3(f.Max)).mul(0.5)
	N := f.Size()
	if !(N > 0) || math.IsInf(N, 0) {
		return camera{}, false
	}
	zoom := c.Zoom
	if !(zoom > 0) {
		zoom = 1
	}
	dist := 2.5 * N / zoom
	dir := vec3{math.Sin(c.Phi) * math.Sin(c.Theta), math.Cos(c.Phi), math.Sin(c.Phi) * math.Cos(c.Theta)}
	cam := newCamera(center.add(dir.mul(dist)), center, 45, aspect)
	if c.Pan != [2]float64{} {
		target := center.add(cam.right.mul(c.Pan[0] * N)).add(cam.up.mul(c.Pan[1] * N))
		cam = newCamera(target.add(dir.mul(dist)), target, 45, aspect)
	}
	return cam, true
}

// ErrCanceled is returned by Render when Options.Cancel closed before the
// picture was finished.
var ErrCanceled = errors.New("render3d: canceled")

// Options select the mode and camera.
type Options struct {
	Mode Mode
	View View // "" is ViewThreeQuarter
	Size int  // square output in pixels, 16-2048; a grid's cells are Size/2
	// Width and Height, when both are set, ask for a Width x Height picture
	// (16-4096 each) instead of the Size square; not with ViewGrid. The
	// vertical field of view stays 45 degrees.
	Width, Height int
	// Camera, when set, replaces the view's direction (not with ViewGrid).
	Camera *Camera
	// Frame, when set, is framed instead of the mesh's own box, so a
	// simplified copy of a mesh is drawn from the same camera as the mesh.
	Frame *Frame
	// Draft marks a mesh decoded from a .drc geometry draft (web ext "drc":
	// flat normals in NormalPreview, Raw and Metal).
	Draft bool
	// SS is the supersampling factor per axis; 0 means 2.
	SS int
	// Interactive skips the SAO pass, for quick frames while the camera
	// moves.
	Interactive bool
	// Background, when set, replaces the mode's background with this
	// display (sRGB) colour.
	Background *[3]uint8
	// WireOverlay draws the triangle edges over any mode.
	WireOverlay bool
	// Cancel, when closed, ends the render early with ErrCanceled.
	Cancel <-chan struct{}
}

// canceled reports whether o.Cancel is closed.
func (o *Options) canceled() bool {
	if o.Cancel == nil {
		return false
	}
	select {
	case <-o.Cancel:
		return true
	default:
		return false
	}
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
	if o.Width > 0 || o.Height > 0 {
		if o.Width < 16 || o.Width > 4096 || o.Height < 16 || o.Height > 4096 {
			return nil, st, fmt.Errorf("render3d: size %dx%d out of range", o.Width, o.Height)
		}
		if o.View == ViewGrid {
			return nil, st, errors.New("render3d: a grid is square; give Size, not Width and Height")
		}
	} else if o.Size < 16 || o.Size > 2048 {
		return nil, st, fmt.Errorf("render3d: size %d out of range", o.Size)
	}
	if (def.useMaps || def.copper || def.needUV) && m.UV == nil {
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
	frame := FrameOf(m)
	if o.Frame != nil {
		frame = *o.Frame
	}
	if o.Camera != nil {
		if o.View == ViewGrid {
			return nil, st, errors.New("render3d: a grid has its own cameras")
		}
		return render(m, maps, def, o, frame, *o.Camera)
	}
	if o.View != ViewGrid {
		c, ok := o.View.Camera()
		if !ok {
			return nil, st, fmt.Errorf("render3d: unknown view %q", o.View)
		}
		return render(m, maps, def, o, frame, c)
	}
	cell := o.Size / 2
	if cell < 16 {
		return nil, st, fmt.Errorf("render3d: size %d is too small for a grid", o.Size)
	}
	t0 := time.Now()
	out := image.NewNRGBA(image.Rect(0, 0, 2*cell, 2*cell))
	for i, v := range GridViews {
		c, _ := v.Camera()
		co := o
		co.Size = cell
		img, s, err := render(m, maps, def, co, frame, c)
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

// render draws one picture from camera c.
func render(m *Mesh, maps *Maps, def modeDef, o Options, frame Frame, c Camera) (*image.NRGBA, Stats, error) {
	var st Stats
	t0 := time.Now()
	ss := o.SS
	if ss <= 0 {
		ss = 2
	}
	w, h := o.Size, o.Size
	if o.Width > 0 && o.Height > 0 {
		w, h = o.Width, o.Height
	}
	W, H := w*ss, h*ss

	// Framing as HomeScene._addModel: centre on the bounding box, N = 2r/3 +
	// maxSize/3 (r = half the box diagonal), camera at 2.5 N (zoom 1), fov 45.
	center := vec3(frame.Min).add(vec3(frame.Max)).mul(0.5)
	size := vec3(frame.Max).sub(vec3(frame.Min))
	r := size.length() / 2
	N := frame.Size()
	cam, ok := frame.camera(c, float64(w)/float64(h))
	if !ok {
		return nil, st, errors.New("render3d: the mesh has no extent")
	}
	dist := cam.eye.sub(cam.target).length()

	s := &shadeCtx{m: m, maps: maps, def: def, cam: &cam, center: center, ss: ss, W: W, H: H, N: N,
		flat: def.forceFlat || (def.flatIfDraft && o.Draft) || m.Nrm == nil, nModel: len(m.Idx) / 3,
		cancel: o.canceled}
	if o.Interactive {
		s.shadowRes = 512
	}
	s.depthNear, s.depthFar = math.Max(dist-r, 1e-3), dist+r
	if o.Background != nil {
		bg := *o.Background
		s.bgOverride = &vec3{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255}
	}
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
	s.g = newGbuf(W, H)
	s.p = project(&cam, pos, W, H)
	rasterize(s.g, s.p, idx, float32(cam.near))
	if o.canceled() {
		return nil, st, ErrCanceled
	}
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
	s.col = make([]float32, W*H*4)
	if def.wire || o.WireOverlay {
		s.wire = make([]uint8, W*H)
	}
	s.shadeAll()
	st.Shade = time.Since(t1)
	if o.canceled() {
		return nil, st, ErrCanceled
	}

	t2 := time.Now()
	if def.sao > 0 && !o.Interactive {
		s.applySAO(def.sao, w, h)
		if o.canceled() {
			return nil, st, ErrCanceled
		}
	}
	if def.outline {
		s.applyOutline()
	}
	img := s.finish(w, h)
	if o.canceled() {
		return nil, st, ErrCanceled
	}
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
