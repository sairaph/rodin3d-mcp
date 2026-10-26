package render3d

import "math"

// Everything the web viewer loads as image or model files (environments,
// CSS backgrounds, the copper maps, the ground plane) is computed here
// instead, from a few measured numbers per resource: mean colours, light
// directions and energies, gradient and noise scales. Directions are world
// space (Y up, front +Z); the equirect frame is three.js's (u = 0.5 at +X,
// u = 0.75 at +Z).

// envRadiance maps an environment to its radiance function.
var envRadiance = [envCount]func(d vec3) vec3{
	envRoom:       roomRadiance,
	envSoftbox:    softboxRadiance,
	envSoftboxDim: softboxDimRadiance,
	envSunset:     sunsetRadiance,
}

// smoothstep is GLSL smoothstep; edge0 may be above edge1 (falling edge).
func smoothstep(edge0, edge1, x float64) float64 {
	t := sat((x - edge0) / (edge1 - edge0))
	return t * t * (3 - 2*t)
}

// elevation is the angle of d above the horizon in degrees.
func elevation(d vec3) float64 { return math.Asin(math.Max(-1, math.Min(1, d[1]))) * 180 / math.Pi }

// angleDeg is the angle between two unit directions in degrees.
func angleDeg(a, b vec3) float64 {
	return math.Acos(math.Max(-1, math.Min(1, a.dot(b)))) * 180 / math.Pi
}

// lamp is a small disc light of angular radius rho degrees that adds the
// given irradiance (radiance times solid angle) to the environment.
type lamp struct {
	dir    vec3
	rho    float64
	energy vec3
}

func (l lamp) at(d vec3) vec3 {
	c := math.Cos(l.rho * math.Pi / 180)
	if d.dot(l.dir) <= c {
		return vec3{}
	}
	return l.energy.mul(1 / (2 * math.Pi * (1 - c)))
}

// roomLamps are the interior map's bright fixtures, below the horizon as in
// the measured map (energies from its hottest texels).
var roomLamps = []lamp{
	{vec3{0.02, -0.84, -0.54}.norm(), 2, vec3{3.3, 3.1, 2.0}},
	{vec3{0.60, -0.78, -0.17}.norm(), 2, vec3{2.2, 1.6, 1.25}},
	{vec3{-0.85, -0.47, -0.24}.norm(), 2.5, vec3{1.0, 0.7, 0.32}},
	{vec3{-0.46, -0.52, -0.71}.norm(), 1.5, vec3{0.11, 0.08, 0.03}},
}

// roomRadiance stands in for interior.exr (mean luminance 1.04): a dim warm
// ceiling, neutral walls, a brighter warm floor, a cool window between +X and
// +Z, a warm band under it and four small lamps.
func roomRadiance(d vec3) vec3 {
	e := elevation(d)
	ceiling := vec3{0.17, 0.11, 0.06}
	wall := vec3{0.33, 0.30, 0.28}
	floor := vec3{0.50, 0.42, 0.34}
	var c vec3
	if e > 0 {
		c = mix3(wall, ceiling, smoothstep(20, 60, e))
	} else {
		c = mix3(wall, floor, smoothstep(-5, -35, e))
	}
	phi := math.Atan2(d[2], d[0]) * 180 / math.Pi
	// window: phi -15 to 55, elevation -35 to 20, brightest at its +X edge
	win := smoothstep(-20, -10, phi) * smoothstep(60, 50, phi) * smoothstep(-40, -30, e) * smoothstep(25, 15, e)
	if win > 0 {
		k := 1 - 0.6*sat((phi+15)/70)
		c = mix3(c, vec3{3.6, 5.0, 7.3}.mul(k), win)
	}
	// warm band under the window
	band := smoothstep(-20, -5, phi) * smoothstep(70, 55, phi) * smoothstep(-48, -42, e) * smoothstep(-34, -38, e)
	c = mix3(c, vec3{3.5, 1.6, 0.6}, band*0.8)
	for _, l := range roomLamps {
		c = c.add(l.at(d))
	}
	return c
}

// softboxRadiance stands in for top_area_light_2.exr: a uniform grey 0.6
// with a large white light overhead (6.0 above about 58 degrees).
func softboxRadiance(d vec3) vec3 {
	v := 0.6 + 5.4*smoothstep(55, 61, elevation(d))
	return vec3{v, v, v}
}

// softboxDimRadiance stands in for top_area_light_3.exr: grey 0.6 above the
// horizon, a 1.0 light overhead and a floor that fades to black by -18
// degrees.
func softboxDimRadiance(d vec3) vec3 {
	e := elevation(d)
	v := 0.6 + 0.4*smoothstep(55, 61, e)
	if e < -2 {
		v = 0.6 * sat((e+18)/16)
	}
	return vec3{v, v, v}
}

// sunDir is the sun of venice_sunset_1k.hdr, 3.4 degrees above the horizon.
var sunDir = vec3{0.81, 0.06, 0.58}.norm()

var sunDisc = lamp{sunDir, 0.6, vec3{0.5, 0.08, 0.005}}

// sunGlow is the warm haze round the sun at an angle theta in degrees, fitted
// to the map's excess over the open sky (red falls off fastest).
func sunGlow(theta float64) vec3 {
	return vec3{6 * math.Exp(-theta/24), 3.6 * math.Exp(-theta/26), 1.2 * math.Exp(-theta/30)}
}

// skyline is the height of the buildings in degrees above the horizon per
// azimuth in degrees (atan2(z, x)), measured from the map: open water towards
// the sun, the tallest blocks around -X.
var skyline = [][2]float64{
	{-180, 28}, {-165, 22}, {-150, 12}, {-135, 14}, {-125, 15}, {-115, 11}, {-100, 7}, {-85, 4}, {-72, 1}, {-65, 0},
	{92, 0}, {97, 1}, {107, 5}, {118, 7}, {128, 9}, {140, 24}, {152, 28}, {163, 31}, {180, 28},
}

func skylineAt(phi float64) float64 {
	for i := 1; i < len(skyline); i++ {
		a, b := skyline[i-1], skyline[i]
		if phi <= b[0] {
			return a[1] + (b[1]-a[1])*(phi-a[0])/(b[0]-a[0])
		}
	}
	return skyline[len(skyline)-1][1]
}

// sunsetRadiance stands in for venice_sunset_1k.hdr: a blue sky, bluest about
// 28 degrees up, the sun low between +X and +Z with its glow, a skyline of
// sunlit brown facades away from the sun, and dark water that mirrors the
// glow.
func sunsetRadiance(d vec3) vec3 {
	e := elevation(d)
	if e < 0 {
		m := vec3{d[0], -d[1], d[2]} // mirror direction
		return vec3{0.12, 0.10, 0.12}.add(sunGlow(angleDeg(m, sunDir)).mul(0.25 * math.Exp(e/30)))
	}
	horizon := vec3{0.52, 0.78, 1.12}
	sky := vec3{0.43, 0.74, 1.30}
	zenith := vec3{0.22, 0.37, 0.72}
	var c vec3
	if e < 28 {
		c = mix3(horizon, sky, smoothstep(5, 28, e))
	} else {
		c = mix3(sky, zenith, smoothstep(28, 85, e))
	}
	c = c.add(sunGlow(angleDeg(d, sunDir))).add(sunDisc.at(d))
	// buildings in 4-degree blocks of varied height and brightness
	phi := math.Atan2(d[2], d[0]) * 180 / math.Pi
	block := int32(math.Floor((phi + 180) / 4))
	height := skylineAt(phi) * (0.8 + 0.4*hash1(block, 7))
	if height > 0 && e < height {
		facade := vec3{0.24, 0.14, 0.09}.mul(0.3 + 0.9*hash1(block, 13))
		c = mix3(c, facade, smoothstep(height, height-0.7, e))
	}
	return c
}

// Backgrounds: the CSS images the web draws behind a transparent canvas,
// as functions of the canvas position (u, v from 0 to 1, top left), in
// display (sRGB) values.

// vignetteBackground stands in for background-0.jpg drawn with
// background-size cover: a blue-grey radial gradient, (180, 189, 199) at the
// centre falling linearly with the distance from it (the image is 543x655,
// so the square canvas shows its middle and distances are a little
// elliptical).
func vignetteBackground(u, v float64) vec3 {
	du, dv := u-0.5, (v-0.5)*543/655
	r := math.Sqrt(du*du + dv*dv)
	c := vec3{179.5 - 200*r, 189.3 - 213*r, 198.7 - 227*r}
	return vec3{sat(c[0] / 255), sat(c[1] / 255), sat(c[2] / 255)}
}

// ringBackground stands in for bronze.png: dark slate (42, 45, 45) with a
// soft lighter ring at a quarter of the width from the centre.
func ringBackground(u, v float64) vec3 {
	du, dv := u-0.5, v-0.5
	r := math.Sqrt(du*du + dv*dv)
	g := math.Exp(-(r - 0.24) * (r - 0.24) / (2 * 0.09 * 0.09))
	return vec3{(42 + 18*g) / 255, (45 + 19*g) / 255, (45 + 21*g) / 255}
}

// Copper (web Copper): the bundle's material reads four maps, all loaded
// without a colour space. Their measured content: a flat colour, a rust
// colour, a multi-scale roughness (mean .665, sd .064, structure up to a
// quarter of the map) and a fine bump (sd .0047, grain about 6 texels of
// 2048, drawn at uv x3).

var (
	copperColor = vec3{51.2 / 255, 56.2 / 255, 50.1 / 255}
	copperRust  = vec3{74.1 / 255, 56.2 / 255, 46.8 / 255}
)

// copperRustAt is the rust colour with its measured variation (R and B move
// against each other).
func copperRustAt(u, v float64) vec3 {
	n := fbm(u*8, v*8, 4, 11)
	return vec3{(74.1 + 4.1*n) / 255, (56.2 + 1.7*n) / 255, (46.8 - 3.6*n) / 255}
}

// copperRoughnessAt is the G channel of the roughness map.
func copperRoughnessAt(u, v float64) float64 {
	return sat(0.665 + 0.064*fbm(u*4, v*4, 7, 23))
}

// copperBumpAt is the bump height at bump coordinates (uv x3).
func copperBumpAt(u, v float64) float64 {
	return 0.0047 * fbm(u*340, v*340, 3, 37)
}

// The ground plane (web AO and Copper): the viewer's plane is 2 x 2 scaled
// by (3.078, 1, 2.715) at the bottom of the model box, black with a soft
// round contact shadow as its alpha (0.5 at the centre, 0.2 at 0.65 units,
// gone by 1.3), drawn alpha blended.
const groundHalfX, groundHalfZ = 3.0781586170196533, 2.714895486831665

// groundMesh is the plane at y = 0, centred on the origin, two triangles
// facing up.
var groundMesh = &Mesh{
	Pos: [][3]float32{{-groundHalfX, 0, -groundHalfZ}, {groundHalfX, 0, -groundHalfZ}, {groundHalfX, 0, groundHalfZ}, {-groundHalfX, 0, groundHalfZ}},
	Idx: []uint32{0, 2, 1, 0, 3, 2},
}

// groundAlpha is the contact shadow's opacity at a horizontal distance r
// from the model centre.
func groundAlpha(r float64) float64 {
	x := r / 0.67
	return 0.5 * math.Exp(-x*x*x)
}

// Noise: hashed value noise and its fractal sum, deterministic.

func hash2(x, y int32, seed uint32) float64 {
	h := uint32(x)*0x8da6b343 ^ uint32(y)*0xd8163841 ^ seed*0xcb1ab31f
	h ^= h >> 15
	h *= 0x2c1b3c6d
	h ^= h >> 12
	h *= 0x297a2d39
	h ^= h >> 15
	return float64(h)/(1<<31) - 1 // [-1, 1)
}

func hash1(x int32, seed uint32) float64 { return hash2(x, 0, seed)*0.5 + 0.5 }

// valueNoise is smooth value noise in [-1, 1]; its standard deviation is
// valueNoiseSD (corners uniform: variance 1/3, times .743 per axis for the
// smoothstep blend).
func valueNoise(x, y float64, seed uint32) float64 {
	fx, fy := math.Floor(x), math.Floor(y)
	ix, iy := int32(int64(fx)), int32(int64(fy))
	tx, ty := x-fx, y-fy
	sx, sy := tx*tx*(3-2*tx), ty*ty*(3-2*ty)
	a := hash2(ix, iy, seed) + (hash2(ix+1, iy, seed)-hash2(ix, iy, seed))*sx
	b := hash2(ix, iy+1, seed) + (hash2(ix+1, iy+1, seed)-hash2(ix, iy+1, seed))*sx
	return a + (b-a)*sy
}

const valueNoiseSD = 0.4290

// fbm sums octaves of value noise (each half the amplitude, twice the
// frequency) scaled to a standard deviation of about 1.
func fbm(x, y float64, octaves int, seed uint32) float64 {
	sum, amp, sq := 0.0, 1.0, 0.0
	for i := 0; i < octaves; i++ {
		sum += amp * valueNoise(x, y, seed+uint32(i)*101)
		sq += amp * amp
		amp *= 0.5
		x, y = x*2+17.3, y*2+5.9
	}
	return sum / (valueNoiseSD * math.Sqrt(sq))
}
