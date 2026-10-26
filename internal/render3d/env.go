package render3d

import (
	"math"
	"sync"
)

// envMap is a lighting environment: an equirectangular radiance map computed
// from one of the procedural environments in procedural.go, prefiltered into
// a few roughness levels (a stand-in for three.js PMREM). Built on first use
// and shared read-only by every render.
type envMap struct {
	levels []envLevel  // roughness 0, .2, .4, .6, .8, 1
	samp   *envSampler // the path tracer's importance sampler (samplerMu)
}

type envLevel struct {
	w, h int
	rgb  []float32 // linear, rows top first
}

var envRoughness = []float64{0, 0.2, 0.4, 0.6, 0.8, 1}

// envKind names an environment. The web viewer's file each one stands in for
// is in procedural.go.
type envKind int

const (
	envNone       envKind = iota
	envRoom               // interior.exr
	envSoftbox            // top_area_light_2.exr
	envSoftboxDim         // top_area_light_3.exr
	envSunset             // venice_sunset_1k.hdr
	envCount
)

var envCache [envCount]struct {
	once sync.Once
	m    *envMap
}

// environment returns the prefiltered map of k (nil for envNone), computing
// it on first use (tens of milliseconds).
func environment(k envKind) *envMap {
	if k <= envNone || k >= envCount {
		return nil
	}
	e := &envCache[k]
	e.once.Do(func() { e.m = buildEnv(envRadiance[k]) })
	return e.m
}

// buildEnv evaluates a radiance function over a 256x128 equirect map, 3x3
// samples per texel so small lights keep their energy, and prefilters it.
func buildEnv(f func(d vec3) vec3) *envMap {
	const w, h, sub = 256, 128, 3
	rgb := make([]float32, w*h*3)
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < w; x++ {
				var s vec3
				for sy := 0; sy < sub; sy++ {
					for sx := 0; sx < sub; sx++ {
						u := (float64(x) + (float64(sx)+0.5)/sub) / w
						t := (float64(y) + (float64(sy)+0.5)/sub) / h
						s = s.add(f(equirectDir(u, t)))
					}
				}
				o := (y*w + x) * 3
				s = s.mul(1.0 / (sub * sub))
				rgb[o], rgb[o+1], rgb[o+2] = float32(s[0]), float32(s[1]), float32(s[2])
			}
		}
	})
	base := envLevel{w, h, rgb}
	small := downsample(base, 64)
	e := &envMap{}
	for _, r := range envRoughness {
		if r == 0 {
			e.levels = append(e.levels, base)
			continue
		}
		e.levels = append(e.levels, prefilter(small, r))
	}
	return e
}

// equirectDir is the world direction at equirect coordinates u (0 to 1 left
// to right) and t (0 to 1 top to bottom); three.js equirectUv inverse:
// u = atan(z, x)/2pi + 0.5, latitude = (0.5 - t) pi.
func equirectDir(u, t float64) vec3 {
	phi := (u - 0.5) * 2 * math.Pi
	lat := (0.5 - t) * math.Pi
	return vec3{math.Cos(lat) * math.Cos(phi), math.Sin(lat), math.Cos(lat) * math.Sin(phi)}
}

func downsample(l envLevel, tw int) envLevel {
	if l.w <= tw {
		return l
	}
	f := l.w / tw
	th := l.h / f
	out := make([]float32, tw*th*3)
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			var s [3]float32
			for yy := 0; yy < f; yy++ {
				for xx := 0; xx < f; xx++ {
					o := ((y*f+yy)*l.w + x*f + xx) * 3
					s[0] += l.rgb[o]
					s[1] += l.rgb[o+1]
					s[2] += l.rgb[o+2]
				}
			}
			k := float32(f * f)
			o := (y*tw + x) * 3
			out[o], out[o+1], out[o+2] = s[0]/k, s[1]/k, s[2]/k
		}
	}
	return envLevel{tw, th, out}
}

// prefilter convolves with a lobe whose width follows GGX roughness (a
// normalised power-cosine with exponent 2/a^2 - 2, a = roughness^2), into
// 64x32 for roughness up to .4 and 32x16 above.
func prefilter(src envLevel, rough float64) envLevel {
	a := rough * rough
	n := math.Max(2/(a*a)-2, 1)
	ow, oh := 32, 16
	if rough <= 0.4 {
		ow, oh = 64, 32
	}
	dirs := make([]vec3, src.w*src.h)
	wts := make([]float64, src.w*src.h)
	for y := 0; y < src.h; y++ {
		t := (float64(y) + 0.5) / float64(src.h)
		for x := 0; x < src.w; x++ {
			dirs[y*src.w+x] = equirectDir((float64(x)+0.5)/float64(src.w), t)
			wts[y*src.w+x] = math.Cos((0.5 - t) * math.Pi) // texel solid angle
		}
	}
	out := make([]float32, ow*oh*3)
	parallel(oh, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < ow; x++ {
				d := equirectDir((float64(x)+0.5)/float64(ow), (float64(y)+0.5)/float64(oh))
				var s [3]float64
				var ws float64
				for i, sd := range dirs {
					c := d.dot(sd)
					if c <= 0 {
						continue
					}
					wt := math.Pow(c, n) * wts[i]
					s[0] += wt * float64(src.rgb[i*3])
					s[1] += wt * float64(src.rgb[i*3+1])
					s[2] += wt * float64(src.rgb[i*3+2])
					ws += wt
				}
				o := (y*ow + x) * 3
				if ws > 0 {
					out[o], out[o+1], out[o+2] = float32(s[0]/ws), float32(s[1]/ws), float32(s[2]/ws)
				}
			}
		}
	})
	return envLevel{ow, oh, out}
}

func (l *envLevel) at(d vec3) vec3 {
	u := math.Atan2(d[2], d[0])/(2*math.Pi) + 0.5
	v := math.Asin(math.Max(-1, math.Min(1, d[1])))/math.Pi + 0.5
	x := u*float64(l.w) - 0.5
	y := (1-v)*float64(l.h) - 0.5
	fx, fy := math.Floor(x), math.Floor(y)
	ax, ay := x-fx, y-fy
	x0, x1 := wrap(int(fx), l.w), wrap(int(fx)+1, l.w)
	y0, y1 := clampI(int(fy), 0, l.h-1), clampI(int(fy)+1, 0, l.h-1)
	var out vec3
	for c := 0; c < 3; c++ {
		a := float64(l.rgb[(y0*l.w+x0)*3+c])*(1-ax) + float64(l.rgb[(y0*l.w+x1)*3+c])*ax
		b := float64(l.rgb[(y1*l.w+x0)*3+c])*(1-ax) + float64(l.rgb[(y1*l.w+x1)*3+c])*ax
		out[c] = a*(1-ay) + b*ay
	}
	return out
}

// radiance samples the environment along direction d (world, unit length)
// at a roughness from 0 to 1, blending the two nearest levels.
func (e *envMap) radiance(d vec3, rough float64) vec3 {
	rough = math.Max(0, math.Min(1, rough))
	f := rough / 0.2
	i := int(f)
	if i >= len(e.levels)-1 {
		return e.levels[len(e.levels)-1].at(d)
	}
	t := f - float64(i)
	a := e.levels[i].at(d)
	if t == 0 {
		return a
	}
	return mix3(a, e.levels[i+1].at(d), t)
}
