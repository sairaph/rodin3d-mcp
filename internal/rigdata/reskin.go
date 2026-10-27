package rigdata

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/fbx"
)

// Bone is one entry of rodinrig_hierarchy as the web's reskin sends it.
type Bone struct {
	Name      string    `json:"name"`
	Parent    int       `json:"parent"`
	Length    *float64  `json:"length,omitempty"`
	Transform []float64 `json:"transform"`
}

// matchName is the editor's name key for matching imported and generated
// bones (cf/7859:3741, M): no namespace, no mixamorig prefix, no spaces,
// "_", "." or "-", lower case.
func matchName(s string) string {
	s = mixamoNoColon.ReplaceAllString(afterColon(s), "")
	s = strings.Map(func(r rune) rune {
		if r == '_' || r == '.' || r == '-' || isSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.ToLower(s)
}

// Reskin is the web's FBX reskin: the generated joints of the rig made
// from the FBX's bone names, each turned to its imported bone's frame
// (k), then each bone's Y axis laid along the generated bone (the import
// positions handler), as the hierarchy of the second rodin_rigging call.
func Reskin(imported []fbx.Bone, joints []Joint) ([]Bone, error) {
	adjusted, err := importFrames(imported, joints)
	if err != nil {
		return nil, err
	}
	adjusted = alignToBones(adjusted, joints)
	var out []Bone
	for _, j := range adjusted {
		name := trim(j.Label)
		if name == "" || fbx.IsPoseBone(name) {
			continue
		}
		b := Bone{Name: name, Parent: j.Parent, Length: j.Length, Transform: []float64{}}
		if j.Transform.Finite() {
			b.Transform = append([]float64(nil), j.Transform[:]...)
		}
		out = append(out, b)
	}
	return out, nil
}

// importFrames is k (cf/7859:3748-3895).
func importFrames(e []fbx.Bone, t []Joint) ([]Joint, error) {
	byName := map[string]int{}
	for i, j := range t {
		key := matchName(j.Label)
		if _, dup := byName[key]; dup {
			return nil, fmt.Errorf("ambiguous generated bone: %s", j.Label)
		}
		byName[key] = i
	}
	used := map[int]bool{}
	r := make([]int, len(e))
	for i, b := range e {
		k, ok := byName[matchName(b.Name)]
		if !ok || used[k] {
			return nil, fmt.Errorf("cannot match imported bone: %s", b.Name)
		}
		used[k], r[i] = true, k
	}
	pos := func(m fbx.Mat4) (fbx.Vec3, error) {
		if !m.Finite() {
			return fbx.Vec3{}, errors.New("missing or invalid skeleton transform")
		}
		return m.Position(), nil
	}
	a := make([]fbx.Vec3, len(t))
	for i, j := range t {
		var err error
		if a[i], err = pos(j.Transform); err != nil {
			return nil, err
		}
	}
	l := make([]fbx.Vec3, len(e))
	d := make([]fbx.Quat, len(e))
	for i, b := range e {
		var err error
		if l[i], err = pos(b.Transform); err != nil {
			return nil, err
		}
		_, q, s := b.Transform.Decompose()
		if !finiteQuat(q) || math.Abs(s.X) < 1e-12 || math.Abs(s.Y) < 1e-12 || math.Abs(s.Z) < 1e-12 {
			return nil, fmt.Errorf("invalid imported local frame: %s", b.Name)
		}
		d[i] = q.Normalize()
	}
	root := false
	for _, b := range e {
		if b.Parent == -1 {
			root = true
		}
	}
	if !root {
		return nil, errors.New("missing imported skeleton root")
	}
	for n, b := range e {
		if b.Parent < 0 {
			continue
		}
		if b.Parent >= len(e) || b.Parent == n {
			return nil, errors.New("invalid imported skeleton hierarchy")
		}
		o := t[r[n]].Parent
		seen := map[int]bool{}
		for o >= 0 && !used[o] && !seen[o] {
			seen[o] = true
			if o >= len(t) {
				o = -2
				break
			}
			o = t[o].Parent
		}
		if o != r[b.Parent] {
			return nil, fmt.Errorf("skeleton parent mismatch: %s", b.Name)
		}
	}
	kids := make([][]int, len(t))
	for i, j := range t {
		if j.Parent == -1 {
			continue
		}
		if j.Parent < 0 || j.Parent >= len(t) || j.Parent == i {
			return nil, errors.New("invalid generated skeleton hierarchy")
		}
		kids[j.Parent] = append(kids[j.Parent], i)
	}
	done, onPath := map[int]bool{}, map[int]bool{}
	var visit func(i int) error
	visit = func(i int) error {
		if onPath[i] {
			return errors.New("cyclic generated skeleton hierarchy")
		}
		if done[i] {
			return nil
		}
		onPath[i] = true
		for _, c := range kids[i] {
			if err := visit(c); err != nil {
				return err
			}
		}
		delete(onPath, i)
		done[i] = true
		return nil
	}
	for i := range t {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	p := make([][]int, len(e))
	for i, b := range e {
		if b.Parent >= 0 {
			p[b.Parent] = append(p[b.Parent], i)
		}
	}
	g := map[int]fbx.Quat{}
	var f func(n int, inherited fbx.Quat)
	f = func(n int, inherited fbx.Quat) {
		near := append([]int(nil), p[n]...)
		if len(near) == 0 && e[n].Parent >= 0 {
			near = append(near, e[n].Parent)
		}
		pairs := make([][2]fbx.Vec3, len(near))
		for i, x := range near {
			pairs[i] = [2]fbx.Vec3{l[x].Sub(l[n]), a[r[x]].Sub(a[r[n]])}
		}
		s, ok := fitRotation(pairs)
		if !ok {
			s = inherited
		}
		g[r[n]] = s.Mul(d[n]).Normalize()
		for _, c := range p[n] {
			f(c, s)
		}
	}
	for n, b := range e {
		if b.Parent == -1 {
			f(n, fbx.Quat{W: 1})
		}
	}
	out := make([]Joint, len(t))
	for i, j := range t {
		out[i] = j
		if q, ok := g[i]; ok {
			_, _, s := j.Transform.Decompose()
			out[i].Transform = fbx.Compose(a[i], q, s)
		}
	}
	return out, nil
}

// fitRotation is k's best rotation from the imported child directions to
// the generated ones: the first pair fixes one axis, the first other pair
// that is not parallel fixes the roll; one usable pair alone gives the
// shortest arc.
func fitRotation(pairs [][2]fbx.Vec3) (fbx.Quat, bool) {
	var t [][2]fbx.Vec3
	for _, p := range pairs {
		if p[0].LengthSq() > 1e-12 && p[1].LengthSq() > 1e-12 {
			t = append(t, p)
		}
	}
	if len(t) == 0 {
		return fbx.Quat{}, false
	}
	i, n := t[0][0].Normalize(), t[0][1].Normalize()
	for _, s := range t[1:] {
		e, u := s[0].Normalize(), s[1].Normalize()
		e = e.AddScaled(i, -e.Dot(i))
		u = u.AddScaled(n, -u.Dot(n))
		if e.LengthSq() < 1e-8 || u.LengthSq() < 1e-8 {
			continue
		}
		e, u = e.Normalize(), u.Normalize()
		r := fbx.QuatFromRotation(fbx.Basis(i, e, i.Cross(e)))
		return fbx.QuatFromRotation(fbx.Basis(n, u, n.Cross(u))).Mul(r.Invert()).Normalize(), true
	}
	return fbx.QuatFromUnitVectors(i, n), true
}

func finiteQuat(q fbx.Quat) bool {
	for _, v := range [4]float64{q.X, q.Y, q.Z, q.W} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// alignToBones is the import positions handler's last step (cf/7859:
// 13054-13110): each bone is turned, the least way, so its Y axis points
// along the generated bone as the editor draws it (head at the bind
// position, along the bind Y axis). The editor skips a turn below 1e-6 of
// the model's size over the bone's drawn length; the model's size is taken
// here as the extent of the skeleton.
func alignToBones(js []Joint, bind []Joint) []Joint {
	maxSize := extent(bind)
	out := make([]Joint, len(js))
	for i, j := range js {
		out[i] = j
		d, h, c := j.Transform.Decompose()
		_, bq, _ := bind[i].Transform.Decompose()
		m := fbx.Vec3{Y: 1}.ApplyQuat(bq).Normalize()
		u := displayLength(i, bind, maxSize)
		if u <= 1e-8*maxSize || m.LengthSq() <= 1e-12 {
			continue
		}
		p := fbx.Vec3{Y: 1}.ApplyQuat(h).Normalize()
		if p.DistanceTo(m)*u <= 1e-6*maxSize {
			continue
		}
		h = fbx.QuatFromUnitVectors(p, m).Mul(h).Normalize()
		out[i].Transform = fbx.Compose(d, h, c)
	}
	return out
}

// displayLength is the drawn length of joint i (_buildSkeletonEditBoneDisplays):
// its length, else the mean distance to its children, else to its parent,
// else 5.5 % of the model, at least 0.4 % of the model.
func displayLength(i int, bind []Joint, maxSize float64) float64 {
	if l := bind[i].Length; l != nil {
		return *l
	}
	head := bind[i].Transform.Position()
	var sum float64
	var n int
	for _, j := range bind {
		if j.Parent == i {
			if dist := j.Transform.Position().DistanceTo(head); dist > 1e-4 {
				sum, n = sum+dist, n+1
			}
		}
	}
	v := 0.055 * maxSize
	switch {
	case n > 0:
		v = sum / float64(n)
	case bind[i].Parent >= 0:
		if f := bind[bind[i].Parent].Transform.Position().DistanceTo(head); f > 1e-4 {
			v = f
		}
	}
	return math.Max(v, 0.004*maxSize)
}

func extent(js []Joint) float64 {
	if len(js) == 0 {
		return 0.001
	}
	lo, hi := js[0].Transform.Position(), js[0].Transform.Position()
	for _, j := range js {
		p := j.Transform.Position()
		lo = fbx.Vec3{X: math.Min(lo.X, p.X), Y: math.Min(lo.Y, p.Y), Z: math.Min(lo.Z, p.Z)}
		hi = fbx.Vec3{X: math.Max(hi.X, p.X), Y: math.Max(hi.Y, p.Y), Z: math.Max(hi.Z, p.Z)}
	}
	return math.Max(math.Max(hi.X-lo.X, hi.Y-lo.Y), math.Max(hi.Z-lo.Z, 0.001))
}
